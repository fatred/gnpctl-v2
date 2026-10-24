// Package render turns discovered state plus the catalog into the desired
// gnmic data-plane configuration (pure compute; serialization lives in emit).
package render

import (
	"sort"

	"github.com/fatred/gnpctl-v2/internal/catalog"
	"github.com/fatred/gnpctl-v2/internal/config"
	"github.com/fatred/gnpctl-v2/internal/state"
)

const (
	SubjectFormat = "subscription.target"
	EventFormat   = "event" // gnmic message format on the wire through NATS
	// Credentials are NEVER literals — gnmic env-expands these at runtime (same
	// account as discover/test). Secrets via env only, never in a file.
	UsernameRef  = "${TARGET_USER}"
	PasswordRef  = "${TARGET_PASS}"
	InstanceProc = "add-instance" // shared tag prepended to every egress chain (promxy HA dedup)
	FederateProc = "add-federate" // shared tag appended to chains carrying a federated metric (LTS)
)

// Ordered is an insertion-ordered string-keyed mapping; YAML key order must
// match the Python dicts (sort_keys=False).
type Ordered struct {
	Keys   []string
	Values []any
}

// Set appends the key, or replaces its value in place when already present.
func (o *Ordered) Set(key string, value any) {
	for i, k := range o.Keys {
		if k == key {
			o.Values[i] = value
			return
		}
	}
	o.Keys = append(o.Keys, key)
	o.Values = append(o.Values, value)
}

// Get returns the value stored under key.
func (o *Ordered) Get(key string) (any, bool) {
	for i, k := range o.Keys {
		if k == key {
			return o.Values[i], true
		}
	}
	return nil, false
}

// StreamSub is one (mode, sample_interval) group of paths within a subscription.
type StreamSub struct {
	Paths          []string
	Mode           string
	SampleInterval *string
}

// Subscription is a gnmic subscription keyed <catalog_key>-<module>.
type Subscription struct {
	Name                string // <catalog_key>-<module>
	StreamSubscriptions []StreamSub
	Outputs             []string // routed to its per-module output
}

// JetstreamOutput is a gnmic jetstream output.
type JetstreamOutput struct {
	Name            string // <catalog_key>-<module>
	Stream          string // <catalog_key>-<module>
	Address         string
	CreateStream    Ordered
	SubjectFormat   string
	Format          string
	NumWorkers      int
	WriteTimeout    string
	ConnectTimeWait string
}

// IngressGlobals are the gnmic ingestor process-level settings (TLS, creds-ref,
// loader, api-server).
type IngressGlobals struct {
	SkipVerify  bool
	TargetsFile string
	TLSCA       *string
	APIServer   *string
	UsernameRef string
	PasswordRef string
}

// IngressConfig is the ingress side of the desired config.
type IngressConfig struct {
	Global        IngressGlobals
	Subscriptions []Subscription
	Outputs       []JetstreamOutput
}

// TargetEntry is one gnmic loader target.
type TargetEntry struct {
	Name          string
	Address       *string
	Port          *int
	Encoding      *string
	Subscriptions []string
	Tags          Ordered
}

// TargetsConfig is the targets.yaml document.
type TargetsConfig struct {
	Targets []TargetEntry
}

// EgressConsumer is one egress jetstream input.
type EgressConsumer struct {
	Name       string // <catalog_key>-<module>
	Stream     string // <catalog_key>-<module>
	Processors []string
}

// EgressInputTuning is the jetstream durable-consumer tuning, uniform across
// every egress input.
type EgressInputTuning struct {
	NumWorkers     int
	FetchBatchSize int
	MaxAckPending  int
	BufferSize     int
	DeliverPolicy  string
	Format         string
}

// PrometheusTuning is the prometheus_write output tuning.
type PrometheusTuning struct {
	Interval              string
	BufferSize            int
	MaxTimeSeriesPerWrite int
	Timeout               string
	NumWorkers            int
}

