package state

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func newState(t *testing.T, modules ...string) *State {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"), modules)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func targetNames(t *testing.T, rows []map[string]any) []string {
	t.Helper()
	names := make([]string, len(rows))
	for i, r := range rows {
		names[i] = r["name"].(string)
	}
	return names
}

func TestSchemaCreatedInWAL(t *testing.T) {
	s := newState(t)
	var mode string
	if err := s.db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", mode)
	}
	rows, err := s.db.Query("SELECT name FROM sqlite_master WHERE type='table'")
	if err != nil {
		t.Fatalf("sqlite_master: %v", err)
	}
	defer rows.Close()
	names := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names[n] = true
	}
	for _, want := range []string{"targets", "inventory_sources"} {
		if !names[want] {
			t.Errorf("table %q missing (got %v)", want, names)
		}
	}
}

func TestInsertDefaults(t *testing.T) {
	s := newState(t)
	if err := s.UpsertTarget("leaf1", new("10.0.0.1"), nil, new("nokia_srl"), ""); err != nil {
		t.Fatalf("UpsertTarget: %v", err)
	}
	tgt, err := s.GetTarget("leaf1")
	if err != nil {
		t.Fatalf("GetTarget: %v", err)
	}
	if tgt["discovered"] != int64(0) || tgt["last_discovery_run"] != int64(0) || tgt["catalog_key"] != nil {
		t.Fatalf("defaults wrong: %v", tgt)
	}
}

func TestEnrichmentAtomicAndIdempotent(t *testing.T) {
	s := newState(t)
	if err := s.UpsertTarget("leaf1", new("10.0.0.1"), nil, new(""), ""); err != nil {
		t.Fatal(err)
	}
	if err := s.EnrichTarget("leaf1", Enrichment{Discovered: true, LastDiscoveryRun: 100, CatalogKey: "k1", Vendor: "nokia"}); err != nil {
		t.Fatal(err)
	}
	if err := s.EnrichTarget("leaf1", Enrichment{Discovered: true, LastDiscoveryRun: 200, CatalogKey: "k2", Vendor: "nokia"}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM targets WHERE name='leaf1'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("rows = %d, want 1", n)
	}
	tgt, err := s.GetTarget("leaf1")
	if err != nil {
		t.Fatal(err)
	}
	if tgt["catalog_key"] != "k2" || tgt["last_discovery_run"] != int64(200) {
		t.Fatalf("enrichment wrong: %v", tgt)
	}
}

