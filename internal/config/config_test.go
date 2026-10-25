package config

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const valid = `
database: /var/lib/gnpctl/state.db
catalog: /etc/gnpctl/catalog
log:
  path: /var/log/gnpctl.log
  level: debug
`

func writeNamed(t *testing.T, name, text string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func writeCfg(t *testing.T, text string) string { return writeNamed(t, "g.yaml", text) }

func mustLoad(t *testing.T, text string) *Config {
	t.Helper()
	c, err := Load(writeCfg(t, text))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return c
}

func loadErr(t *testing.T, text string) error {
	t.Helper()
	_, err := Load(writeCfg(t, text))
	if err == nil {
		t.Fatal("expected a ConfigError, got nil")
	}
	var ce *ConfigError
	if !errors.As(err, &ce) {
		t.Fatalf("not a ConfigError: %v", err)
	}
	return err
}

func wantContains(t *testing.T, err error, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if !strings.Contains(err.Error(), s) {
			t.Fatalf("error %q missing %q", err, s)
		}
	}
}

func TestPathFromEnv(t *testing.T) {
	t.Setenv(EnvVar, writeCfg(t, valid))
	c, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if c.Database != "/var/lib/gnpctl/state.db" || c.Catalog != "/etc/gnpctl/catalog" {
		t.Fatalf("got %q / %q", c.Database, c.Catalog)
	}
}

func TestExplicitPathBeatsEnv(t *testing.T) {
	t.Setenv(EnvVar, filepath.Join(t.TempDir(), "absent.yaml"))
	if _, err := Load(writeCfg(t, valid)); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultPathWhenUnset(t *testing.T) {
	t.Setenv(EnvVar, "")
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, "gnpctl.yaml"), []byte(valid), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if c.Catalog != "/etc/gnpctl/catalog" {
		t.Fatalf("catalog = %q", c.Catalog)
	}
}

func TestMissingConfigFailsFast(t *testing.T) {
	t.Setenv(EnvVar, filepath.Join(t.TempDir(), "nope.yaml"))
	_, err := Load("")
	if err == nil {
		t.Fatal("expected error")
	}
	wantContains(t, err, "config file not found:", "nope.yaml")
}

func TestInvalidYAMLFailsFast(t *testing.T) {
	bad := writeNamed(t, "bad.yaml", "database: [unclosed\n")
	_, err := Load(bad)
	if err == nil {
		t.Fatal("expected error")
	}
	wantContains(t, err, "bad.yaml", "invalid YAML in config")
}

func TestNonMappingRootFailsFast(t *testing.T) {
	wantContains(t, loadErr(t, "- a\n- b\n"), "must be a YAML mapping")
	wantContains(t, loadErr(t, ""), "must be a YAML mapping")
}

func TestCoreSettingsParsed(t *testing.T) {
	c := mustLoad(t, valid)
	if c.Database == "" || c.Catalog == "" {
		t.Fatal("database/catalog empty")
	}
	if c.Log.Path != "/var/log/gnpctl.log" || c.Log.Level != "debug" {
		t.Fatalf("log = %+v", c.Log)
	}
}

func TestLogDefaults(t *testing.T) {
	c := mustLoad(t, "database: db\ncatalog: cat\n")
	if c.Log.Path != "" || c.Log.Level != "info" {
		t.Fatalf("log = %+v", c.Log)
	}
}

// pydantic models ignore unknown keys (default extra='ignore'); match it.
func TestUnknownKeysIgnored(t *testing.T) {
	c := mustLoad(t, valid+`
bogus: 1
render:
  output_dir: ./render
  bogus: {x: 1}
  nats:
    address: nats:4222
    bogus: true
  prometheus:
    remote_write_url: http://prom:9090/api/v1/write
  gnmic:
    bogus: 1
`)
	if c.Render == nil {
		t.Fatal("render missing")
	}
}

func TestMissingRequiredKeyRejected(t *testing.T) {
	err := loadErr(t, "catalog: cat\n")
	wantContains(t, err, "invalid config", "1 validation error for Config", "\ndatabase\n", "Field required [type=missing]")
}

func TestWrongTypeRejected(t *testing.T) {
	wantContains(t, loadErr(t, "database: 5\ncatalog: cat\n"), "database", "Input should be a valid string [type=string_type]")
}

// pydantic's lax mode coerces numeric strings and integer booleans.
func TestLaxCoercion(t *testing.T) {
	c := mustLoad(t, valid+`
render:
  output_dir: ./render
  nats:
    address: nats:4222
    output:
      num_workers: "2"
  prometheus:
    remote_write_url: http://prom:9090/api/v1/write
  gnmic:
    skip_verify: "true"
`)
	if c.Render.Nats.Output.NumWorkers != 2 || !c.Render.Gnmic.SkipVerify {
		t.Fatalf("render = %+v", c.Render)
	}
}