// EgressConfig is the egress side of the desired config.
type EgressConfig struct {
	NatsAddress              string
	PrometheusRemoteWriteURL string
	Consumers                []EgressConsumer
	Processors               Ordered // referenced name -> gnmic spec
	InputTuning              EgressInputTuning
	Prometheus               PrometheusTuning
}

// DesiredConfig is the full set of rendered artifacts.
type DesiredConfig struct {
	Ingress IngressConfig
	Targets TargetsConfig
	Egress  EgressConfig
}

func workqueueStream(maxAge string) Ordered {
	// gnmic upserts the stream via this block (openconfig/gnmic#767). Empty
	// subjects is correct — subject-format: subscription.target routes.
	o := Ordered{}
	o.Set("retention-policy", "workqueue")
	o.Set("storage", "memory")
	o.Set("subjects", []string{})
	o.Set("max-age", maxAge)
	return o
}

func instanceProcessor(value string) Ordered {
	// `instance` tag for promxy HA dedup across the per-DC collection stacks;
	// emitted verbatim (a literal or an ${ENV} ref each stack resolves).
	add := Ordered{}
	add.Set("instance", value)
	tag := Ordered{}
	tag.Set("overwrite", false)
	tag.Set("add", add)
	o := Ordered{}
	o.Set("event-add-tag", tag)
	return o
}

func federateProcessor(names []string) Ordered {
	// `federate=true` tag for the long-term store. value-names derives from the
	// per-metric `federate: true` flags; appended after the module chain, where
	// the event value name equals the canonical metric name.
	vns := make([]string, len(names))
	for i, n := range names {
		vns[i] = "^" + n + "$"
	}
	add := Ordered{}
	add.Set("federate", "true")
	tag := Ordered{}
	tag.Set("overwrite", false)
	tag.Set("value-names", vns)
	tag.Set("add", add)
	o := Ordered{}
	o.Set("event-add-tag", tag)
	return o
}

type pathGroup struct {
	mode     string
	interval string
	paths    []string
}

func groupPaths(paths []*catalog.SubscriptionObject) []StreamSub {
	index := map[[2]string]int{}
	var groups []pathGroup
	for _, p := range paths {
		interval := ""
		if p.SampleInterval != nil {
			interval = *p.SampleInterval
		}
		key := [2]string{p.Mode, interval}
		i, ok := index[key]
		if !ok {
			i = len(groups)
			index[key] = i
			groups = append(groups, pathGroup{mode: p.Mode, interval: interval})
		}
		groups[i].paths = append(groups[i].paths, p.Path)
	}
	sort.SliceStable(groups, func(i, j int) bool {
		if groups[i].mode != groups[j].mode {
			return groups[i].mode < groups[j].mode
		}
		return groups[i].interval < groups[j].interval
	})
	out := make([]StreamSub, 0, len(groups))
	for _, g := range groups {
		sort.Strings(g.paths)
		sub := StreamSub{Paths: g.paths, Mode: g.mode}
		if g.interval != "" {
			interval := g.interval
			sub.SampleInterval = &interval
		}
		out = append(out, sub)
	}
	return out
}

type slice struct {
	paths      []*catalog.SubscriptionObject
	pathIndex  map[string]bool
	processors []string
	federate   map[string]bool
}

