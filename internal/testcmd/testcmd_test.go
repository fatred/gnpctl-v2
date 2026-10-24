package testcmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fatred/gnpctl-v2/internal/catalog"
	"github.com/fatred/gnpctl-v2/internal/pipeline"
	"github.com/fatred/gnpctl-v2/internal/state"
	"github.com/fatred/gnpctl-v2/internal/transport"
)

const testModules = `modules:
  ports: on
  bgp-peers: off
`

const testVendor = `vendor: vx
fingerprint:
  priority: 100
  match:
    org_contains: [X]
`

const testCommon = `key: vx-v1
vendor: vx
modules:
  ports:
    paths:
      - {key: intf, path: /interfaces, mode: sample}
  bgp-peers:
    paths:
      - {key: bgp, path: /bgp, mode: on_change}
`

func testCatalog(t *testing.T) *catalog.LayeredCatalog {
	t.Helper()
	root := filepath.Join(t.TempDir(), "catalog")
	files := map[string]string{
		"modules.yaml":      testModules,
		"vx/vendor.yaml":    testVendor,
		"vx/v1/common.yaml": testCommon,
	}
	for rel, text := range files {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cat, err := catalog.Load(root)
	if err != nil {
		t.Fatalf("catalog.Load: %v", err)
	}
	return cat
}

func openTestState(t *testing.T) *state.State {
	t.Helper()
	s, err := state.Open(filepath.Join(t.TempDir(), "s.db"), nil)
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func discovered(t *testing.T, s *state.State, cat *catalog.LayeredCatalog, name string, modules ...string) {
	t.Helper()
	port := 57400
	if err := s.UpsertTarget(name, new("10.0.0.1"), &port, new(""), ""); err != nil {
		t.Fatal(err)
	}
	if err := s.EnrichTarget(name, state.Enrichment{
		Discovered: true, LastDiscoveryRun: 1, CatalogKey: "vx-v1",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureModuleColumns(cat.Modules().Names()); err != nil {
		t.Fatal(err)
	}
	if len(modules) == 0 {
		modules = []string{"ports", "bgp-peers"}
	}
	for _, module := range modules {
		if err := s.SetModule(name, module, 1); err != nil {
			t.Fatal(err)
		}
	}
}

func fixedTransport(tr transport.GnmiTransport) pipeline.TransportFactory {
	return func(map[string]any, int) (transport.GnmiTransport, error) { return tr, nil }
}

func fixture(subs map[string]map[string]any) *transport.FixtureTransport {
	return transport.NewFixtureTransport(nil, subs)
}

func resultsFor(t *testing.T, results []TargetTestResult, name string) TargetTestResult {
	t.Helper()
	for _, result := range results {
		if result.Name == name {
			return result
		}
	}
	t.Fatalf("no result for %s", name)
	return TargetTestResult{}
}

func TestPerPathResultsForEnabledModules(t *testing.T) {
	cat := testCatalog(t)
	s := openTestState(t)
	discovered(t, s, cat, "leaf1")
	tr := fixture(map[string]map[string]any{
		"/interfaces": {"leaf": 1},
		"/bgp":        {},
	})
	results, anyFailed, err := RunTest(s, cat, nil, fixedTransport(tr))
	if err != nil {
		t.Fatalf("RunTest: %v", err)
	}
	leaf1 := resultsFor(t, results, "leaf1")
	if !leaf1.Ready {
		t.Fatal("leaf1 not ready")
	}
	byPath := map[string]bool{}
	for _, path := range leaf1.Results {
		byPath[path.Path] = path.OK
	}
	if !byPath["/interfaces"] || byPath["/bgp"] {
		t.Fatalf("byPath = %v", byPath)
	}
	if !anyFailed {
		t.Fatal("anyFailed = false")
	}
}

func TestDisabledModulePathsSkipped(t *testing.T) {
	cat := testCatalog(t)
	s := openTestState(t)
	discovered(t, s, cat, "leaf1", "ports")
	if err := s.SetModule("leaf1", "bgp-peers", -1); err != nil {
		t.Fatal(err)
	}
	tr := fixture(map[string]map[string]any{"/interfaces": {"leaf": 1}})
	results, anyFailed, err := RunTest(s, cat, nil, fixedTransport(tr))
	if err != nil {
		t.Fatalf("RunTest: %v", err)
	}
	paths := map[string]bool{}
	for _, path := range results[0].Results {
		paths[path.Path] = true
	}
	if len(paths) != 1 || !paths["/interfaces"] {
		t.Fatalf("paths = %v", paths)
	}
	if anyFailed {
		t.Fatal("anyFailed = true")
	}
}

func TestNotReadyTargetSkipped(t *testing.T) {
	cat := testCatalog(t)
	s := openTestState(t)
	port := 57400
	if err := s.UpsertTarget("undisc", new("10.0.0.2"), &port, new(""), ""); err != nil {
		t.Fatal(err)
	}
	tr := fixture(map[string]map[string]any{"/interfaces": {"leaf": 1}})
	target := "undisc"
	results, _, err := RunTest(s, cat, &target, fixedTransport(tr))
	if err != nil {
		t.Fatalf("RunTest: %v", err)
	}
	if len(results) != 1 || results[0].Ready || len(results[0].Results) != 0 {
		t.Fatalf("results = %+v", results)
	}
}

func TestAllPathsOkNoFailure(t *testing.T) {
	cat := testCatalog(t)
	s := openTestState(t)
	discovered(t, s, cat, "leaf1")
	tr := fixture(map[string]map[string]any{
		"/interfaces": {"leaf": 1},
		"/bgp":        {"leaf": 2},
	})
	_, anyFailed, err := RunTest(s, cat, nil, fixedTransport(tr))
	if err != nil {
		t.Fatalf("RunTest: %v", err)
	}
	if anyFailed {
		t.Fatal("anyFailed = true")
	}
}

func TestTargetScoping(t *testing.T) {
	cat := testCatalog(t)
	s := openTestState(t)
	discovered(t, s, cat, "leaf1")
	discovered(t, s, cat, "leaf2")
	tr := fixture(map[string]map[string]any{
		"/interfaces": {"leaf": 1},
		"/bgp":        {"leaf": 2},
	})
	target := "leaf1"
	results, _, err := RunTest(s, cat, &target, fixedTransport(tr))
	if err != nil {
		t.Fatalf("RunTest: %v", err)
	}
	if len(results) != 1 || results[0].Name != "leaf1" {
		t.Fatalf("results = %+v", results)
	}
}

// copyGoldenDB copies the Python-written state fixture into a temp dir so the
// test cannot touch the checked-in file.
func copyGoldenDB(t *testing.T) string {
	t.Helper()
	src, err := os.Open(filepath.Join("..", "..", "testdata", "golden", "python_state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	dst := filepath.Join(t.TempDir(), "state.db")
	out, err := os.Create(dst)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, src); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	return dst
}

func goldenBody(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "golden", name))
	if err != nil {
		t.Fatal(err)
	}
	i := strings.IndexByte(string(data), '\n')
	if i < 0 {
		t.Fatalf("%s: missing exit line", name)
	}
	return string(data)[i+1:]
}

// everyPathFactory serves a sample for every variant path: the golden run's
// fixture transport did the same via setdefault.
func everyPathFactory(t *testing.T, cat *catalog.LayeredCatalog) pipeline.TransportFactory {
	t.Helper()
	subs := map[string]map[string]any{}
	for _, vendor := range cat.Vendors() {
		for _, variant := range cat.VariantsFor(vendor.Vendor) {
			profile, err := cat.EffectiveProfile(variant.Key, nil)
			if err != nil {
				t.Fatalf("EffectiveProfile(%s): %v", variant.Key, err)
			}
			for _, name := range profile.Modules.Names() {
				module, _ := profile.Modules.Get(name)
				for _, p := range module.Paths {
					subs[p.Path] = map[string]any{p.Path: 1}
				}
			}
		}
	}
	return fixedTransport(transport.NewFixtureTransport(nil, subs))
}

func TestRunTestMatchesGolden(t *testing.T) {
	cat, err := catalog.Load(filepath.Join("..", "..", "catalog"))
	if err != nil {
		t.Fatalf("catalog.Load: %v", err)
	}
	s, err := state.Open(copyGoldenDB(t), cat.Modules().Names())
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	defer s.Close()

	results, anyFailed, err := RunTest(s, cat, nil, everyPathFactory(t, cat))
	if err != nil {
		t.Fatalf("RunTest: %v", err)
	}
	if anyFailed {
		t.Fatal("anyFailed = true")
	}

	var out strings.Builder
	for _, result := range results {
		if !result.Ready {
			fmt.Fprintf(&out, "%s: not-ready (no catalog match)\n", result.Name)
			continue
		}
		for _, path := range result.Results {
			status := "ok"
			if !path.OK {
				status = "FAIL"
			}
			detail := ""
			if path.Detail != "" {
				detail = " (" + path.Detail + ")"
			}
			fmt.Fprintf(&out, "%s %s: %s%s\n", result.Name, path.Path, status, detail)
		}
	}
	if got, want := out.String(), goldenBody(t, "test.txt"); got != want {
		t.Fatalf("test output mismatch:\n got:\n%s\nwant:\n%s", got, want)
	}
}
