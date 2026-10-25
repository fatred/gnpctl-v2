package render_test

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/fatred/gnpctl-v2/internal/catalog"
	"github.com/fatred/gnpctl-v2/internal/config"
	"github.com/fatred/gnpctl-v2/internal/emit"
	"github.com/fatred/gnpctl-v2/internal/golden"
	"github.com/fatred/gnpctl-v2/internal/render"
	"github.com/fatred/gnpctl-v2/internal/state"
	"gopkg.in/yaml.v3"
)

const miniModules = "modules:\n  ports: on\n  bgp-peers: off\n"
const miniVendor = "vendor: vx\nfingerprint:\n  priority: 100\n  match:\n    org_contains: [X]\n"
const miniProcessors = "add-device-labels:\n  event-add-tag:\n    add:\n      source: gnpctl\n"
const miniCommon = `key: vx-v1
vendor: vx
encoding: proto
modules:
  ports:
    processors: [add-device-labels]
    paths:
      - {key: a, path: /a, mode: sample, sample_interval: 15s}
      - {key: b, path: /b, mode: sample, sample_interval: 15s}
      - {key: c, path: /c, mode: sample, sample_interval: 30s}
  bgp-peers:
    paths:
      - {key: bgp, path: /bgp, mode: sample, sample_interval: 30s}
`

