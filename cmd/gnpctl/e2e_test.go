package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/fatred/gnpctl-v2/internal/catalog"
	"github.com/fatred/gnpctl-v2/internal/pipeline"
	"github.com/fatred/gnpctl-v2/internal/transport"
)

// Golden files are the stdout of the Python CLI running the scenario in
// scripts/gen_golden.py, prefixed with "exit=N".
const goldenDir = "../../testdata/golden"

// lastPollRE masks the wall-clock value inventory list prints, which cannot be
// equal to the timestamp baked into the golden.
var lastPollRE = regexp.MustCompile(`last_poll=\d+`)

func readJSON[T any](t *testing.T, path string) T {
	t.Helper()
	var value T
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return value
}

// fixtureFactory mirrors gen_golden.py's transport_for: captured capabilities
// and probe leaves per platform, plus a sample for every subscription path of
// that vendor's variants so `test` exercises them all.
func fixtureFactory(t *testing.T, cat *catalog.LayeredCatalog) pipeline.TransportFactory {
	fixtures := "../../testdata/fixtures"
	return func(target map[string]any, _ int) (transport.GnmiTransport, error) {
		hint, _ := target["platform_hint"].(string)
		var caps map[string]any
		var subs map[string]map[string]any
		var vendor string
		switch {
		case strings.Contains(hint, "juniper"):
			caps = readJSON[map[string]any](t, filepath.Join(fixtures, "juniper/capabilities.json"))
			subs = readJSON[map[string]map[string]any](t, filepath.Join(fixtures, "juniper/qfx10k_components.json"))
			vendor = "juniper"
		case strings.Contains(hint, "nokia") || strings.Contains(hint, "srl"):
			caps = readJSON[map[string]any](t, filepath.Join(fixtures, "nokia_srl/capabilities.json"))
			subs = readJSON[map[string]map[string]any](t, filepath.Join(fixtures, "nokia_srl/subscribe.json"))
			vendor = "nokia"
		default:
			caps = readJSON[map[string]any](t, filepath.Join(fixtures, "arista_eos/capabilities.json"))
			subs = readJSON[map[string]map[string]any](t, filepath.Join(fixtures, "arista_eos/subscribe.json"))
			vendor = "arista"
		}
		for _, variant := range cat.VariantsFor(vendor) {
			profile, err := cat.EffectiveProfile(variant.Key, nil)
			if err != nil {
				return nil, err
			}
			for _, name := range profile.Modules.Names() {
				module, _ := profile.Modules.Get(name)
				for _, object := range module.Paths {
					if _, ok := subs[object.Path]; !ok {
						subs[object.Path] = map[string]any{object.Path: 1}
					}
				}
			}
		}
		return transport.NewFixtureTransport(caps, subs), nil
	}
}

func writeConfig(t *testing.T, dir string) string {
	t.Helper()
	catalogDir, err := filepath.Abs("../../catalog")
	if err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "gnpctl.yaml")
	text := fmt.Sprintf(`database: %s
catalog: %s
log:
  level: warning
render:
  output_dir: %s
  nats:
    address: nats:4222
  prometheus:
    remote_write_url: http://prometheus:9090/api/v1/write
`, filepath.Join(dir, "state.db"), catalogDir, filepath.Join(dir, "render"))
	if err := os.WriteFile(cfg, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func invoke(cfg string, args ...string) (int, string) {
	var out, errw bytes.Buffer
	code := run(append([]string{"--config", cfg}, args...), &out, &errw)
	return code, out.String()
}

func readGolden(t *testing.T, name string) (int, string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(goldenDir, name+".txt"))
	if err != nil {
		t.Fatalf("read golden %s: %v", name, err)
	}
	head, body, _ := strings.Cut(string(data), "\n")
	code, err := strconv.Atoi(strings.TrimPrefix(head, "exit="))
	if err != nil {
		t.Fatalf("golden %s: bad exit line %q", name, head)
	}
	return code, body
}

// expected normalises the two things a fresh run cannot reproduce: the wall
// clock in inventory list, and the throwaway render directory in render.
func expectedOutput(name, text, outDir string) string {
	text = lastPollRE.ReplaceAllString(text, "last_poll=<ts>")
	if m := regexp.MustCompile(`(?m)^wrote (.+)/ingress\.yaml$`).FindStringSubmatch(text); m != nil {
		text = strings.ReplaceAll(text, m[1], outDir)
	}
	return text
}

