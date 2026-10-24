package modules

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fatred/gnpctl-v2/internal/catalog"
	"github.com/fatred/gnpctl-v2/internal/state"
)

const testModules = `modules:
  ports: on
  bgp-peers: off
  isis: off
`

const testVendor = `vendor: vx
fingerprint:
  priority: 100
  match:
    org_contains: [X]
`

// isis is in the registry but NOT in this variant's profile.
const testCommon = `key: vx-v1
vendor: vx
modules:
  ports:
    paths:
      - {key: intf, path: /interfaces, mode: sample}
  bgp-peers:
    default: on
    paths:
      - {key: bgp, path: /bgp, mode: sample}
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

func discovered(t *testing.T) *state.State {
	t.Helper()
	s, err := state.Open(filepath.Join(t.TempDir(), "s.db"), nil)
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.UpsertTarget("leaf1", "10.0.0.1", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.EnrichTarget("leaf1", state.Enrichment{
		Discovered:       true,
		LastDiscoveryRun: 1,
		CatalogKey:       "vx-v1",
	}); err != nil {
		t.Fatal(err)
	}
	return s
}

func moduleValue(t *testing.T, s *state.State, target, name string) int {
	t.Helper()
	v, err := s.ModuleValue(target, name)
	if err != nil {
		t.Fatal(err)
	}
	if v == nil {
		return 0
	}
	return *v
}

func TestEnableDisableSetForceValues(t *testing.T) {
	cat := testCatalog(t)
	s := discovered(t)

	warning, err := Enable(s, cat, "leaf1", "bgp-peers")
	if err != nil || warning != "" {
		t.Fatalf("Enable in-profile = %q, %v (want no warning)", warning, err)
	}
	if got := moduleValue(t, s, "leaf1", "bgp-peers"); got != 2 {
		t.Fatalf("bgp-peers = %d, want 2 (forced-on)", got)
	}
	if err := Disable(s, cat, "leaf1", "ports"); err != nil {
		t.Fatal(err)
	}
	if got := moduleValue(t, s, "leaf1", "ports"); got != -1 {
		t.Fatalf("ports = %d, want -1 (forced-off)", got)
	}
}

func TestEnableModuleAbsentFromProfileWarns(t *testing.T) {
	cat := testCatalog(t)
	s := discovered(t)
	warning, err := Enable(s, cat, "leaf1", "isis")
	if err != nil {
		t.Fatal(err)
	}
	if warning == "" || !strings.Contains(warning, "isis") {
		t.Fatalf("warning = %q, want mention of isis", warning)
	}
	if got := moduleValue(t, s, "leaf1", "isis"); got != 2 {
		t.Fatalf("isis = %d, want 2 (still set)", got)
	}
}

func TestResetRecomputesDefault(t *testing.T) {
	cat := testCatalog(t)
	s := discovered(t)
	if err := s.EnsureModuleColumns(cat.Modules().Names()); err != nil {
		t.Fatal(err)
	}
	if err := s.SetModule("leaf1", "ports", 2); err != nil {
		t.Fatal(err)
	}
	if got, err := Reset(s, cat, "leaf1", "ports"); err != nil || got != 1 {
		t.Fatalf("Reset(ports) = %d, %v (want 1)", got, err)
	}
	if got := moduleValue(t, s, "leaf1", "ports"); got != 1 {
		t.Fatalf("ports = %d, want 1", got)
	}
	if got, err := Reset(s, cat, "leaf1", "bgp-peers"); err != nil || got != 1 {
		t.Fatalf("Reset(bgp-peers) = %d, %v (want 1)", got, err)
	}
	if got, err := Reset(s, cat, "leaf1", "isis"); err != nil || got != 0 {
		t.Fatalf("Reset(isis) = %d, %v (want 0)", got, err)
	}
}

func TestShowRendersLabels(t *testing.T) {
	cat := testCatalog(t)
	s := discovered(t)
	if _, err := Enable(s, cat, "leaf1", "bgp-peers"); err != nil {
		t.Fatal(err)
	}
	if err := Disable(s, cat, "leaf1", "ports"); err != nil {
		t.Fatal(err)
	}
	target := "leaf1"
	rows, err := Show(s, cat, &target)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	label := func(name string) string {
		l, _ := rows[0].Modules.Get(name)
		return l
	}
	if got := label("ports"); got != "forced-off" {
		t.Fatalf("ports = %q", got)
	}
	if got := label("bgp-peers"); got != "forced-on" {
		t.Fatalf("bgp-peers = %q", got)
	}
	if got := label("isis"); got != "off" {
		t.Fatalf("isis = %q", got)
	}
	rendered := FormatModules(rows)
	if !strings.Contains(rendered, "leaf1 [vx-v1]") {
		t.Fatalf("rendered header missing: %q", rendered)
	}
	if !strings.Contains(rendered, "forced-on") {
		t.Fatalf("rendered label missing: %q", rendered)
	}
}

func TestUnknownModuleAndTargetErrors(t *testing.T) {
	cat := testCatalog(t)
	s := discovered(t)
	_, err := Enable(s, cat, "leaf1", "not-a-module")
	var merr *ModulesError
	if !errors.As(err, &merr) || merr.Error() != "unknown module 'not-a-module'" {
		t.Fatalf("unknown module err = %v", err)
	}
	_, err = Enable(s, cat, "ghost", "ports")
	if !errors.As(err, &merr) || merr.Error() != "no such target: ghost" {
		t.Fatalf("unknown target err = %v", err)
	}
}

// copyGoldenDB copies the Python-written state fixture into a temp dir so
// EnsureModuleColumns cannot touch the checked-in file.
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

func TestFormatModulesMatchesGolden(t *testing.T) {
	cat, err := catalog.Load(filepath.Join("..", "..", "catalog"))
	if err != nil {
		t.Fatalf("catalog.Load: %v", err)
	}
	s, err := state.Open(copyGoldenDB(t), cat.Modules().Names())
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	defer s.Close()

	// Pre-disable state: the fixture was written after `modules disable`.
	if err := s.SetModule("leaf1", "bgp-peers", 1); err != nil {
		t.Fatal(err)
	}
	rows, err := Show(s, cat, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := FormatModules(rows)+"\n", goldenBody(t, "modules_show.txt"); got != want {
		t.Fatalf("modules show mismatch:\n got:\n%s\nwant:\n%s", got, want)
	}

	if err := Disable(s, cat, "leaf1", "bgp-peers"); err != nil {
		t.Fatal(err)
	}
	leaf1 := "leaf1"
	rows, err = Show(s, cat, &leaf1)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := FormatModules(rows)+"\n", goldenBody(t, "modules_show_after.txt"); got != want {
		t.Fatalf("modules show after mismatch:\n got:\n%s\nwant:\n%s", got, want)
	}
}