const render = valid + `
render:
  output_dir: ./render
  nats:
    address: nats:4222
  prometheus:
    remote_write_url: http://prom:9090/api/v1/write
`

func TestRenderConfigParsed(t *testing.T) {
	c := mustLoad(t, render)
	r := c.Render
	if r == nil {
		t.Fatal("render missing")
	}
	if r.OutputDir != "./render" || r.Nats.Address != "nats:4222" || r.Prometheus.RemoteWriteURL != "http://prom:9090/api/v1/write" {
		t.Fatalf("render = %+v", r)
	}
}

func TestRenderConfigOptional(t *testing.T) {
	if c := mustLoad(t, valid); c.Render != nil {
		t.Fatalf("render = %+v, want nil", c.Render)
	}
	if c := mustLoad(t, valid+"render: null\n"); c.Render != nil {
		t.Fatalf("render = %+v, want nil", c.Render)
	}
}

func TestRenderMinimalTakesDefaults(t *testing.T) {
	r := mustLoad(t, render).Render
	if r.TargetDeviceDomain != nil {
		t.Fatalf("target_device_domain = %v", *r.TargetDeviceDomain)
	}
	g := r.Gnmic
	if g.SkipVerify || g.TLSCA != nil || g.TargetsFile != "targets.yaml" || g.Instance != nil || g.APIServer != nil {
		t.Fatalf("gnmic = %+v", g)
	}
	o := r.Nats.Output
	if o.NumWorkers != 1 || o.WriteTimeout != "5s" || o.ConnectTimeWait != "2s" || o.MaxAge != "1m" {
		t.Fatalf("nats.output = %+v", o)
	}
	in := r.Nats.Input
	if in.NumWorkers != 10 || in.FetchBatchSize != 500 || in.MaxAckPending != 100000 || in.BufferSize != 10000 || in.DeliverPolicy != "all" {
		t.Fatalf("nats.input = %+v", in)
	}
	p := r.Prometheus
	if p.Interval != "2s" || p.BufferSize != 100000 || p.MaxTimeSeriesPerWrite != 10000 || p.Timeout != "10s" || p.NumWorkers != 10 {
		t.Fatalf("prometheus = %+v", p)
	}
}

const renderFull = valid + `
render:
  output_dir: ./render
  target_device_domain: example.net
  nats:
    address: nats:4222
    output:
      num_workers: 2
      write_timeout: 7s
      connect_time_wait: 3s
      max_age: 2m
    input:
      num_workers: 20
      fetch_batch_size: 250
      max_ack_pending: 50000
      buffer_size: 5000
      deliver_policy: new
  prometheus:
    remote_write_url: http://prom:9090/api/v1/write
    interval: 1s
    buffer_size: 200000
    max_time_series_per_write: 5000
    timeout: 15s
    num_workers: 4
  gnmic:
    skip_verify: false
    tls_ca: /app/ca-chain.pem
    targets_file: /app/targets.yaml
    instance: vmk-netpoczur1-01:9804
    api_server: ":7890"
`

func TestRenderDeploymentKnobsParsedAndOverridable(t *testing.T) {
	r := mustLoad(t, renderFull).Render
	if r.TargetDeviceDomain == nil || *r.TargetDeviceDomain != "example.net" {
		t.Fatalf("target_device_domain = %v", r.TargetDeviceDomain)
	}
	g := r.Gnmic
	if g.SkipVerify {
		t.Fatal("skip_verify = true")
	}
	if g.TLSCA == nil || *g.TLSCA != "/app/ca-chain.pem" ||
		g.TargetsFile != "/app/targets.yaml" ||
		g.Instance == nil || *g.Instance != "vmk-netpoczur1-01:9804" ||
		g.APIServer == nil || *g.APIServer != ":7890" {
		t.Fatalf("gnmic = %+v", g)
	}
	if r.Nats.Output.NumWorkers != 2 || r.Nats.Output.WriteTimeout != "7s" {
		t.Fatalf("nats.output = %+v", r.Nats.Output)
	}
	if r.Nats.Input.FetchBatchSize != 250 || r.Nats.Input.DeliverPolicy != "new" {
		t.Fatalf("nats.input = %+v", r.Nats.Input)
	}
	if r.Prometheus.Interval != "1s" || r.Prometheus.MaxTimeSeriesPerWrite != 5000 {
		t.Fatalf("prometheus = %+v", r.Prometheus)
	}
}

const renderSkipVerify = valid + `
render:
  output_dir: ./render
  nats:
    address: nats:4222
  prometheus:
    remote_write_url: http://prom:9090/api/v1/write
  gnmic:
    skip_verify: true
`