func compileYAML(t *testing.T, path string) *yaml.Node {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return &doc
}

// compareYAML asserts equal structure and values in the same key order; bytes
// may differ (quoting style etc.) because emit is free to pick a scalar style.
func compareYAML(t *testing.T, path string, want, got *yaml.Node) {
	t.Helper()
	if want.Kind != got.Kind || want.Tag != got.Tag || want.Value != got.Value {
		t.Errorf("%s: node mismatch at %q: want kind=%d tag=%s value=%q, got kind=%d tag=%s value=%q",
			path, want.Value, want.Kind, want.Tag, want.Value, got.Kind, got.Tag, got.Value)
		return
	}
	if len(want.Content) != len(got.Content) {
		t.Errorf("%s: %q has %d children, want %d\n--- got ---\n%s\n--- want ---\n%s",
			path, want.Value, len(got.Content), len(want.Content), nodeText(got), nodeText(want))
		return
	}
	for i := range want.Content {
		compareYAML(t, path, want.Content[i], got.Content[i])
	}
}

func TestCLIScenarioMatchesGolden(t *testing.T) {
	cat, err := catalog.Load("../../catalog")
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	previous := pipeline.DefaultTransportFactory
	pipeline.DefaultTransportFactory = fixtureFactory(t, cat)
	t.Cleanup(func() { pipeline.DefaultTransportFactory = previous })

	tmp := t.TempDir()
	cfg := writeConfig(t, tmp)
	outDir := filepath.Join(tmp, "render")
	inventory, err := filepath.Abs("../../testdata/fixtures/clab_nornir.yml")
	if err != nil {
		t.Fatal(err)
	}

	steps := []struct {
		golden string
		args   []string
	}{
		{"inv_add", []string{"inventory", "add", "clab", "--type", "nornir", "-p", "path=" + inventory}},
		{"inv_diff_before", []string{"inventory", "diff"}},
		{"inv_pull", []string{"inventory", "pull"}},
		{"inv_list", []string{"inventory", "list"}},
		{"inv_show_undiscovered", []string{"inventory", "show", "--undiscovered"}},
		{"discover", []string{"discover"}},
		{"inv_show", []string{"inventory", "show"}},
		{"modules_show", []string{"modules", "show"}},
		{"modules_disable", []string{"modules", "disable", "bgp-peers", "--target", "leaf1"}},
		{"modules_show_after", []string{"modules", "show", "--target", "leaf1"}},
		{"test", []string{"test"}},
		{"render", []string{"render"}},
	}
	for _, step := range steps {
		wantCode, want := readGolden(t, step.golden)
		code, got := invoke(cfg, step.args...)
		if code != wantCode {
			t.Fatalf("%s: exit=%d, want %d\nstdout:\n%s", step.golden, code, wantCode, got)
		}
		want = expectedOutput(step.golden, want, outDir)
		if step.golden == "inv_list" {
			got = lastPollRE.ReplaceAllString(got, "last_poll=<ts>")
		}
		if got != want {
			t.Errorf("%s: stdout mismatch\n--- got ---\n%s\n--- want ---\n%s", step.golden, got, want)
		}
	}

	other := filepath.Join(tmp, "other")
	if code, stdout := invoke(cfg, "render", "--output-dir", other); code != 0 {
		t.Fatalf("render --output-dir: exit=%d\n%s", code, stdout)
	}
	for _, dir := range []string{outDir, other} {
		for _, name := range []string{"ingress.yaml", "egress.yaml", "targets.yaml"} {
			want := compileYAML(t, filepath.Join(goldenDir, "render", name))
			got := compileYAML(t, filepath.Join(dir, name))
			compareYAML(t, name, want, got)
		}
	}
}

func TestHelpNeedsNoConfig(t *testing.T) {
	var out, errw bytes.Buffer
	if code := run([]string{"--help"}, &out, &errw); code != 0 {
		t.Fatalf("--help exit=%d, stderr:\n%s", code, errw.String())
	}
	if !strings.Contains(out.String(), "gNMI discovery and path-catalog control") {
		t.Errorf("--help output missing description:\n%s", out.String())
	}
}

func nodeText(n *yaml.Node) string {
	data, err := yaml.Marshal(n)
	if err != nil {
		return ""
	}
	return string(data)
}
