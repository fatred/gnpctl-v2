// Package config loads gnpctl's YAML configuration: the state database, the
// catalog directory, logging and the optional data-plane render block.
//
// Behaviour mirrors the Python gnpctl.config module, including pydantic's lax
// coercion rules (numeric strings for ints, "true"/1 for bools) and its
// acceptance of unknown keys. Validation failures are reported in pydantic's
// multi-line shape, without the input_value repr and errors.pydantic.dev link.
package config

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	// EnvVar names the config path when no explicit path is given.
	EnvVar = "GNPCONFIG"
	// DefaultConfigPath is used when neither an explicit path nor EnvVar is set.
	DefaultConfigPath = "gnpctl.yaml"
)

// ConfigError is raised when the configuration cannot be located, parsed or validated.
type ConfigError struct{ msg string }

func (e *ConfigError) Error() string { return e.msg }

func errorf(format string, args ...any) *ConfigError {
	return &ConfigError{msg: fmt.Sprintf(format, args...)}
}

// LogConfig is the log destination and level. An empty Path means stderr.
type LogConfig struct {
	Path  string
	Level string
}

// JetstreamOutputTuning is the ingress jetstream-output tuning.
type JetstreamOutputTuning struct {
	NumWorkers      int
	WriteTimeout    string
	ConnectTimeWait string
	MaxAge          string
}

// JetstreamInputTuning is the egress jetstream-input (durable consumer) tuning.
type JetstreamInputTuning struct {
	NumWorkers     int
	FetchBatchSize int
	MaxAckPending  int
	BufferSize     int
	DeliverPolicy  string
}

// NatsConfig is the NATS address plus both stream tunings.
type NatsConfig struct {
	Address string
	Output  JetstreamOutputTuning
	Input   JetstreamInputTuning
}

// PrometheusConfig is the egress remote_write endpoint and tuning.
type PrometheusConfig struct {
	RemoteWriteURL        string
	Interval              string
	BufferSize            int
	MaxTimeSeriesPerWrite int
	Timeout               string
	NumWorkers            int
}

// GnmicConfig is emitted into the rendered ingress/egress configs. Credentials
// never appear here: they are env references resolved by gnmic at runtime.
type GnmicConfig struct {
	SkipVerify  bool
	TLSCA       *string
	TargetsFile string
	Instance    *string
	APIServer   *string
}

// RenderConfig is the optional `render` block. Pointer fields are unset-or-value.
type RenderConfig struct {
	OutputDir          string
	TargetDeviceDomain *string
	Nats               NatsConfig
	Prometheus         PrometheusConfig
	Gnmic              GnmicConfig
}

// Config is the loaded configuration.
type Config struct {
	Database string
	Catalog  string
	Log      LogConfig
	Render   *RenderConfig
}

const skipVerifyTLSCAConflict = "skip_verify: true cannot be combined with tls_ca — set tls_ca to trust a private CA (the secure escape hatch), or use skip_verify alone only where no trusted CA exists. Pick one, not both."

const requireRenderMessage = `render needs a 'render' block in your config file (the one $GNPCONFIG points to, else ./gnpctl.yaml) — these are config keys, not CLI flags. Add:

  render:
    output_dir: ./render
    nats:
      address: nats:4222
    prometheus:
      remote_write_url: http://prometheus:9090/api/v1/write

Only --output-dir can be overridden on the command line.`

// Load resolves, parses and validates the configuration. An empty path falls
// back to $GNPCONFIG, else ./gnpctl.yaml.
func Load(path string) (*Config, error) {
	resolved := resolvePath(path)
	if _, err := os.Stat(resolved); err != nil {
		return nil, errorf("config file not found: %s", resolved)
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return nil, errorf("cannot read config %s: %v", resolved, err)
	}
	var root any
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, errorf("invalid YAML in config %s: %v", resolved, err)
	}
	m, ok := asMapping(root)
	if !ok {
		return nil, errorf("config %s must be a YAML mapping", resolved)
	}
	v := &validator{}
	cfg := build(v, m)
	if len(v.errs) > 0 {
		return nil, errorf("invalid config %s: %s", resolved, v.message())
	}
	return cfg, nil
}

