package inventory

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fatred/gnpctl-v2/internal/inventory/sources"
	"github.com/fatred/gnpctl-v2/internal/state"
)

const inventoryA = `
leaf1:
  hostname: 10.0.0.1
  platform: nokia_srl
leaf2:
  hostname: 10.0.0.2
  platform: arista_eos
`

func newInventoryState(t *testing.T) *state.State {
	t.Helper()
	s, err := state.Open(filepath.Join(t.TempDir(), "state.db"), nil)
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func sourceNames(t *testing.T, s *state.State) []string {
	t.Helper()
	rows, err := s.ListSources()
	if err != nil {
		t.Fatalf("ListSources: %v", err)
	}
	names := make([]string, len(rows))
	for i, row := range rows {
		names[i] = row["name"].(string)
	}
	return names
}

// addNornirSource writes text to <tmp>/<name>.yml, registers it and returns the
// inventory path.
func addNornirSource(t *testing.T, s *state.State, text, name string) string {
	t.Helper()
	inv := filepath.Join(t.TempDir(), name+".yml")
	if err := os.WriteFile(inv, []byte(text), 0o644); err != nil {
		t.Fatalf("write inventory: %v", err)
	}
	if err := AddSource(s, name, "nornir", map[string]string{"path": inv}, ""); err != nil {
		t.Fatalf("AddSource(%q): %v", name, err)
	}
	return inv
}

// --- registry ---

func TestAddListRemoveSource(t *testing.T) {
	s := newInventoryState(t)
	addNornirSource(t, s, inventoryA, "clab")
	if got := sourceNames(t, s); len(got) != 1 || got[0] != "clab" {
		t.Fatalf("sources = %v", got)
	}
	if err := s.UpsertTarget("leaf1", new("10.0.0.1"), nil, new(""), ""); err != nil {
		t.Fatalf("UpsertTarget: %v", err)
	}
	removed, err := s.RemoveSource("clab")
	if err != nil || !removed {
		t.Fatalf("RemoveSource = %v, %v", removed, err)
	}
	if got := sourceNames(t, s); len(got) != 0 {
		t.Fatalf("sources after remove = %v", got)
	}
	target, err := s.GetTarget("leaf1")
	if err != nil || target == nil {
		t.Fatalf("target after source removal = %v, %v", target, err)
	}
}

func TestDuplicateSourceRejected(t *testing.T) {
	s := newInventoryState(t)
	inv := addNornirSource(t, s, inventoryA, "clab")
	err := AddSource(s, "clab", "nornir", map[string]string{"path": inv}, "")
	var stateErr *state.StateError
	if !errors.As(err, &stateErr) {
		t.Fatalf("err = %v, want *state.StateError", err)
	}
	if stateErr.Message != "source 'clab' already exists" {
		t.Fatalf("msg = %q", stateErr.Message)
	}
}

func TestUnknownTypeRejected(t *testing.T) {
	s := newInventoryState(t)
	err := AddSource(s, "x", "does-not-exist", map[string]string{}, "")
	var unknown *UnknownSourceType
	if !errors.As(err, &unknown) {
		t.Fatalf("err = %v, want *UnknownSourceType", err)
	}
}

func TestMissingRequiredParamRejected(t *testing.T) {
	s := newInventoryState(t)
	err := AddSource(s, "x", "nornir", map[string]string{}, "")
	var sourceErr *sources.SourceError
	if !errors.As(err, &sourceErr) {
		t.Fatalf("err = %v, want *sources.SourceError", err)
	}
}

func TestNetBoxRequiresCredential(t *testing.T) {
	s := newInventoryState(t)
	err := AddSource(s, "nb", "netbox", map[string]string{"url": "http://nb", "tag": "t"}, "")
	var sourceErr *sources.SourceError
	if !errors.As(err, &sourceErr) {
		t.Fatalf("err = %v, want *sources.SourceError", err)
	}
}

// --- pull ---

func TestPullInsertsWithDefaults(t *testing.T) {
	s := newInventoryState(t)
	addNornirSource(t, s, inventoryA, "clab")
	summary, err := Pull(s, nil, new(int64(500)))
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if summary.Sources != 1 || summary.Targets != 2 || len(summary.Errors) != 0 {
		t.Fatalf("summary = %+v", summary)
	}
	target, err := s.GetTarget("leaf1")
	if err != nil || target == nil {
		t.Fatalf("GetTarget = %v, %v", target, err)
	}
	if target["discovered"] != int64(0) || target["last_discovery_run"] != int64(0) {
		t.Fatalf("discovery defaults = %v/%v", target["discovered"], target["last_discovery_run"])
	}
	if target["address"] != "10.0.0.1" || target["platform_hint"] != "nokia_srl" {
		t.Fatalf("inventory fields = %v/%v", target["address"], target["platform_hint"])
	}
	src, err := s.GetSource("clab")
	if err != nil || src == nil {
		t.Fatalf("GetSource = %v, %v", src, err)
	}
	if src["last_poll"] != int64(500) || src["last_error"] != nil {
		t.Fatalf("source poll = %v/%v", src["last_poll"], src["last_error"])
	}
}

func TestPullStoresNullForAddresslessTarget(t *testing.T) {
	s := newInventoryState(t)
	addNornirSource(t, s, "leaf1:\n  platform: nokia_srl\n", "clab")
	if _, err := Pull(s, nil, new(int64(500))); err != nil {
		t.Fatalf("Pull: %v", err)
	}
	target, err := s.GetTarget("leaf1")
	if err != nil || target == nil {
		t.Fatalf("GetTarget = %v, %v", target, err)
	}
	if target["address"] != nil {
		t.Fatalf("address = %#v, want NULL", target["address"])
	}
	if target["platform_hint"] != "nokia_srl" {
		t.Fatalf("platform_hint = %#v", target["platform_hint"])
	}
}

func TestPullPreservesDiscoveryState(t *testing.T) {
	s := newInventoryState(t)
	addNornirSource(t, s, inventoryA, "clab")
	if _, err := Pull(s, nil, new(int64(100))); err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if err := s.EnrichTarget("leaf1", state.Enrichment{
		Discovered: true, LastDiscoveryRun: 999, CatalogKey: "nokia",
	}); err != nil {
		t.Fatalf("EnrichTarget: %v", err)
	}
	if _, err := Pull(s, nil, new(int64(200))); err != nil {
		t.Fatalf("Pull: %v", err)
	}
	target, err := s.GetTarget("leaf1")
	if err != nil || target == nil {
		t.Fatalf("GetTarget = %v, %v", target, err)
	}
	if target["discovered"] != int64(1) || target["last_discovery_run"] != int64(999) ||
		target["catalog_key"] != "nokia" {
		t.Fatalf("discovery state clobbered: %v", target)
	}
}

func TestPullContinuesPastFailingSource(t *testing.T) {
	s := newInventoryState(t)
	missing := filepath.Join(t.TempDir(), "missing.yml")
	if err := AddSource(s, "broken", "nornir", map[string]string{"path": missing}, ""); err != nil {
		t.Fatalf("AddSource: %v", err)
	}
	addNornirSource(t, s, inventoryA, "clab")
	summary, err := Pull(s, nil, new(int64(300)))
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if summary.Sources != 2 || summary.Targets != 2 || len(summary.Errors) != 1 {
		t.Fatalf("summary = %+v", summary)
	}
	if summary.Errors[0].Name != "broken" || summary.Errors[0].Message == "" {
		t.Fatalf("error = %+v", summary.Errors[0])
	}
	src, err := s.GetSource("broken")
	if err != nil || src == nil {
		t.Fatalf("GetSource = %v, %v", src, err)
	}
	if src["last_poll"] != int64(0) {
		t.Fatalf("last_poll = %v, want unchanged 0", src["last_poll"])
	}
	if msg, _ := src["last_error"].(string); msg == "" {
		t.Fatalf("last_error = %v", src["last_error"])
	}
}

// --- diff ---

func TestDiffReportsVarianceWithoutWriting(t *testing.T) {
	s := newInventoryState(t)
	inv := addNornirSource(t, s, inventoryA, "clab")
	if _, err := Pull(s, nil, new(int64(1))); err != nil {
		t.Fatalf("Pull: %v", err)
	}
	updated := "leaf1:\n  hostname: 10.9.9.9\n  platform: nokia_srl\n" +
		"leaf3:\n  hostname: 10.0.0.3\n  platform: nokia_srl\n"
	if err := os.WriteFile(inv, []byte(updated), 0o644); err != nil {
		t.Fatalf("rewrite inventory: %v", err)
	}
	result, err := Diff(s, nil)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !contains(result.Added, "leaf3") || !contains(result.Removed, "leaf2") ||
		!contains(result.Changed, "leaf1") {
		t.Fatalf("diff = %+v", result)
	}
	target, err := s.GetTarget("leaf1")
	if err != nil || target == nil {
		t.Fatalf("GetTarget = %v, %v", target, err)
	}
	if target["address"] != "10.0.0.1" {
		t.Fatalf("diff wrote address %v", target["address"])
	}
	if added, err := s.GetTarget("leaf3"); err != nil || added != nil {
		t.Fatalf("diff inserted leaf3: %v, %v", added, err)
	}
}

func contains(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}

// --- show ---

func TestShowFiltersAndFormat(t *testing.T) {
	s := newInventoryState(t)
	addNornirSource(t, s, inventoryA, "clab")
	if _, err := Pull(s, nil, new(int64(1))); err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if err := s.RecordTargetError("leaf2", errors.New("timeout")); err != nil {
		t.Fatalf("RecordTargetError: %v", err)
	}
	all, err := s.ShowTargets(false, false, false, state.DefaultStaleWindow, nil)
	if err != nil {
		t.Fatalf("ShowTargets: %v", err)
	}
	if names := targetNames(t, all); len(names) != 2 {
		t.Fatalf("all = %v", names)
	}
	failed, err := s.ShowTargets(true, false, false, state.DefaultStaleWindow, nil)
	if err != nil {
		t.Fatalf("ShowTargets: %v", err)
	}
	if names := targetNames(t, failed); len(names) != 1 || names[0] != "leaf2" {
		t.Fatalf("errors = %v", names)
	}
	table := FormatTable(all)
	if !strings.Contains(table, "name") || !strings.Contains(table, "leaf1") ||
		!strings.Contains(table, "timeout") {
		t.Fatalf("table:\n%s", table)
	}
}

func targetNames(t *testing.T, rows []map[string]any) []string {
	t.Helper()
	names := make([]string, len(rows))
	for i, row := range rows {
		names[i] = row["name"].(string)
	}
	return names
}

func goldenBody(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "golden", name))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	body := strings.SplitN(string(raw), "\n", 2)
	if len(body) != 2 {
		t.Fatalf("golden %s has no output body", name)
	}
	return body[1]
}

