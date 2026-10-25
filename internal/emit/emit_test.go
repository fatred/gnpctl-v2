package emit_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fatred/gnpctl-v2/internal/emit"
	"github.com/fatred/gnpctl-v2/internal/render"
	"gopkg.in/yaml.v3"
)

func workqueueStream() render.Ordered {
	stream := render.Ordered{}
	stream.Set("retention-policy", "workqueue")
	stream.Set("storage", "memory")
	stream.Set("subjects", []string{})
	return stream
}

func desiredConfig() *render.DesiredConfig {
	return &render.DesiredConfig{
		Ingress: render.IngressConfig{
			Global: render.IngressGlobals{
				TargetsFile: "targets.yaml",
				UsernameRef: "${TARGET_USER}",
				PasswordRef: "${TARGET_PASS}",
			},
			Subscriptions: []render.Subscription{{
				Name: "vx-v1-ports",
				StreamSubscriptions: []render.StreamSub{
					{Paths: []string{"/a", "/b"}, Mode: "sample", SampleInterval: new("15s")},
					{Paths: []string{"/c"}, Mode: "sample", SampleInterval: new("30s")},
				},
				Outputs: []string{"vx-v1-ports"},
			}},
			Outputs: []render.JetstreamOutput{{
				Name:            "vx-v1-ports",
				Stream:          "vx-v1-ports",
				Address:         "nats:4222",
				CreateStream:    workqueueStream(),
				SubjectFormat:   "subscription.target",
				Format:          "event",
				NumWorkers:      1,
				WriteTimeout:    "5s",
				ConnectTimeWait: "2s",
			}},
		},
		Targets: render.TargetsConfig{Targets: []render.TargetEntry{{
			Name:          "leaf1",
			Address:       new("10.0.0.1"),
			Port:          new(57400),
			Encoding:      new("proto"),
			Subscriptions: []string{"vx-v1-ports"},
			Tags: func() render.Ordered {
				tags := render.Ordered{}
				tags.Set("vendor", "vx")
				tags.Set("model", "M1")
				tags.Set("variant", "vx-v1")
				return tags
			}(),
		}}},
		Egress: render.EgressConfig{
			NatsAddress:              "nats:4222",
			PrometheusRemoteWriteURL: "http://prom:9090/api/v1/write",
			Consumers: []render.EgressConsumer{{
				Name: "vx-v1-ports", Stream: "vx-v1-ports",
				Processors: []string{"add-device-labels"},
			}},
			Processors: func() render.Ordered {
				spec := render.Ordered{}
				add := render.Ordered{}
				add.Set("source", "gnpctl")
				tag := render.Ordered{}
				tag.Set("add", add)
				spec.Set("event-add-tag", tag)
				processors := render.Ordered{}
				processors.Set("add-device-labels", spec)
				return processors
			}(),
			InputTuning: render.EgressInputTuning{
				NumWorkers: 10, FetchBatchSize: 500, MaxAckPending: 100000,
				BufferSize: 10000, DeliverPolicy: "all", Format: "event",
			},
			Prometheus: render.PrometheusTuning{
				Interval: "2s", BufferSize: 100000, MaxTimeSeriesPerWrite: 10000,
				Timeout: "10s", NumWorkers: 10,
			},
		},
	}
}

func emitFiles(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if _, err := emit.Emit(dir, desiredConfig()); err != nil {
		t.Fatal(err)
	}
	return dir
}