// ApplyOverrides applies CLI overrides over config values (flags win). An empty
// argument leaves the corresponding config value untouched.
func (c *Config) ApplyOverrides(state, catalog string) *Config {
	if state != "" {
		c.Database = state
	}
	if catalog != "" {
		c.Catalog = catalog
	}
	return c
}

// RequireRender returns the render block or fails clearly when it is absent.
func (c *Config) RequireRender() (*RenderConfig, error) {
	if c.Render == nil {
		return nil, errorf("%s", requireRenderMessage)
	}
	return c.Render, nil
}

// SetupLogging points slog at the configured destination and level. An empty
// path logs to stderr; an unrecognised level falls back to info.
func SetupLogging(c *Config) error {
	var w io.Writer = os.Stderr
	if c.Log.Path != "" {
		f, err := os.OpenFile(c.Log.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		w = f
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: logLevel(c.Log.Level)})))
	return nil
}

func logLevel(level string) slog.Level {
	switch strings.ToUpper(level) {
	case "DEBUG":
		return slog.LevelDebug
	case "WARN", "WARNING":
		return slog.LevelWarn
	case "ERROR":
		return slog.LevelError
	case "CRITICAL", "FATAL":
		// Python's CRITICAL (50) is above ERROR (40): error records are suppressed.
		return slog.LevelError + 1
	}
	return slog.LevelInfo
}

func resolvePath(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if env := os.Getenv(EnvVar); env != "" {
		return env
	}
	return DefaultConfigPath
}

func build(v *validator, m map[string]any) *Config {
	c := &Config{
		Database: v.requiredString(m, "database", "database"),
		Catalog:  v.requiredString(m, "catalog", "catalog"),
		Log:      LogConfig{Level: "info"},
	}
	if lm, ok := v.defaultModel(m, "log", "log", "LogConfig"); ok {
		c.Log.Path = deref(v.optString(lm, "path", "log.path"))
		c.Log.Level = v.stringDefault(lm, "level", "log.level", "info")
	}
	// `render` is Optional: absent or null both mean "not configured".
	if raw, ok := m["render"]; ok && raw != nil {
		if rm, ok := v.subModel(raw, "render", "RenderConfig"); ok {
			c.Render = buildRender(v, rm)
		}
	}
	return c
}

func buildRender(v *validator, m map[string]any) *RenderConfig {
	r := &RenderConfig{
		OutputDir:          v.requiredString(m, "output_dir", "render.output_dir"),
		TargetDeviceDomain: v.optString(m, "target_device_domain", "render.target_device_domain"),
		Nats:               NatsConfig{Output: defaultOutputTuning(), Input: defaultInputTuning()},
		Prometheus:         defaultPrometheus(),
		Gnmic:              GnmicConfig{TargetsFile: "targets.yaml"},
	}
	if nm, ok := v.model(m, "nats", "render.nats", "NatsConfig"); ok {
		r.Nats = buildNats(v, nm)
	}
	if pm, ok := v.model(m, "prometheus", "render.prometheus", "PrometheusConfig"); ok {
		r.Prometheus = buildPrometheus(v, pm)
	}
	if gm, ok := v.defaultModel(m, "gnmic", "render.gnmic", "GnmicConfig"); ok {
		r.Gnmic = buildGnmic(v, gm)
	}
	return r
}

func defaultOutputTuning() JetstreamOutputTuning {
	return JetstreamOutputTuning{NumWorkers: 1, WriteTimeout: "5s", ConnectTimeWait: "2s", MaxAge: "1m"}
}

func defaultInputTuning() JetstreamInputTuning {
	return JetstreamInputTuning{NumWorkers: 10, FetchBatchSize: 500, MaxAckPending: 100000, BufferSize: 10000, DeliverPolicy: "all"}
}