// collectSlices walks discovered state into per-(variant, module) slices and
// target entries. Paths are unioned across targets of the same variant; the
// last target's variant-level processors win.
func collectSlices(
	st *state.State, cat *catalog.LayeredCatalog, renderCfg *config.RenderConfig,
) (map[string]*slice, []TargetEntry, error) {
	moduleNames := cat.Modules().Names()
	slices := map[string]*slice{}
	var targets []TargetEntry

	rows, err := st.AllTargets()
	if err != nil {
		return nil, nil, err
	}
	for _, row := range rows {
		if !discovered(row) {
			continue
		}
		key := stringValue(row["catalog_key"])
		if key == "" {
			continue
		}
		variant := cat.Variant(key)
		if variant == nil {
			continue // not ready: vendor-known-platform-unknown
		}
		osVersion := optionalString(row["os_version"])
		profile, err := cat.EffectiveProfile(key, osVersion)
		if err != nil {
			return nil, nil, err
		}
		name := stringValue(row["name"])
		enabled, err := st.EnabledModules(name, moduleNames)
		if err != nil {
			return nil, nil, err
		}
		enabledSet := make(map[string]bool, len(enabled))
		for _, m := range enabled {
			enabledSet[m] = true
		}

		var targetSubs []string
		for _, moduleName := range profile.Modules.Names() {
			resolved, _ := profile.Modules.Get(moduleName)
			if !enabledSet[moduleName] || len(resolved.Paths) == 0 {
				continue
			}
			subName := key + "-" + moduleName
			targetSubs = append(targetSubs, subName)
			slot, ok := slices[subName]
			if !ok {
				slot = &slice{
					pathIndex:  map[string]bool{},
					processors: append([]string{}, resolved.Processors...),
					federate:   map[string]bool{},
				}
				slices[subName] = slot
			}
			for _, p := range resolved.Paths {
				pk := p.PathKey()
				if slot.pathIndex[pk] {
					continue
				}
				slot.pathIndex[pk] = true
				slot.paths = append(slot.paths, p)
			}
			slot.processors = append([]string{}, resolved.Processors...)
			for _, metric := range resolved.Metrics {
				if metric.Federate {
					slot.federate[metric.Name] = true
				}
			}
		}

		// Name the target by its FQDN so gnmic's `source` tag is the fleet-wide
		// identity; the connection address keeps the discovered IP.
		targetName := name
		if domain := renderCfg.TargetDeviceDomain; domain != nil && *domain != "" {
			targetName = name + "." + *domain
		}
		sort.Strings(targetSubs)
		tags := Ordered{}
		tags.Set("vendor", row["vendor"])
		tags.Set("model", row["model"])
		tags.Set("variant", key)
		targets = append(targets, TargetEntry{
			Name:          targetName,
			Address:       optionalString(row["address"]),
			Port:          optionalInt(row["port"]),
			Encoding:      variant.Encoding,
			Subscriptions: targetSubs,
			Tags:          tags,
		})
	}
	return slices, targets, nil
}

func buildPipeline(
	slices map[string]*slice, renderCfg *config.RenderConfig,
) ([]Subscription, []JetstreamOutput, []EgressConsumer) {
	tuning := renderCfg.Nats.Output
	names := make([]string, 0, len(slices))
	for name := range slices {
		names = append(names, name)
	}
	sort.Strings(names)

	subscriptions := make([]Subscription, 0, len(names))
	outputs := make([]JetstreamOutput, 0, len(names))
	consumers := make([]EgressConsumer, 0, len(names))
	for _, name := range names {
		slot := slices[name]
		subscriptions = append(subscriptions, Subscription{
			Name:                name,
			StreamSubscriptions: groupPaths(slot.paths),
			Outputs:             []string{name},
		})
		outputs = append(outputs, JetstreamOutput{
			Name:            name,
			Stream:          name,
			Address:         renderCfg.Nats.Address,
			CreateStream:    workqueueStream(tuning.MaxAge),
			SubjectFormat:   SubjectFormat,
			Format:          EventFormat,
			NumWorkers:      tuning.NumWorkers,
			WriteTimeout:    tuning.WriteTimeout,
			ConnectTimeWait: tuning.ConnectTimeWait,
		})
		consumers = append(consumers, EgressConsumer{
			Name:       name,
			Stream:     name,
			Processors: append([]string{}, slot.processors...),
		})
	}
	return subscriptions, outputs, consumers
}