func readDoc(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	doc := map[string]any{}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func m(t *testing.T, parent map[string]any, key string) map[string]any {
	t.Helper()
	child, ok := parent[key].(map[string]any)
	if !ok {
		t.Fatalf("%q is %T", key, parent[key])
	}
	return child
}

func TestEmitsThreeFiles(t *testing.T) {
	dir := t.TempDir()
	written, err := emit.Emit(dir, desiredConfig())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ingress.yaml", "targets.yaml", "egress.yaml"}
	if len(written) != len(want) {
		t.Fatalf("written = %v", written)
	}
	for i, name := range want {
		if filepath.Base(written[i]) != name {
			t.Fatalf("written[%d] = %s, want %s", i, written[i], name)
		}
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestIngressYAMLShape(t *testing.T) {
	doc := readDoc(t, filepath.Join(emitFiles(t), "ingress.yaml"))
	sub := m(t, m(t, doc, "subscriptions"), "vx-v1-ports")
	if outputs := sub["outputs"].([]any); len(outputs) != 1 || outputs[0] != "vx-v1-ports" {
		t.Fatalf("outputs = %v", sub["outputs"])
	}
	intervals := map[string][]any{}
	for _, item := range sub["stream-subscriptions"].([]any) {
		group := item.(map[string]any)
		intervals[group["sample-interval"].(string)] = group["paths"].([]any)
	}
	if len(intervals) != 2 || len(intervals["15s"]) != 2 || intervals["30s"][0] != "/c" {
		t.Fatalf("intervals = %v", intervals)
	}
	first := sub["stream-subscriptions"].([]any)[0].(map[string]any)
	if first["stream-mode"] != "sample" {
		t.Fatalf("stream-mode = %v", first["stream-mode"])
	}
	out := m(t, m(t, doc, "outputs"), "vx-v1-ports")
	if out["type"] != "jetstream" || out["stream"] != "vx-v1-ports" ||
		out["subject-format"] != "subscription.target" {
		t.Fatalf("output = %v", out)
	}
	if m(t, out, "create-stream")["retention-policy"] != "workqueue" {
		t.Fatalf("create-stream = %v", out["create-stream"])
	}
	if _, ok := doc["targets"]; ok {
		t.Fatal("targets must be a separate artifact")
	}
}

func TestTargetsYAMLIsSeparateAndShaped(t *testing.T) {
	doc := readDoc(t, filepath.Join(emitFiles(t), "targets.yaml"))
	leaf := m(t, doc, "leaf1")
	if leaf["address"] != "10.0.0.1:57400" || leaf["encoding"] != "proto" {
		t.Fatalf("leaf = %v", leaf)
	}
	if subs := leaf["subscriptions"].([]any); len(subs) != 1 || subs[0] != "vx-v1-ports" {
		t.Fatalf("subscriptions = %v", leaf["subscriptions"])
	}
	tags := m(t, leaf, "tags")
	if tags["vendor"] != "vx" || tags["model"] != "M1" || tags["variant"] != "vx-v1" {
		t.Fatalf("tags = %v", tags)
	}
}

func TestNonPositivePortRendersBareAddress(t *testing.T) {
	desired := desiredConfig()
	desired.Targets.Targets[0].Port = new(-1)
	dir := t.TempDir()
	if _, err := emit.Emit(dir, desired); err != nil {
		t.Fatal(err)
	}
	leaf := m(t, readDoc(t, filepath.Join(dir, "targets.yaml")), "leaf1")
	if leaf["address"] != "10.0.0.1" {
		t.Fatalf("address = %v", leaf["address"])
	}
}

func TestEgressYAMLShape(t *testing.T) {
	doc := readDoc(t, filepath.Join(emitFiles(t), "egress.yaml"))
	inp := m(t, m(t, doc, "inputs"), "vx-v1-ports")
	if inp["type"] != "jetstream" || inp["stream"] != "vx-v1-ports" {
		t.Fatalf("input = %v", inp)
	}
	if processors := inp["event-processors"].([]any); len(processors) != 1 ||
		processors[0] != "add-device-labels" {
		t.Fatalf("event-processors = %v", inp["event-processors"])
	}
	spec := m(t, m(t, doc, "processors"), "add-device-labels")
	if add := m(t, m(t, spec, "event-add-tag"), "add"); add["source"] != "gnpctl" {
		t.Fatalf("spec = %v", spec)
	}
	prom := m(t, m(t, doc, "outputs"), "prometheus")
	if prom["type"] != "prometheus_write" || prom["url"] != "http://prom:9090/api/v1/write" {
		t.Fatalf("prometheus = %v", prom)
	}
}

func TestYAMLKeyOrderMatchesPython(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(emitFiles(t), "targets.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	leaf := nodeAt(t, &doc, "leaf1")
	if got, want := mappingKeys(leaf), []string{"address", "encoding", "subscriptions", "tags"}; !equalStrings(got, want) {
		t.Fatalf("leaf keys = %v, want %v", got, want)
	}
	if got, want := mappingKeys(nodeAt(t, leaf, "tags")), []string{"vendor", "model", "variant"}; !equalStrings(got, want) {
		t.Fatalf("tag keys = %v, want %v", got, want)
	}
}

func TestStringTrueStaysString(t *testing.T) {
	doc := render.Ordered{}
	doc.Set("flag", "true")
	doc.Set("plain", "value")
	dir := t.TempDir()
	desired := &render.DesiredConfig{Egress: render.EgressConfig{
		Processors: func() render.Ordered {
			processors := render.Ordered{}
			processors.Set("p", doc)
			return processors
		}(),
	}}
	if _, err := emit.Emit(dir, desired); err != nil {
		t.Fatal(err)
	}
	got := m(t, m(t, readDoc(t, filepath.Join(dir, "egress.yaml")), "processors"), "p")
	if got["flag"] != "true" || got["plain"] != "value" {
		t.Fatalf("processors.p = %v", got)
	}
}

func TestScalarsRoundTrip(t *testing.T) {
	spec := render.Ordered{}
	spec.Set("multiline", "line1\nline2\n")
	spec.Set("unicode", "Zürich")
	processors := render.Ordered{}
	processors.Set("round-trip", spec)
	dir := t.TempDir()
	desired := &render.DesiredConfig{Egress: render.EgressConfig{Processors: processors}}
	if _, err := emit.Emit(dir, desired); err != nil {
		t.Fatal(err)
	}
	got := m(t, m(t, readDoc(t, filepath.Join(dir, "egress.yaml")), "processors"), "round-trip")
	if got["multiline"] != "line1\nline2\n" || got["unicode"] != "Zürich" {
		t.Fatalf("scalars = %v", got)
	}
}

func nodeAt(t *testing.T, node *yaml.Node, keys ...string) *yaml.Node {
	t.Helper()
	if node.Kind == yaml.DocumentNode {
		if len(node.Content) == 0 {
			t.Fatal("empty document")
		}
		node = node.Content[0]
	}
	for _, key := range keys {
		if node.Kind != yaml.MappingNode {
			t.Fatalf("%q: parent is not a mapping", key)
		}
		var next *yaml.Node
		for i := 0; i+1 < len(node.Content); i += 2 {
			if node.Content[i].Value == key {
				next = node.Content[i+1]
				break
			}
		}
		if next == nil {
			t.Fatalf("key %q not found", key)
		}
		node = next
	}
	return node
}

func mappingKeys(node *yaml.Node) []string {
	if node.Kind != yaml.MappingNode {
		return nil
	}
	keys := make([]string, 0, len(node.Content)/2)
	for i := 0; i+1 < len(node.Content); i += 2 {
		keys = append(keys, node.Content[i].Value)
	}
	return keys
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