func TestConnectionErrorPreservesRetryEligibility(t *testing.T) {
	s := newState(t)
	if err := s.UpsertTarget("leaf1", new("10.0.0.1"), nil, new(""), ""); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordTargetError("leaf1", errors.New("connection refused")); err != nil {
		t.Fatal(err)
	}
	tgt, err := s.GetTarget("leaf1")
	if err != nil {
		t.Fatal(err)
	}
	if tgt["discovered"] != int64(0) || tgt["last_discovery_run"] != int64(0) {
		t.Fatalf("discovery state touched: %v", tgt)
	}
	if tgt["last_error"] != "connection refused" {
		t.Fatalf("last_error = %v", tgt["last_error"])
	}
	selected, err := s.SelectTargets("default", DefaultStaleWindow, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, n := range targetNames(t, selected) {
		found = found || n == "leaf1"
	}
	if !found {
		t.Fatalf("leaf1 not selected: %v", selected)
	}
}

func TestSourceCRUDAndDuplicate(t *testing.T) {
	s := newState(t)
	if err := s.AddSource("nb", "netbox", map[string]any{"url": "http://nb"}, "NETBOX_TOKEN"); err != nil {
		t.Fatalf("AddSource: %v", err)
	}
	srcs, err := s.ListSources()
	if err != nil {
		t.Fatal(err)
	}
	if len(srcs) != 1 || srcs[0]["name"] != "nb" {
		t.Fatalf("ListSources = %v", srcs)
	}
	var stateErr *StateError
	if err := s.AddSource("nb", "netbox", nil, ""); !errors.As(err, &stateErr) {
		t.Fatalf("duplicate AddSource error = %v, want StateError", err)
	} else if stateErr.Message != "source 'nb' already exists" {
		t.Fatalf("message = %q", stateErr.Message)
	}
	if err := s.UpsertTarget("leaf1", new("10.0.0.1"), nil, new(""), ""); err != nil {
		t.Fatal(err)
	}
	removed, err := s.RemoveSource("nb")
	if err != nil {
		t.Fatal(err)
	}
	if !removed {
		t.Fatal("RemoveSource = false, want true")
	}
	if srcs, err = s.ListSources(); err != nil {
		t.Fatal(err)
	}
	if len(srcs) != 0 {
		t.Fatalf("sources left: %v", srcs)
	}
	if tgt, err := s.GetTarget("leaf1"); err != nil || tgt == nil {
		t.Fatalf("target lost on source removal: %v %v", tgt, err)
	}
}

func TestSourcePollOutcomes(t *testing.T) {
	s := newState(t)
	if err := s.AddSource("nb", "netbox", nil, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordSourcePoll("nb", nil, new(int64(500))); err != nil {
		t.Fatal(err)
	}
	src, err := s.GetSource("nb")
	if err != nil {
		t.Fatal(err)
	}
	if src["last_poll"] != int64(500) || src["last_error"] != nil {
		t.Fatalf("success poll wrong: %v", src)
	}
	if err := s.RecordSourcePoll("nb", errors.New("boom"), nil); err != nil {
		t.Fatal(err)
	}
	src, err = s.GetSource("nb")
	if err != nil {
		t.Fatal(err)
	}
	if src["last_poll"] != int64(500) || src["last_error"] != "boom" {
		t.Fatalf("error poll wrong: %v", src)
	}
}

func TestSelectionModes(t *testing.T) {
	s := newState(t)
	for _, n := range []string{"undisc", "fresh", "stale"} {
		if err := s.UpsertTarget(n, new("addr-"+n), nil, new(""), ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.EnrichTarget("fresh", Enrichment{Discovered: true, LastDiscoveryRun: 10000}); err != nil {
		t.Fatal(err)
	}
	if err := s.EnrichTarget("stale", Enrichment{Discovered: true, LastDiscoveryRun: 100}); err != nil {
		t.Fatal(err)
	}
	got, err := s.SelectTargets("default", DefaultStaleWindow, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if names := targetNames(t, got); len(names) != 1 || names[0] != "undisc" {
		t.Fatalf("default = %v", names)
	}
	got, err = s.SelectTargets("periodic", 3600, nil, new(int64(10000)))
	if err != nil {
		t.Fatal(err)
	}
	if names := targetNames(t, got); len(names) != 1 || names[0] != "stale" {
		t.Fatalf("periodic = %v", names)
	}
	got, err = s.SelectTargets("force", DefaultStaleWindow, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if names := targetNames(t, got); len(names) != 3 || names[0] != "fresh" || names[1] != "stale" || names[2] != "undisc" {
		t.Fatalf("force = %v", names)
	}
	var stateErr *StateError
	if _, err := s.SelectTargets("bogus", DefaultStaleWindow, nil, nil); !errors.As(err, &stateErr) {
		t.Fatalf("unknown mode error = %v", err)
	} else if stateErr.Message != "unknown selection mode 'bogus'" {
		t.Fatalf("message = %q", stateErr.Message)
	}
	got, err = s.SelectTargets("default", DefaultStaleWindow, new("fresh"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if names := targetNames(t, got); len(names) != 1 || names[0] != "fresh" {
		t.Fatalf("explicit target = %v", names)
	}
}

func TestModuleColumnsCreatedWithDefaultZero(t *testing.T) {
	s := newState(t, "ports", "bgp-peers")
	if err := s.UpsertTarget("leaf1", new("10.0.0.1"), nil, new(""), ""); err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{"ports", "bgp-peers"} {
		v, err := s.ModuleValue("leaf1", m)
		if err != nil {
			t.Fatal(err)
		}
		if v == nil || *v != 0 {
			t.Fatalf("%s = %v, want 0", m, v)
		}
	}
	var v int
	if err := s.db.QueryRow(`SELECT "mod_bgp_peers" FROM targets WHERE name='leaf1'`).Scan(&v); err != nil {
		t.Fatalf("hyphen not mapped to underscore: %v", err)
	}
}

func TestModuleColumnsAdditiveWithoutTouchingRows(t *testing.T) {
	s := newState(t, "ports")
	if err := s.UpsertTarget("leaf1", new("10.0.0.1"), nil, new(""), ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SetModule("leaf1", "ports", 2); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureModuleColumns([]string{"ports", "isis"}); err != nil {
		t.Fatal(err)
	}
	ports, err := s.ModuleValue("leaf1", "ports")
	if err != nil {
		t.Fatal(err)
	}
	isis, err := s.ModuleValue("leaf1", "isis")
	if err != nil {
		t.Fatal(err)
	}
	if ports == nil || *ports != 2 {
		t.Fatalf("ports = %v, want 2", ports)
	}
	if isis == nil || *isis != 0 {
		t.Fatalf("isis = %v, want 0", isis)
	}
}

func TestEnabledModulesThreshold(t *testing.T) {
	s := newState(t, "ports", "bgp-peers", "isis")
	if err := s.UpsertTarget("leaf1", new("10.0.0.1"), nil, new(""), ""); err != nil {
		t.Fatal(err)
	}
	for m, v := range map[string]int{"ports": 1, "bgp-peers": 2, "isis": -1} {
		if err := s.SetModule("leaf1", m, v); err != nil {
			t.Fatal(err)
		}
	}
	row, err := s.GetTarget("leaf1")
	if err != nil {
		t.Fatal(err)
	}
	enabled := Enabled(row, []string{"ports", "bgp-peers", "isis"})
	if len(enabled) != 2 || enabled[0] != "ports" || enabled[1] != "bgp-peers" {
		t.Fatalf("enabled = %v", enabled)
	}
}

func TestReconcilePreservesForcedValues(t *testing.T) {
	s := newState(t, "ports", "bgp-peers", "isis")
	if err := s.UpsertTarget("leaf1", new("10.0.0.1"), nil, new(""), ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SetModule("leaf1", "ports", 2); err != nil {
		t.Fatal(err)
	}
	if err := s.SetModule("leaf1", "bgp-peers", -1); err != nil {
		t.Fatal(err)
	}
	if err := s.ReconcileModules("leaf1", map[string]int{"ports": 0, "bgp-peers": 1, "isis": 1}); err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"ports": 2, "bgp-peers": -1, "isis": 1}
	for m, w := range want {
		got, err := s.ModuleValue("leaf1", m)
		if err != nil {
			t.Fatal(err)
		}
		if got == nil || *got != w {
			t.Fatalf("%s = %v, want %d", m, got, w)
		}
	}
}

func TestShowFiltersUnion(t *testing.T) {
	s := newState(t)
	for _, n := range []string{"undisc", "errored", "ok"} {
		if err := s.UpsertTarget(n, new("addr-"+n), nil, new(""), ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RecordTargetError("errored", errors.New("timeout")); err != nil {
		t.Fatal(err)
	}
	if err := s.EnrichTarget("ok", Enrichment{Discovered: true, LastDiscoveryRun: 10000}); err != nil {
		t.Fatal(err)
	}
	all, err := s.ShowTargets(false, false, false, DefaultStaleWindow, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("all = %v", targetNames(t, all))
	}
	errs, err := s.ShowTargets(true, false, false, DefaultStaleWindow, nil)
	if err != nil {
		t.Fatal(err)
	}
	if names := targetNames(t, errs); len(names) != 1 || names[0] != "errored" {
		t.Fatalf("errors = %v", names)
	}
	union, err := s.ShowTargets(true, true, false, DefaultStaleWindow, nil)
	if err != nil {
		t.Fatal(err)
	}
	if names := targetNames(t, union); len(names) != 2 || names[0] != "errored" || names[1] != "undisc" {
		t.Fatalf("union = %v", names)
	}
}

// catalogModules mirrors catalog/modules.yaml, the module list Python's golden
// DB already has columns for.
var catalogModules = []string{
	"ports", "processors", "mempools", "storage", "sensors", "environment",
	"entity-physical", "transceivers", "services",
	"bgp-peers", "ospf", "ospfv3", "isis", "bfd", "mpls", "route", "vlans",
	"stp", "fdb-table", "arp-table", "ntp", "wireless",
	"discovery-protocols", "mac-accounting", "acl", "forwarding-resources",
}

func TestOpensPythonDBAndAddsModuleColumn(t *testing.T) {
	src, err := os.Open(filepath.Join("..", "..", "testdata", "golden", "python_state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	dst := filepath.Join(t.TempDir(), "state.db")
	dstFile, err := os.Create(dst)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(dstFile, src); err != nil {
		t.Fatal(err)
	}
	if err := dstFile.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(dst, append(append([]string{}, catalogModules...), "brand-new-module"))
	if err != nil {
		t.Fatalf("Open Python DB: %v", err)
	}
	defer s.Close()

	rows, err := s.AllTargets()
	if err != nil {
		t.Fatal(err)
	}
	if names := targetNames(t, rows); len(names) != 2 || names[0] != "leaf1" || names[1] != "spine1" {
		t.Fatalf("targets = %v", names)
	}
	leaf, err := s.GetTarget("leaf1")
	if err != nil {
		t.Fatal(err)
	}
	if leaf["catalog_key"] != "nokia-srlinux" || leaf["discovered"] != int64(1) {
		t.Fatalf("python row not read: %v", leaf)
	}
	if v, err := s.ModuleValue("leaf1", "bgp-peers"); err != nil || v == nil || *v != -1 {
		t.Fatalf("mod_bgp_peers = %v, %v (want -1)", v, err)
	}
	if v, err := s.ModuleValue("leaf1", "brand-new-module"); err != nil || v == nil || *v != 0 {
		t.Fatalf("new module column = %v, %v (want 0)", v, err)
	}
	var typ string
	if err := s.db.QueryRow("SELECT type FROM pragma_table_info('targets') WHERE name='mod_brand_new_module'").Scan(&typ); err != nil {
		t.Fatalf("new column missing: %v", err)
	}
	if typ != "INTEGER" {
		t.Fatalf("new column type = %q", typ)
	}
}

func TestNilInventoryTextIsStoredAsNull(t *testing.T) {
	s := newState(t)
	if err := s.UpsertTarget("inserted", nil, nil, nil, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertTarget("updated", new("10.0.0.1"), nil, new("nokia_srl"), ""); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertTarget("updated", nil, nil, nil, ""); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"inserted", "updated"} {
		tgt, err := s.GetTarget(name)
		if err != nil {
			t.Fatal(err)
		}
		if tgt["address"] != nil || tgt["platform_hint"] != nil {
			t.Errorf("%s: address/platform_hint = %#v/%#v, want NULL",
				name, tgt["address"], tgt["platform_hint"])
		}
	}
}

func TestEmptyInventoryTextIsStoredAsEmpty(t *testing.T) {
	s := newState(t)
	if err := s.UpsertTarget("leaf1", new(""), nil, new(""), ""); err != nil {
		t.Fatal(err)
	}
	tgt, err := s.GetTarget("leaf1")
	if err != nil {
		t.Fatal(err)
	}
	for _, col := range []string{"address", "platform_hint", "source"} {
		if tgt[col] != "" {
			t.Errorf("%s = %#v, want \"\"", col, tgt[col])
		}
	}

	if err := s.EnrichTarget("leaf1", Enrichment{Discovered: true, LastDiscoveryRun: 1}); err != nil {
		t.Fatal(err)
	}
	if tgt, err = s.GetTarget("leaf1"); err != nil {
		t.Fatal(err)
	}
	for _, col := range []string{"catalog_key", "vendor", "os", "model", "os_version", "capabilities_hash"} {
		if tgt[col] != nil {
			t.Errorf("%s = %#v, want NULL", col, tgt[col])
		}
	}

	if err := s.AddSource("nb", "netbox", nil, ""); err != nil {
		t.Fatal(err)
	}
	src, err := s.GetSource("nb")
	if err != nil {
		t.Fatal(err)
	}
	if src["credential_ref"] != nil {
		t.Errorf("credential_ref = %#v, want NULL", src["credential_ref"])
	}
}

func TestConcurrentDuplicateAddSourceReturnsStateError(t *testing.T) {
	s := newState(t)
	for i := 0; i < 50; i++ {
		name := fmt.Sprintf("src-%d", i)
		start := make(chan struct{})
		errs := make(chan error, 2)
		for j := 0; j < 2; j++ {
			go func() {
				<-start
				errs <- s.AddSource(name, "nornir", nil, "")
			}()
		}
		close(start)
		for j := 0; j < 2; j++ {
			err := <-errs
			if err == nil {
				continue
			}
			var stateErr *StateError
			if !errors.As(err, &stateErr) || stateErr.Message != fmt.Sprintf("source '%s' already exists", name) {
				t.Fatalf("duplicate AddSource error = %v, want StateError", err)
			}
		}
	}
}

func TestReconcileModulesRollsBackOnError(t *testing.T) {
	s := newState(t, "bgp-peers")
	if err := s.UpsertTarget("leaf1", new("10.0.0.1"), nil, new(""), ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SetModule("leaf1", "bgp-peers", 1); err != nil {
		t.Fatal(err)
	}
	err := s.ReconcileModules("leaf1", map[string]int{"bgp-peers": 0, "zzz-missing": 1})
	if err == nil {
		t.Fatal("ReconcileModules succeeded, want a missing-column error")
	}
	v, err := s.ModuleValue("leaf1", "bgp-peers")
	if err != nil {
		t.Fatal(err)
	}
	if v == nil || *v != 1 {
		t.Fatalf("bgp-peers = %v, want 1 (first update must roll back)", v)
	}
}