// attachSharedProcessors resolves referenced processor specs and splices in the
// two shared chains, mutating each consumer's processors in place.
func attachSharedProcessors(
	consumers []EgressConsumer, slices map[string]*slice,
	cat *catalog.LayeredCatalog, renderCfg *config.RenderConfig,
) Ordered {
	seen := map[string]bool{}
	var referenced []string
	for _, c := range consumers {
		for _, n := range c.Processors {
			if !seen[n] {
				seen[n] = true
				referenced = append(referenced, n)
			}
		}
	}
	sort.Strings(referenced)
	specs := Ordered{}
	for _, n := range referenced {
		specs.Set(n, cat.Processors().Get(n))
	}

	// Prepend the shared instance tag to every chain when configured: every
	// series needs `instance` so promxy can dedupe the scatter-gather.
	if instance := renderCfg.Gnmic.Instance; instance != nil && *instance != "" {
		for i := range consumers {
			consumers[i].Processors = append([]string{InstanceProc}, consumers[i].Processors...)
		}
		specs.Set(InstanceProc, instanceProcessor(*instance))
	}

	// Append the shared federate tag to every chain carrying a federated metric.
	federatedSet := map[string]bool{}
	for _, slot := range slices {
		for n := range slot.federate {
			federatedSet[n] = true
		}
	}
	federated := make([]string, 0, len(federatedSet))
	for n := range federatedSet {
		federated = append(federated, n)
	}
	sort.Strings(federated)
	if len(federated) > 0 {
		for i := range consumers {
			if len(slices[consumers[i].Name].federate) > 0 {
				consumers[i].Processors = append(consumers[i].Processors, FederateProc)
			}
		}
		specs.Set(FederateProc, federateProcessor(federated))
	}
	return specs
}

// Compute builds the desired data-plane config from state + catalog (pure).
func Compute(
	st *state.State, cat *catalog.LayeredCatalog, renderCfg *config.RenderConfig,
) (*DesiredConfig, error) {
	slices, targets, err := collectSlices(st, cat, renderCfg)
	if err != nil {
		return nil, err
	}
	subscriptions, outputs, consumers := buildPipeline(slices, renderCfg)
	specs := attachSharedProcessors(consumers, slices, cat, renderCfg)

	sort.SliceStable(targets, func(i, j int) bool { return targets[i].Name < targets[j].Name })

	g := renderCfg.Gnmic
	in := renderCfg.Nats.Input
	p := renderCfg.Prometheus
	return &DesiredConfig{
		Ingress: IngressConfig{
			Global: IngressGlobals{
				SkipVerify:  g.SkipVerify,
				TargetsFile: g.TargetsFile,
				TLSCA:       g.TLSCA,
				APIServer:   g.APIServer,
				UsernameRef: UsernameRef,
				PasswordRef: PasswordRef,
			},
			Subscriptions: subscriptions,
			Outputs:       outputs,
		},
		Targets: TargetsConfig{Targets: targets},
		Egress: EgressConfig{
			NatsAddress:              renderCfg.Nats.Address,
			PrometheusRemoteWriteURL: p.RemoteWriteURL,
			Consumers:                consumers,
			Processors:               specs,
			InputTuning: EgressInputTuning{
				NumWorkers:     in.NumWorkers,
				FetchBatchSize: in.FetchBatchSize,
				MaxAckPending:  in.MaxAckPending,
				BufferSize:     in.BufferSize,
				DeliverPolicy:  in.DeliverPolicy,
				Format:         EventFormat,
			},
			Prometheus: PrometheusTuning{
				Interval:              p.Interval,
				BufferSize:            p.BufferSize,
				MaxTimeSeriesPerWrite: p.MaxTimeSeriesPerWrite,
				Timeout:               p.Timeout,
				NumWorkers:            p.NumWorkers,
			},
		},
	}, nil
}

func discovered(row map[string]any) bool {
	switch v := row["discovered"].(type) {
	case int64:
		return v != 0
	case int:
		return v != 0
	case bool:
		return v
	}
	return false
}

// stringValue mirrors Python's row.get(key) or "", which also flattens NULL.
func stringValue(v any) string {
	s, _ := v.(string)
	return s
}

func optionalString(v any) *string {
	s, ok := v.(string)
	if !ok {
		return nil
	}
	return &s
}

func optionalInt(v any) *int {
	if v == nil {
		return nil
	}
	var n int
	switch t := v.(type) {
	case int64:
		n = int(t)
	case int:
		n = t
	default:
		return nil
	}
	return &n
}