func defaultPrometheus() PrometheusConfig {
	return PrometheusConfig{Interval: "2s", BufferSize: 100000, MaxTimeSeriesPerWrite: 10000, Timeout: "10s", NumWorkers: 10}
}

func buildNats(v *validator, m map[string]any) NatsConfig {
	n := NatsConfig{
		Address: v.requiredString(m, "address", "render.nats.address"),
		Output:  defaultOutputTuning(),
		Input:   defaultInputTuning(),
	}
	if om, ok := v.defaultModel(m, "output", "render.nats.output", "JetstreamOutputTuning"); ok {
		const p = "render.nats.output."
		n.Output = JetstreamOutputTuning{
			NumWorkers:      v.intDefault(om, "num_workers", p+"num_workers", n.Output.NumWorkers),
			WriteTimeout:    v.stringDefault(om, "write_timeout", p+"write_timeout", n.Output.WriteTimeout),
			ConnectTimeWait: v.stringDefault(om, "connect_time_wait", p+"connect_time_wait", n.Output.ConnectTimeWait),
			MaxAge:          v.stringDefault(om, "max_age", p+"max_age", n.Output.MaxAge),
		}
	}
	if im, ok := v.defaultModel(m, "input", "render.nats.input", "JetstreamInputTuning"); ok {
		const p = "render.nats.input."
		n.Input = JetstreamInputTuning{
			NumWorkers:     v.intDefault(im, "num_workers", p+"num_workers", n.Input.NumWorkers),
			FetchBatchSize: v.intDefault(im, "fetch_batch_size", p+"fetch_batch_size", n.Input.FetchBatchSize),
			MaxAckPending:  v.intDefault(im, "max_ack_pending", p+"max_ack_pending", n.Input.MaxAckPending),
			BufferSize:     v.intDefault(im, "buffer_size", p+"buffer_size", n.Input.BufferSize),
			DeliverPolicy:  v.stringDefault(im, "deliver_policy", p+"deliver_policy", n.Input.DeliverPolicy),
		}
	}
	return n
}

func buildPrometheus(v *validator, m map[string]any) PrometheusConfig {
	p := defaultPrometheus()
	p.RemoteWriteURL = v.requiredString(m, "remote_write_url", "render.prometheus.remote_write_url")
	const path = "render.prometheus."
	p.Interval = v.stringDefault(m, "interval", path+"interval", p.Interval)
	p.BufferSize = v.intDefault(m, "buffer_size", path+"buffer_size", p.BufferSize)
	p.MaxTimeSeriesPerWrite = v.intDefault(m, "max_time_series_per_write", path+"max_time_series_per_write", p.MaxTimeSeriesPerWrite)
	p.Timeout = v.stringDefault(m, "timeout", path+"timeout", p.Timeout)
	p.NumWorkers = v.intDefault(m, "num_workers", path+"num_workers", p.NumWorkers)
	return p
}

func buildGnmic(v *validator, m map[string]any) GnmicConfig {
	g := GnmicConfig{TargetsFile: "targets.yaml"}
	g.SkipVerify = v.boolDefault(m, "skip_verify", "render.gnmic.skip_verify", false)
	g.TLSCA = v.optString(m, "tls_ca", "render.gnmic.tls_ca")
	g.TargetsFile = v.stringDefault(m, "targets_file", "render.gnmic.targets_file", g.TargetsFile)
	g.Instance = v.optString(m, "instance", "render.gnmic.instance")
	g.APIServer = v.optString(m, "api_server", "render.gnmic.api_server")
	// A CA bundle is the escape hatch used INSTEAD of disabling verification.
	if g.SkipVerify && g.TLSCA != nil && *g.TLSCA != "" {
		v.add("render.gnmic", "Value error, "+skipVerifyTLSCAConflict, "value_error")
	}
	return g
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// fieldError is one pydantic-shaped validation failure.
type fieldError struct{ path, msg, typ string }

type validator struct{ errs []fieldError }

func (v *validator) add(path, msg, typ string) {
	v.errs = append(v.errs, fieldError{path: path, msg: msg, typ: typ})
}

func (v *validator) message() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d validation error", len(v.errs))
	if len(v.errs) != 1 {
		b.WriteString("s")
	}
	b.WriteString(" for Config")
	for _, e := range v.errs {
		fmt.Fprintf(&b, "\n%s\n  %s [type=%s]", e.path, e.msg, e.typ)
	}
	return b.String()
}