func TestFormatTableMatchesGoldenShow(t *testing.T) {
	rows := []map[string]any{
		{
			"name": "leaf1", "address": "172.20.20.3", "port": int64(57400),
			"discovered": int64(1), "vendor": "nokia", "model": "7220 IXR-D2L",
			"os_version": "v24.10.4-244-g5c0afd1a61", "catalog_key": "nokia-srlinux",
			"last_error": nil,
		},
		{
			"name": "spine1", "address": "172.20.20.4", "port": int64(6030),
			"discovered": int64(1), "vendor": "arista", "model": nil,
			"os_version": "4.33.4M", "catalog_key": "arista-eos", "last_error": nil,
		},
	}
	want := goldenBody(t, "inv_show.txt")
	if got := FormatTable(rows) + "\n"; got != want {
		t.Fatalf("table mismatch\ngot:\n%q\nwant:\n%q", got, want)
	}
}

func TestFormatTableMatchesGoldenShowUndiscovered(t *testing.T) {
	rows := []map[string]any{
		{
			"name": "leaf1", "address": "172.20.20.3", "port": nil,
			"discovered": int64(0), "vendor": nil, "model": nil,
			"os_version": nil, "catalog_key": nil, "last_error": nil,
		},
		{
			"name": "spine1", "address": "172.20.20.4", "port": nil,
			"discovered": int64(0), "vendor": nil, "model": nil,
			"os_version": nil, "catalog_key": nil, "last_error": nil,
		},
	}
	want := goldenBody(t, "inv_show_undiscovered.txt")
	if got := FormatTable(rows) + "\n"; got != want {
		t.Fatalf("table mismatch\ngot:\n%q\nwant:\n%q", got, want)
	}
}

func TestFormatDiffMatchesGolden(t *testing.T) {
	want := goldenBody(t, "inv_diff_before.txt")
	got := FormatDiff(DiffResult{Added: []string{"leaf1", "spine1"}}) + "\n"
	if got != want {
		t.Fatalf("diff mismatch\ngot:\n%q\nwant:\n%q", got, want)
	}
}

func TestFormatDiffOrdersChanges(t *testing.T) {
	result := DiffResult{
		Added:   []string{"a"},
		Changed: []string{"b"},
		Removed: []string{"c"},
	}
	if got, want := FormatDiff(result), "+ a\n~ b\n- c"; got != want {
		t.Fatalf("FormatDiff = %q, want %q", got, want)
	}
}