func TestSkipVerifyAloneStillLoads(t *testing.T) {
	g := mustLoad(t, renderSkipVerify).Render.Gnmic
	if !g.SkipVerify || g.TLSCA != nil {
		t.Fatalf("gnmic = %+v", g)
	}
}

func TestSkipVerifyWithTLSCARejected(t *testing.T) {
	wantContains(t, loadErr(t, renderSkipVerify+"    tls_ca: /app/ca-chain.pem\n"),
		"invalid config", "render.gnmic", "skip_verify", "tls_ca", "secure escape hatch")
}

func TestRequireRenderRaisesWhenAbsent(t *testing.T) {
	c := &Config{Database: "db", Catalog: "cat"}
	_, err := c.RequireRender()
	if err == nil {
		t.Fatal("expected error")
	}
	var ce *ConfigError
	if !errors.As(err, &ce) {
		t.Fatalf("not a ConfigError: %v", err)
	}
	wantContains(t, err, "render needs a 'render' block", "$GNPCONFIG", "output_dir: ./render")
}

func TestRequireRenderReturnsBlock(t *testing.T) {
	c := mustLoad(t, render)
	r, err := c.RequireRender()
	if err != nil || r != c.Render {
		t.Fatalf("RequireRender = %v, %v", r, err)
	}
}

func TestFlagOverridesConfig(t *testing.T) {
	c := &Config{Database: "db", Catalog: "cat"}
	c.ApplyOverrides("", "/other")
	if c.Catalog != "/other" || c.Database != "db" {
		t.Fatalf("config = %+v", c)
	}
	c.ApplyOverrides("/state.db", "")
	if c.Database != "/state.db" || c.Catalog != "/other" {
		t.Fatalf("config = %+v", c)
	}
}

func TestLoggingHonorsDestinationAndLevel(t *testing.T) {
	logfile := filepath.Join(t.TempDir(), "out.log")
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	if err := SetupLogging(&Config{Log: LogConfig{Path: logfile, Level: "debug"}}); err != nil {
		t.Fatal(err)
	}
	slog.Debug("hello-debug")
	slog.Error("hello-error")

	data, err := os.ReadFile(logfile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "hello-debug") || !strings.Contains(string(data), "hello-error") {
		t.Fatalf("log = %q", data)
	}
}

func TestLoggingFiltersBelowLevel(t *testing.T) {
	logfile := filepath.Join(t.TempDir(), "out.log")
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	// An unknown level is not rejected at load; logging falls back to INFO like Python.
	if err := SetupLogging(&Config{Log: LogConfig{Path: logfile, Level: "chatty"}}); err != nil {
		t.Fatal(err)
	}
	slog.Debug("noisy-debug")
	slog.Warn("loud-warning")

	data, err := os.ReadFile(logfile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "noisy-debug") {
		t.Fatalf("debug leaked at info level: %q", data)
	}
	if !strings.Contains(string(data), "loud-warning") {
		t.Fatalf("warning missing: %q", data)
	}
}

func TestRenderMissingRequiredModelsCounted(t *testing.T) {
	err := loadErr(t, valid+"render:\n  output_dir: x\n")
	wantContains(t, err, "2 validation errors for Config", "\nrender.nats\n", "\nrender.prometheus\n", "Field required [type=missing]")
}

func TestNullModelRejected(t *testing.T) {
	wantContains(t, loadErr(t, "database: db\ncatalog: cat\nlog: null\n"), "log", "model_type")
}

func TestNullRequiredStringRejected(t *testing.T) {
	wantContains(t, loadErr(t, "database: null\ncatalog: cat\n"), "database", "string_type")
}

func TestFractionalIntRejected(t *testing.T) {
	cfg := valid + `
render:
  output_dir: x
  nats:
    address: nats:4222
    output:
      num_workers: 5.5
  prometheus:
    remote_write_url: u
`
	wantContains(t, loadErr(t, cfg), "render.nats.output.num_workers", "int_from_float")
}

func TestLoggingCriticalSuppressesErrorRecords(t *testing.T) {
	for _, level := range []string{"critical", "fatal"} {
		t.Run(level, func(t *testing.T) {
			logfile := filepath.Join(t.TempDir(), "out.log")
			prev := slog.Default()
			t.Cleanup(func() { slog.SetDefault(prev) })

			if err := SetupLogging(&Config{Log: LogConfig{Path: logfile, Level: level}}); err != nil {
				t.Fatal(err)
			}
			slog.Error("should-not-appear")

			data, err := os.ReadFile(logfile)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data), "should-not-appear") {
				t.Fatalf("%s level logged an error record: %q", level, data)
			}
		})
	}
}