func (v *validator) strAt(raw any, path string) (string, bool) {
	if s, ok := raw.(string); ok {
		return s, true
	}
	v.add(path, "Input should be a valid string", "string_type")
	return "", false
}

func (v *validator) requiredString(m map[string]any, key, path string) string {
	raw, ok := m[key]
	if !ok {
		v.add(path, "Field required", "missing")
		return ""
	}
	s, _ := v.strAt(raw, path)
	return s
}

func (v *validator) stringDefault(m map[string]any, key, path, def string) string {
	raw, ok := m[key]
	if !ok {
		return def
	}
	s, _ := v.strAt(raw, path)
	return s
}

func (v *validator) optString(m map[string]any, key, path string) *string {
	raw, ok := m[key]
	if !ok || raw == nil {
		return nil
	}
	s, ok := v.strAt(raw, path)
	if !ok {
		return nil
	}
	return &s
}

func (v *validator) intDefault(m map[string]any, key, path string, def int) int {
	raw, ok := m[key]
	if !ok {
		return def
	}
	n, _ := v.intAt(raw, path)
	return n
}

func (v *validator) intAt(raw any, path string) (int, bool) {
	switch t := raw.(type) {
	case int:
		return t, true
	case int64:
		return int(t), true
	case uint64:
		return int(t), true
	case float64:
		if t == float64(int(t)) {
			return int(t), true
		}
		v.add(path, "Input should be a valid integer, got a number with a fractional part", "int_from_float")
		return 0, false
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(t)); err == nil {
			return n, true
		}
		v.add(path, "Input should be a valid integer, unable to parse string as an integer", "int_parsing")
		return 0, false
	}
	v.add(path, "Input should be a valid integer", "int_type")
	return 0, false
}

func (v *validator) boolDefault(m map[string]any, key, path string, def bool) bool {
	raw, ok := m[key]
	if !ok {
		return def
	}
	b, _ := v.boolAt(raw, path)
	return b
}

func (v *validator) boolAt(raw any, path string) (bool, bool) {
	switch t := raw.(type) {
	case bool:
		return t, true
	case int:
		switch t {
		case 0:
			return false, true
		case 1:
			return true, true
		}
	case string:
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "1", "on", "t", "true", "y", "yes":
			return true, true
		case "0", "off", "f", "false", "n", "no":
			return false, true
		}
	}
	v.add(path, "Input should be a valid boolean, unable to interpret input", "bool_parsing")
	return false, false
}

// model decodes a required nested mapping.
func (v *validator) model(m map[string]any, key, path, name string) (map[string]any, bool) {
	raw, ok := m[key]
	if !ok {
		v.add(path, "Field required", "missing")
		return nil, false
	}
	return v.subModel(raw, path, name)
}

// defaultModel decodes a nested mapping that may be absent (the field's default
// then applies) but not null, which pydantic rejects for non-Optional models.
func (v *validator) defaultModel(m map[string]any, key, path, name string) (map[string]any, bool) {
	raw, ok := m[key]
	if !ok {
		return nil, false
	}
	return v.subModel(raw, path, name)
}

func (v *validator) subModel(raw any, path, name string) (map[string]any, bool) {
	if mm, ok := asMapping(raw); ok {
		return mm, true
	}
	v.add(path, "Input should be a valid dictionary or instance of "+name, "model_type")
	return nil, false
}

func asMapping(raw any) (map[string]any, bool) {
	switch t := raw.(type) {
	case map[string]any:
		return t, true
	case map[any]any:
		m := make(map[string]any, len(t))
		for k, val := range t {
			s, ok := k.(string)
			if !ok {
				return nil, false
			}
			m[s] = val
		}
		return m, true
	}
	return nil, false
}