const fedModules = "modules:\n  alpha: on\n  beta: on\n  gamma: on\n"
const fedProcessors = "pass:\n  event-add-tag:\n    add:\n      x: y\n"
const fedVendor = "vendor: fx\nfingerprint:\n  priority: 100\n  match:\n    org_contains: [F]\n"
const fedCommon = `key: fx-v1
vendor: fx
encoding: proto
modules:
  alpha:
    processors: [pass]
    metrics:
      m1: {type: info, federate: true}
      m2: {type: info}
    paths:
      - {key: a, path: /a, mode: sample, sample_interval: 15s}
  beta:
    processors: [pass]
    metrics:
      m3: {type: info}
    paths:
      - {key: b, path: /b, mode: sample, sample_interval: 15s}
  gamma:
    paths:
      - {key: g, path: /g, mode: sample, sample_interval: 15s}
`

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, text := range files {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func loadCatalog(t *testing.T, files map[string]string) *catalog.LayeredCatalog {
	t.Helper()
	root := t.TempDir()
	writeTree(t, root, files)
	cat, err := catalog.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

func miniCatalog(t *testing.T) *catalog.LayeredCatalog {
	return loadCatalog(t, map[string]string{
		"modules.yaml":           miniModules,
		"processors/labels.yaml": miniProcessors,
		"vx/vendor.yaml":         miniVendor,
		"vx/v1/common.yaml":      miniCommon,
	})
}

func federateCatalog(t *testing.T) *catalog.LayeredCatalog {
	return loadCatalog(t, map[string]string{
		"modules.yaml":      fedModules,
		"processors/p.yaml": fedProcessors,
		"fx/vendor.yaml":    fedVendor,
		"fx/v1/common.yaml": fedCommon,
	})
}

var (
	realCatalogOnce sync.Once
	realCatalogVal  *catalog.LayeredCatalog
	realCatalogErr  error
)

func realCatalog(t *testing.T) *catalog.LayeredCatalog {
	t.Helper()
	realCatalogOnce.Do(func() {
		realCatalogVal, realCatalogErr = catalog.Load(filepath.Join("..", "..", "catalog"))
	})
	if realCatalogErr != nil {
		t.Fatal(realCatalogErr)
	}
	return realCatalogVal
}

func newState(t *testing.T) *state.State {
	t.Helper()
	st, err := state.Open(filepath.Join(t.TempDir(), "s.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func addTarget(t *testing.T, st *state.State, name, address string, port int, key, vendor, model, osVersion string) {
	t.Helper()
	if err := st.UpsertTarget(name, new(address), &port, new(""), ""); err != nil {
		t.Fatal(err)
	}
	if err := st.EnrichTarget(name, state.Enrichment{
		Discovered: true, LastDiscoveryRun: 1, CatalogKey: key,
		Vendor: vendor, Model: model, OSVersion: osVersion,
	}); err != nil {
		t.Fatal(err)
	}
}

func enable(t *testing.T, st *state.State, target string, values map[string]int) {
	t.Helper()
	for name, value := range values {
		if err := st.SetModule(target, name, value); err != nil {
			t.Fatal(err)
		}
	}
}

// renderConfig writes a minimal config and returns its parsed render block, so
// tests exercise the same defaults the CLI gets.
func renderConfig(t *testing.T, renderBlock string) *config.RenderConfig {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "gnpctl.yaml")
	text := "database: state.db\ncatalog: catalog\nrender:\n" + renderBlock
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Render
}

func defaultRenderConfig(t *testing.T) *config.RenderConfig {
	return renderConfig(t, `  output_dir: ./out
  nats:
    address: nats:4222
  prometheus:
    remote_write_url: http://prom:9090/api/v1/write
`)
}

func fullRenderConfig(t *testing.T) *config.RenderConfig {
	return renderConfig(t, `  output_dir: ./out
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
    api_server: ":7890"
`)
}

func identityRenderConfig(t *testing.T) *config.RenderConfig {
	return renderConfig(t, `  output_dir: ./out
  target_device_domain: net.plabs.ch
  nats:
    address: nats:4222
  prometheus:
    remote_write_url: http://prom:9090/api/v1/write
  gnmic:
    instance: ${GNMIC_INSTANCE}
`)
}

func skipVerifyRenderConfig(t *testing.T) *config.RenderConfig {
	return renderConfig(t, `  output_dir: ./out
  nats:
    address: nats:4222
  prometheus:
    remote_write_url: http://prom:9090/api/v1/write
  gnmic:
    skip_verify: true
`)
}

func compute(t *testing.T, st *state.State, cat *catalog.LayeredCatalog, cfg *config.RenderConfig) *render.DesiredConfig {
	t.Helper()
	desired, err := render.Compute(st, cat, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return desired
}

func subscriptionsByName(items []render.Subscription) map[string]render.Subscription {
	out := map[string]render.Subscription{}
	for _, item := range items {
		out[item.Name] = item
	}
	return out
}

func outputsByName(items []render.JetstreamOutput) map[string]render.JetstreamOutput {
	out := map[string]render.JetstreamOutput{}
	for _, item := range items {
		out[item.Name] = item
	}
	return out
}

func consumersByName(items []render.EgressConsumer) map[string]render.EgressConsumer {
	out := map[string]render.EgressConsumer{}
	for _, item := range items {
		out[item.Name] = item
	}
	return out
}

func targetsByName(items []render.TargetEntry) map[string]render.TargetEntry {
	out := map[string]render.TargetEntry{}
	for _, item := range items {
		out[item.Name] = item
	}
	return out
}

func miniState(t *testing.T) *state.State {
	st := newState(t)
	addTarget(t, st, "leaf1", "10.0.0.1", 57400, "vx-v1", "vx", "M1", "")
	if err := st.EnsureModuleColumns([]string{"ports", "bgp-peers"}); err != nil {
		t.Fatal(err)
	}
	enable(t, st, "leaf1", map[string]int{"ports": 1, "bgp-peers": -1})
	return st
}

func TestSubscriptionKeyedAndGroupedByInterval(t *testing.T) {
	desired := compute(t, miniState(t), miniCatalog(t), defaultRenderConfig(t))
	subs := subscriptionsByName(desired.Ingress.Subscriptions)
	ports, ok := subs["vx-v1-ports"]
	if !ok {
		t.Fatalf("missing vx-v1-ports subscription: %v", subs)
	}
	if _, ok := subs["vx-v1-bgp-peers"]; ok {
		t.Fatal("disabled module rendered")
	}
	byInterval := map[string][]string{}
	for _, group := range ports.StreamSubscriptions {
		if group.SampleInterval == nil {
			t.Fatalf("missing interval in %v", group)
		}
		byInterval[*group.SampleInterval] = group.Paths
	}
	if len(byInterval["15s"]) != 2 || byInterval["15s"][0] != "/a" || byInterval["15s"][1] != "/b" {
		t.Fatalf("15s group = %v", byInterval["15s"])
	}
	if len(byInterval["30s"]) != 1 || byInterval["30s"][0] != "/c" {
		t.Fatalf("30s group = %v", byInterval["30s"])
	}
}

func TestIngressOutputAndRouting(t *testing.T) {
	desired := compute(t, miniState(t), miniCatalog(t), defaultRenderConfig(t))
	out, ok := outputsByName(desired.Ingress.Outputs)["vx-v1-ports"]
	if !ok {
		t.Fatal("missing output")
	}
	if out.Stream != "vx-v1-ports" || out.Address != "nats:4222" ||
		out.SubjectFormat != "subscription.target" {
		t.Fatalf("output = %+v", out)
	}
	if policy, _ := out.CreateStream.Get("retention-policy"); policy != "workqueue" {
		t.Fatalf("retention-policy = %v", policy)
	}
	sub := subscriptionsByName(desired.Ingress.Subscriptions)["vx-v1-ports"]
	if len(sub.Outputs) != 1 || sub.Outputs[0] != "vx-v1-ports" {
		t.Fatalf("outputs = %v", sub.Outputs)
	}
}

func TestTargetsEntry(t *testing.T) {
	desired := compute(t, miniState(t), miniCatalog(t), defaultRenderConfig(t))
	target, ok := targetsByName(desired.Targets.Targets)["leaf1"]
	if !ok {
		t.Fatal("missing leaf1")
	}
	if target.Address == nil || *target.Address != "10.0.0.1" ||
		target.Port == nil || *target.Port != 57400 {
		t.Fatalf("address/port = %v/%v", target.Address, target.Port)
	}
	if target.Encoding == nil || *target.Encoding != "proto" {
		t.Fatalf("encoding = %v", target.Encoding)
	}
	if len(target.Subscriptions) != 1 || target.Subscriptions[0] != "vx-v1-ports" {
		t.Fatalf("subscriptions = %v", target.Subscriptions)
	}
	for key, want := range map[string]any{"vendor": "vx", "variant": "vx-v1", "model": "M1"} {
		if got, _ := target.Tags.Get(key); got != want {
			t.Fatalf("tag %s = %v, want %v", key, got, want)
		}
	}
}

func TestEgressConsumer(t *testing.T) {
	desired := compute(t, miniState(t), miniCatalog(t), defaultRenderConfig(t))
	consumer, ok := consumersByName(desired.Egress.Consumers)["vx-v1-ports"]
	if !ok {
		t.Fatal("missing consumer")
	}
	if consumer.Stream != "vx-v1-ports" {
		t.Fatalf("stream = %s", consumer.Stream)
	}
	if len(consumer.Processors) != 1 || consumer.Processors[0] != "add-device-labels" {
		t.Fatalf("processors = %v", consumer.Processors)
	}
	if desired.Egress.NatsAddress != "nats:4222" ||
		desired.Egress.PrometheusRemoteWriteURL != "http://prom:9090/api/v1/write" {
		t.Fatalf("egress = %+v", desired.Egress)
	}
}

func TestIngressGlobalsAndOutputTuning(t *testing.T) {
	desired := compute(t, miniState(t), miniCatalog(t), fullRenderConfig(t))
	global := desired.Ingress.Global
	if global.SkipVerify {
		t.Fatal("skip-verify true")
	}
	if global.TLSCA == nil || *global.TLSCA != "/app/ca-chain.pem" ||
		global.TargetsFile != "/app/targets.yaml" ||
		global.APIServer == nil || *global.APIServer != ":7890" {
		t.Fatalf("globals = %+v", global)
	}
	if global.UsernameRef != "${TARGET_USER}" || global.PasswordRef != "${TARGET_PASS}" {
		t.Fatalf("creds refs = %q/%q", global.UsernameRef, global.PasswordRef)
	}
	out := outputsByName(desired.Ingress.Outputs)["vx-v1-ports"]
	if out.NumWorkers != 2 || out.WriteTimeout != "7s" || out.ConnectTimeWait != "3s" {
		t.Fatalf("tuning = %+v", out)
	}
	if maxAge, _ := out.CreateStream.Get("max-age"); maxAge != "2m" {
		t.Fatalf("max-age = %v", maxAge)
	}
	subjects, _ := out.CreateStream.Get("subjects")
	if list, ok := subjects.([]string); !ok || len(list) != 0 {
		t.Fatalf("subjects = %v", subjects)
	}
}

func TestIngressGlobalsDefaultToSafeMinimal(t *testing.T) {
	desired := compute(t, miniState(t), miniCatalog(t), defaultRenderConfig(t))
	global := desired.Ingress.Global
	if global.SkipVerify || global.TLSCA != nil || global.APIServer != nil {
		t.Fatalf("globals = %+v", global)
	}
	if global.TargetsFile != "targets.yaml" {
		t.Fatalf("targets file = %q", global.TargetsFile)
	}
	out := outputsByName(desired.Ingress.Outputs)["vx-v1-ports"]
	if out.NumWorkers != 1 || out.WriteTimeout != "5s" || out.ConnectTimeWait != "2s" {
		t.Fatalf("tuning = %+v", out)
	}
	if maxAge, _ := out.CreateStream.Get("max-age"); maxAge != "1m" {
		t.Fatalf("max-age = %v", maxAge)
	}
}

func TestEgressInputAndPrometheusTuning(t *testing.T) {
	desired := compute(t, miniState(t), miniCatalog(t), fullRenderConfig(t))
	tuning := desired.Egress.InputTuning
	if tuning.Format != "event" || tuning.DeliverPolicy != "new" || tuning.NumWorkers != 20 ||
		tuning.FetchBatchSize != 250 || tuning.MaxAckPending != 50000 || tuning.BufferSize != 5000 {
		t.Fatalf("input tuning = %+v", tuning)
	}
	prom := desired.Egress.Prometheus
	if prom.Interval != "1s" || prom.BufferSize != 200000 || prom.MaxTimeSeriesPerWrite != 5000 ||
		prom.Timeout != "15s" || prom.NumWorkers != 4 {
		t.Fatalf("prometheus = %+v", prom)
	}
}

func TestEgressTuningDefaultsToProductionReference(t *testing.T) {
	desired := compute(t, miniState(t), miniCatalog(t), defaultRenderConfig(t))
	tuning := desired.Egress.InputTuning
	if tuning.NumWorkers != 10 || tuning.FetchBatchSize != 500 || tuning.MaxAckPending != 100000 ||
		tuning.BufferSize != 10000 || tuning.DeliverPolicy != "all" {
		t.Fatalf("input tuning = %+v", tuning)
	}
	prom := desired.Egress.Prometheus
	if prom.Interval != "2s" || prom.BufferSize != 100000 || prom.MaxTimeSeriesPerWrite != 10000 ||
		prom.Timeout != "10s" || prom.NumWorkers != 10 {
		t.Fatalf("prometheus = %+v", prom)
	}
}

func TestTargetNamedByFQDNAndAddressKeepsIP(t *testing.T) {
	desired := compute(t, miniState(t), miniCatalog(t), identityRenderConfig(t))
	target, ok := targetsByName(desired.Targets.Targets)["leaf1.net.plabs.ch"]
	if !ok {
		t.Fatalf("targets = %v", desired.Targets.Targets)
	}
	if target.Address == nil || *target.Address != "10.0.0.1" ||
		target.Port == nil || *target.Port != 57400 {
		t.Fatalf("address = %v port = %v", target.Address, target.Port)
	}
	if _, err := emit.Emit(t.TempDir(), desired); err != nil {
		t.Fatal(err)
	}
}

func TestNoDomainKeepsBareDeviceName(t *testing.T) {
	desired := compute(t, miniState(t), miniCatalog(t), defaultRenderConfig(t))
	if len(desired.Targets.Targets) != 1 || desired.Targets.Targets[0].Name != "leaf1" {
		t.Fatalf("targets = %v", desired.Targets.Targets)
	}
}

func TestInstanceProcessorPrependedToEveryChain(t *testing.T) {
	desired := compute(t, miniState(t), miniCatalog(t), identityRenderConfig(t))
	for _, consumer := range desired.Egress.Consumers {
		if len(consumer.Processors) == 0 || consumer.Processors[0] != "add-instance" {
			t.Fatalf("chain = %v", consumer.Processors)
		}
	}
	spec, ok := desired.Egress.Processors.Get("add-instance")
	if !ok {
		t.Fatal("missing add-instance spec")
	}
	tag := subMap(t, specMap(t, spec), "event-add-tag")
	if tag["overwrite"] != false {
		t.Fatalf("overwrite = %v", tag["overwrite"])
	}
	if add := subMap(t, tag, "add"); add["instance"] != "${GNMIC_INSTANCE}" {
		t.Fatalf("add = %v", add)
	}
}

func TestNoInstanceProcessorWhenUnset(t *testing.T) {
	desired := compute(t, miniState(t), miniCatalog(t), defaultRenderConfig(t))
	if _, ok := desired.Egress.Processors.Get("add-instance"); ok {
		t.Fatal("add-instance present")
	}
	for _, consumer := range desired.Egress.Consumers {
		for _, proc := range consumer.Processors {
			if proc == "add-instance" {
				t.Fatal("add-instance in chain")
			}
		}
	}
}

func TestSkipVerifyRenderWarnsAndBanners(t *testing.T) {
	desired := compute(t, miniState(t), miniCatalog(t), skipVerifyRenderConfig(t))
	dir := t.TempDir()
	stderr := captureStderr(t, func() {
		if _, err := emit.Emit(dir, desired); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(stderr, "skip-verify") || !strings.Contains(stderr, "ingress.yaml") {
		t.Fatalf("stderr = %q", stderr)
	}
	text, err := os.ReadFile(filepath.Join(dir, "ingress.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	trimmed := strings.TrimLeft(string(text), " \n")
	if !strings.HasPrefix(trimmed, "#") || !strings.Contains(string(text), "DISABLED") {
		t.Fatalf("ingress = %q", text)
	}
}

func TestSecureRenderEmitsNoTLSWarning(t *testing.T) {
	desired := compute(t, miniState(t), miniCatalog(t), defaultRenderConfig(t))
	dir := t.TempDir()
	stderr := captureStderr(t, func() {
		if _, err := emit.Emit(dir, desired); err != nil {
			t.Fatal(err)
		}
	})
	if strings.Contains(stderr, "skip-verify") {
		t.Fatalf("stderr = %q", stderr)
	}
	text, err := os.ReadFile(filepath.Join(dir, "ingress.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(text), "DISABLED") {
		t.Fatal("banner present")
	}
}

func TestUndiscoveredAndKeylessTargetsSkipped(t *testing.T) {
	st := miniState(t)
	if err := st.UpsertTarget("undisc", new("10.0.0.2"), nil, new(""), ""); err != nil {
		t.Fatal(err)
	}
	desired := compute(t, st, miniCatalog(t), defaultRenderConfig(t))
	if len(desired.Targets.Targets) != 1 || desired.Targets.Targets[0].Name != "leaf1" {
		t.Fatalf("targets = %v", desired.Targets.Targets)
	}
}

func TestPortsSliceGoldenRenderRealCatalog(t *testing.T) {
	cat := realCatalog(t)
	st := newState(t)
	addTarget(t, st, "leaf1", "10.0.0.1", 57400, "nokia-srlinux", "nokia", "7220 IXR-D2L", "v24.10.4")
	if err := st.EnsureModuleColumns(cat.Modules().Names()); err != nil {
		t.Fatal(err)
	}
	enable(t, st, "leaf1", map[string]int{"ports": 1})

	desired := compute(t, st, cat, defaultRenderConfig(t))
	sub := subscriptionsByName(desired.Ingress.Subscriptions)["nokia-srlinux-ports"]
	if len(sub.StreamSubscriptions) != 1 || sub.StreamSubscriptions[0].SampleInterval == nil ||
		*sub.StreamSubscriptions[0].SampleInterval != "15s" {
		t.Fatalf("groups = %+v", sub.StreamSubscriptions)
	}
	if !containsString(sub.StreamSubscriptions[0].Paths, "/interface[name=*]/statistics") {
		t.Fatalf("paths = %v", sub.StreamSubscriptions[0].Paths)
	}
	out := outputsByName(desired.Ingress.Outputs)["nokia-srlinux-ports"]
	if policy, _ := out.CreateStream.Get("retention-policy"); policy != "workqueue" {
		t.Fatalf("retention-policy = %v", policy)
	}
	consumer := consumersByName(desired.Egress.Consumers)["nokia-srlinux-ports"]
	if len(consumer.Processors) != 1 || consumer.Processors[0] != "gnpctl-meta" {
		t.Fatalf("chain = %v", consumer.Processors)
	}
	if _, ok := desired.Egress.Processors.Get("gnpctl-meta"); !ok {
		t.Fatal("gnpctl-meta spec missing")
	}
	dir := t.TempDir()
	if _, err := emit.Emit(dir, desired); err != nil {
		t.Fatal(err)
	}
	ingress := readYAML(t, filepath.Join(dir, "ingress.yaml"))
	subs := subMap(t, ingress, "subscriptions")
	ports := subMap(t, subs, "nokia-srlinux-ports")
	if outputs := ports["outputs"].([]any); len(outputs) != 1 || outputs[0] != "nokia-srlinux-ports" {
		t.Fatalf("outputs = %v", ports["outputs"])
	}
	if ingress["skip-verify"] != false || ingress["username"] != "${TARGET_USER}" ||
		ingress["password"] != "${TARGET_PASS}" {
		t.Fatalf("globals = %v", ingress)
	}
	loader := subMap(t, ingress, "loader")
	if loader["type"] != "file" || loader["path"] != "targets.yaml" {
		t.Fatalf("loader = %v", loader)
	}
	iout := subMap(t, subMap(t, ingress, "outputs"), "nokia-srlinux-ports")
	if iout["format"] != "event" || iout["subject-format"] != "subscription.target" {
		t.Fatalf("output = %v", iout)
	}
	stream := subMap(t, iout, "create-stream")
	if stream["max-age"] != "1m" {
		t.Fatalf("create-stream = %v", stream)
	}
	if subjects := stream["subjects"].([]any); len(subjects) != 0 {
		t.Fatalf("subjects = %v", subjects)
	}
	targets := readYAML(t, filepath.Join(dir, "targets.yaml"))
	leaf := subMap(t, targets, "leaf1")
	if tags := subMap(t, leaf, "tags"); tags["variant"] != "nokia-srlinux" {
		t.Fatalf("tags = %v", tags)
	}
	egress := readYAML(t, filepath.Join(dir, "egress.yaml"))
	einp := subMap(t, subMap(t, egress, "inputs"), "nokia-srlinux-ports")
	if processors := einp["event-processors"].([]any); len(processors) != 1 || processors[0] != "gnpctl-meta" {
		t.Fatalf("event-processors = %v", einp["event-processors"])
	}
	if einp["format"] != "event" || einp["deliver-policy"] != "all" || einp["fetch-batch-size"] != 500 {
		t.Fatalf("input = %v", einp)
	}
	prom := subMap(t, subMap(t, egress, "outputs"), "prometheus")
	if prom["type"] != "prometheus_write" || prom["interval"] != "2s" {
		t.Fatalf("prometheus = %v", prom)
	}
	meta := subMap(t, subMap(t, subMap(t, egress, "processors"), "gnpctl-meta"), "event-add-tag")
	if add := subMap(t, meta, "add"); add["pipeline"] != "gnpctl" {
		t.Fatalf("meta = %v", meta)
	}
}

func TestSharedChainsRealCatalog(t *testing.T) {
	cases := []struct {
		module     string
		value      int
		chain      []string
		convertFor string
		allowName  string
	}{
		{"processors", 1, []string{"cpu-rename", "cpu-convert", "cpu-keep", "add-federate"},
			"cpu-convert", "^cpu_utilization$"},
		{"mempools", 1, []string{"memory-rename", "memory-convert", "memory-keep", "add-federate"},
			"memory-convert", "^memory_total_bytes$"},
		{"transceivers", 1, []string{"transceiver-rename", "transceiver-convert", "transceiver-keep", "add-federate"},
			"transceiver-convert", "^transceiver_input_power_dbm$"},
	}
	cat := realCatalog(t)
	for _, tc := range cases {
		t.Run(tc.module, func(t *testing.T) {
			for _, key := range []string{"nokia-srlinux", "juniper-mx", "juniper-qfx"} {
				st := newState(t)
				addTarget(t, st, "r1", "10.0.0.1", 57400, key, "x", "", "")
				if err := st.EnsureModuleColumns(cat.Modules().Names()); err != nil {
					t.Fatal(err)
				}
				enable(t, st, "r1", map[string]int{tc.module: tc.value})
				desired := compute(t, st, cat, defaultRenderConfig(t))
				consumer, ok := consumersByName(desired.Egress.Consumers)[key+"-"+tc.module]
				if !ok {
					t.Fatalf("missing %s-%s", key, tc.module)
				}
				if strings.Join(consumer.Processors, ",") != strings.Join(tc.chain, ",") {
					t.Fatalf("%s chain = %v, want %v", key, consumer.Processors, tc.chain)
				}
				spec, _ := desired.Egress.Processors.Get(tc.convertFor)
				convert := subMap(t, specMap(t, spec), "event-convert")
				if convert["type"] != "float" {
					t.Fatalf("%s convert = %v", tc.convertFor, convert)
				}
			}
		})
	}
}

func TestSensorsAndStorageAndISISChains(t *testing.T) {
	cat := realCatalog(t)
	cases := []struct {
		module string
		value  int
		keys   []string
		chain  []string
		allow  string
	}{
		{"sensors", 1, []string{"nokia-srlinux", "juniper-mx", "juniper-qfx"},
			[]string{"sensors-state", "sensors-keep", "add-federate"}, "^power_supply_state$"},
		{"storage", 1, []string{"nokia-srlinux", "juniper-qfx"},
			[]string{"storage-rename", "storage-convert", "storage-keep", "add-federate"}, "^storage_total_bytes$"},
		{"isis", 2, []string{"nokia-srlinux", "juniper-mx"},
			[]string{"isis-fsm", "isis-fsm-convert", "isis-rename", "isis-keep-state", "add-federate"},
			"^isis_adjacency_state$"},
	}
	for _, tc := range cases {
		t.Run(tc.module, func(t *testing.T) {
			for _, key := range tc.keys {
				st := newState(t)
				addTarget(t, st, "r1", "10.0.0.1", 57400, key, "x", "", "")
				if err := st.EnsureModuleColumns(cat.Modules().Names()); err != nil {
					t.Fatal(err)
				}
				enable(t, st, "r1", map[string]int{tc.module: tc.value})
				desired := compute(t, st, cat, defaultRenderConfig(t))
				consumer, ok := consumersByName(desired.Egress.Consumers)[key+"-"+tc.module]
				if !ok {
					t.Fatalf("missing %s-%s", key, tc.module)
				}
				if strings.Join(consumer.Processors, ",") != strings.Join(tc.chain, ",") {
					t.Fatalf("%s chain = %v, want %v", key, consumer.Processors, tc.chain)
				}
				keepName := tc.chain[len(tc.chain)-2]
				spec, _ := desired.Egress.Processors.Get(keepName)
				keep := subMap(t, specMap(t, spec), "event-allow")
				names := anyStrings(keep["value-names"])
				if !containsString(names, tc.allow) {
					t.Fatalf("%s value-names = %v, want %s", keepName, names, tc.allow)
				}
			}
		})
	}
}

func TestBGPSliceSharedChain(t *testing.T) {
	cat := realCatalog(t)
	chain := []string{"bgp-info", "bgp-fsm", "bgp-fsm-convert", "bgp-rename", "bgp-keep-state", "add-federate"}
	wantNames := []string{"^bgp_neighbor_session_state$", "^bgp_neighbor_peer_group$", "^bgp_neighbor_description$"}
	for _, key := range []string{"nokia-srlinux", "juniper-mx", "juniper-qfx"} {
		st := newState(t)
		addTarget(t, st, "r1", "10.0.0.1", 57400, key, "x", "", "")
		if err := st.EnsureModuleColumns(cat.Modules().Names()); err != nil {
			t.Fatal(err)
		}
		enable(t, st, "r1", map[string]int{"bgp-peers": 2})
		desired := compute(t, st, cat, defaultRenderConfig(t))
		consumer, ok := consumersByName(desired.Egress.Consumers)[key+"-bgp-peers"]
		if !ok {
			t.Fatalf("missing %s-bgp-peers", key)
		}
		if strings.Join(consumer.Processors, ",") != strings.Join(chain, ",") {
			t.Fatalf("%s chain = %v", key, consumer.Processors)
		}
		info, _ := desired.Egress.Processors.Get("bgp-info")
		if _, ok := specMap(t, info)["event-jq"]; !ok {
			t.Fatal("bgp-info has no event-jq")
		}
		keep, _ := desired.Egress.Processors.Get("bgp-keep-state")
		names := anyStrings(subMap(t, specMap(t, keep), "event-allow")["value-names"])
		if strings.Join(names, ",") != strings.Join(wantNames, ",") {
			t.Fatalf("value-names = %v", names)
		}
		convert, _ := desired.Egress.Processors.Get("bgp-fsm-convert")
		if subMap(t, specMap(t, convert), "event-convert")["type"] != "int" {
			t.Fatal("bgp-fsm-convert type")
		}
	}
}

func TestFederateProcessorAppendedToFederatedChains(t *testing.T) {
	desired := compute(t, federateState(t), federateCatalog(t), defaultRenderConfig(t))
	consumers := consumersByName(desired.Egress.Consumers)
	if strings.Join(consumers["fx-v1-alpha"].Processors, ",") != "pass,add-federate" {
		t.Fatalf("alpha = %v", consumers["fx-v1-alpha"].Processors)
	}
	if strings.Join(consumers["fx-v1-beta"].Processors, ",") != "pass" {
		t.Fatalf("beta = %v", consumers["fx-v1-beta"].Processors)
	}
	if len(consumers["fx-v1-gamma"].Processors) != 0 {
		t.Fatalf("gamma = %v", consumers["fx-v1-gamma"].Processors)
	}
	spec, ok := desired.Egress.Processors.Get("add-federate")
	if !ok {
		t.Fatal("missing add-federate")
	}
	tag := subMap(t, specMap(t, spec), "event-add-tag")
	if tag["overwrite"] != false {
		t.Fatalf("overwrite = %v", tag["overwrite"])
	}
	if names := anyStrings(tag["value-names"]); strings.Join(names, ",") != "^m1$" {
		t.Fatalf("value-names = %v", names)
	}
	if add := subMap(t, tag, "add"); add["federate"] != "true" {
		t.Fatalf("add = %v", add)
	}
}

func TestFederateValueNamesScopeOnlyFederatedMetrics(t *testing.T) {
	desired := compute(t, federateState(t), federateCatalog(t), defaultRenderConfig(t))
	spec, _ := desired.Egress.Processors.Get("add-federate")
	names := anyStrings(subMap(t, specMap(t, spec), "event-add-tag")["value-names"])
	if len(names) != 1 || names[0] != "^m1$" {
		t.Fatalf("value-names = %v", names)
	}
}

func TestNoFederateProcessorWhenNoneFlagged(t *testing.T) {
	desired := compute(t, miniState(t), miniCatalog(t), defaultRenderConfig(t))
	if _, ok := desired.Egress.Processors.Get("add-federate"); ok {
		t.Fatal("add-federate present")
	}
	for _, consumer := range desired.Egress.Consumers {
		for _, proc := range consumer.Processors {
			if proc == "add-federate" {
				t.Fatal("add-federate in chain")
			}
		}
	}
}

func TestFederateAndInstanceOrdering(t *testing.T) {
	desired := compute(t, federateState(t), federateCatalog(t), identityRenderConfig(t))
	consumers := consumersByName(desired.Egress.Consumers)
	want := map[string]string{
		"fx-v1-alpha": "add-instance,pass,add-federate",
		"fx-v1-beta":  "add-instance,pass",
		"fx-v1-gamma": "add-instance",
	}
	for name, chain := range want {
		if got := strings.Join(consumers[name].Processors, ","); got != chain {
			t.Fatalf("%s chain = %s, want %s", name, got, chain)
		}
	}
}

func federateState(t *testing.T) *state.State {
	st := newState(t)
	addTarget(t, st, "r1", "10.0.0.1", 57400, "fx-v1", "fx", "M1", "")
	if err := st.EnsureModuleColumns([]string{"alpha", "beta", "gamma"}); err != nil {
		t.Fatal(err)
	}
	enable(t, st, "r1", map[string]int{"alpha": 1, "beta": 1, "gamma": 1})
	return st
}

// TestGoldenRenderSemantic renders the Python-populated state against the real
// catalog and compares the three artifacts with the golden files produced by the
// Python implementation: same structure, scalar values and mapping key order.
func TestGoldenRenderSemantic(t *testing.T) {
	cat := realCatalog(t)
	goldenDir := filepath.Join("..", "..", "testdata", "golden")
	dbCopy := golden.CopyDB(t)
	cfgPath := filepath.Join(t.TempDir(), "gnpctl.yaml")
	cfgText := "database: " + dbCopy + "\n" +
		"catalog: " + filepath.Join("..", "..", "catalog") + "\n" +
		"render:\n  output_dir: ./render\n  nats:\n    address: nats:4222\n" +
		"  prometheus:\n    remote_write_url: http://prometheus:9090/api/v1/write\n"
	if err := os.WriteFile(cfgPath, []byte(cfgText), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	st, err := state.Open(dbCopy, cat.Modules().Names())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	desired := compute(t, st, cat, cfg.Render)
	outDir := t.TempDir()
	if _, err := emit.Emit(outDir, desired); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ingress.yaml", "targets.yaml", "egress.yaml"} {
		got, err := os.ReadFile(filepath.Join(outDir, name))
		if err != nil {
			t.Fatal(err)
		}
		want, err := os.ReadFile(filepath.Join(goldenDir, "render", name))
		if err != nil {
			t.Fatal(err)
		}
		compareYAML(t, name, got, want)
	}
}

// compareYAML asserts two YAML documents are semantically equal: same kinds,
// same mapping key order and equal decoded scalar values. Presentation (quoting
// style, flow vs block, indentation) is deliberately not compared.
func compareYAML(t *testing.T, name string, got, want []byte) {
	t.Helper()
	var gotNode, wantNode yaml.Node
	if err := yaml.Unmarshal(got, &gotNode); err != nil {
		t.Fatalf("%s: produced invalid YAML: %v", name, err)
	}
	if err := yaml.Unmarshal(want, &wantNode); err != nil {
		t.Fatalf("%s: golden file invalid YAML: %v", name, err)
	}
	compareNode(t, name+": document", &gotNode, &wantNode)
}

func compareNode(t *testing.T, path string, got, want *yaml.Node) {
	t.Helper()
	if got.Kind == yaml.DocumentNode && want.Kind == yaml.DocumentNode {
		if len(got.Content) != 1 || len(want.Content) != 1 {
			t.Fatalf("%s: document has %d/%d roots", path, len(got.Content), len(want.Content))
		}
		compareNode(t, path, got.Content[0], want.Content[0])
		return
	}
	if got.Kind != want.Kind {
		t.Fatalf("%s: kind %v, want %v", path, got.Kind, want.Kind)
	}
	switch got.Kind {
	case yaml.MappingNode:
		if len(got.Content) != len(want.Content) {
			t.Fatalf("%s: %d mapping entries, want %d", path, len(got.Content)/2, len(want.Content)/2)
		}
		for i := 0; i+1 < len(got.Content); i += 2 {
			key := got.Content[i].Value
			if key != want.Content[i].Value {
				t.Fatalf("%s: key #%d is %q, want %q (mapping order differs)",
					path, i/2, key, want.Content[i].Value)
			}
			compareNode(t, path+"."+key, got.Content[i+1], want.Content[i+1])
		}
	case yaml.SequenceNode:
		if len(got.Content) != len(want.Content) {
			t.Fatalf("%s: %d items, want %d", path, len(got.Content), len(want.Content))
		}
		for i := range got.Content {
			compareNode(t, fmt.Sprintf("%s[%d]", path, i), got.Content[i], want.Content[i])
		}
	case yaml.ScalarNode:
		var gotValue, wantValue any
		if err := got.Decode(&gotValue); err != nil {
			t.Fatalf("%s: decode produced scalar: %v", path, err)
		}
		if err := want.Decode(&wantValue); err != nil {
			t.Fatalf("%s: decode golden scalar: %v", path, err)
		}
		if !reflect.DeepEqual(gotValue, wantValue) {
			t.Fatalf("%s = %#v, want %#v", path, gotValue, wantValue)
		}
	default:
		t.Fatalf("%s: unexpected node kind %v", path, got.Kind)
	}
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func anyStrings(value any) []string {
	switch items := value.(type) {
	case []any:
		out := make([]string, 0, len(items))
		for _, item := range items {
			if text, ok := item.(string); ok {
				out = append(out, text)
			}
		}
		return out
	case []string:
		return items
	}
	return nil
}

// specMap renders a processor spec (catalog *yaml.Node or generated
// render.Ordered) as a plain map for assertions.
func specMap(t *testing.T, spec any) map[string]any {
	t.Helper()
	value, ok := tree(spec).(map[string]any)
	if !ok {
		t.Fatalf("spec is %T", spec)
	}
	return value
}

func tree(value any) any {
	switch v := value.(type) {
	case *yaml.Node:
		var decoded any
		if err := v.Decode(&decoded); err != nil {
			return nil
		}
		return decoded
	case render.Ordered:
		out := make(map[string]any, len(v.Keys))
		for i, key := range v.Keys {
			out[key] = tree(v.Values[i])
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = tree(item)
		}
		return out
	}
	return value
}

func subMap(t *testing.T, parent map[string]any, key string) map[string]any {
	t.Helper()
	child, ok := parent[key].(map[string]any)
	if !ok {
		t.Fatalf("%q is %T in %v", key, parent[key], parent)
	}
	return child
}

func readYAML(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]any{}
	if err := yaml.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// captureStderr runs fn with os.Stderr redirected to a pipe and returns what
// was written.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	original := os.Stderr
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = writer
	defer func() { os.Stderr = original }()
	fn()
	writer.Close()
	os.Stderr = original
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	reader.Close()
	return string(data)
}

func TestComputeAddsMissingModuleColumns(t *testing.T) {
	st := newState(t) // opened without module columns, as the CLI does
	addTarget(t, st, "leaf1", "10.0.0.1", 57400, "vx-v1", "vx", "M1", "")
	compute(t, st, miniCatalog(t), defaultRenderConfig(t))
	if v, err := st.ModuleValue("leaf1", "ports"); err != nil || v == nil {
		t.Fatalf("mod_ports column missing after render: v=%v err=%v", v, err)
	}
}
