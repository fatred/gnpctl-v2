package pipeline

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fatred/gnpctl-v2/internal/catalog"
	"github.com/fatred/gnpctl-v2/internal/state"
	"github.com/fatred/gnpctl-v2/internal/transport"
)

var nokiaCaps = map[string]any{
	"supported_models": []map[string]any{
		{"name": "srl_nokia-system", "organization": "Nokia"},
	},
	"supported_encodings": []any{"json_ietf", "proto"},
	"gnmi_version":        "0.10.0",
}

var nokiaSubs = map[string]map[string]any{
	"/system/information": {
		"srl_nokia-system:system/srl_nokia-system-info:information/version":     "v24.10.4-244-g5c0afd1a61",
		"srl_nokia-system:system/srl_nokia-system-info:information/description": "SRLinux-v24.10.4-244-g5c0afd1a61 7220 IXR-D2L Copyright (c) 2000-2025 Nokia.",
	},
}

var aristaCaps = map[string]any{
	"supported_models": []map[string]any{
		{"name": "openconfig-platform", "organization": "Arista Networks"},
	},
	"supported_encodings": []any{"json_ietf"},
	"gnmi_version":        "0.10.0",
}

var aristaSubs = map[string]map[string]any{
	"eos_native:/Eos/image": {
		"eos_native:Eos/image/version": "4.33.4M",
		"eos_native:Eos/image/flavor":  "cEOS",
	},
}

func testCatalog(t *testing.T) *catalog.LayeredCatalog {
	t.Helper()
	cat, err := catalog.Load(filepath.Join("..", "..", "catalog"))
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

type connection struct {
	name string
	port int
}

func fixedTransport(tr transport.GnmiTransport, captured *[]connection) TransportFactory {
	return func(target map[string]any, port int) (transport.GnmiTransport, error) {
		if captured != nil {
			name, _ := target["name"].(string)
			*captured = append(*captured, connection{name, port})
		}
		return tr, nil
	}
}

func targetRow(t *testing.T, s *state.State, name string) map[string]any {
	t.Helper()
	row, err := s.GetTarget(name)
	if err != nil {
		t.Fatalf("GetTarget(%s): %v", name, err)
	}
	if row == nil {
		t.Fatalf("target %s missing", name)
	}
	return row
}

func rowInt(t *testing.T, row map[string]any, key string) int64 {
	t.Helper()
	v, ok := row[key].(int64)
	if !ok {
		t.Fatalf("%s: want int64, got %T (%v)", key, row[key], row[key])
	}
	return v
}

func rowStr(t *testing.T, row map[string]any, key string) string {
	t.Helper()
	s, _ := row[key].(string)
	return s
}

func moduleValue(t *testing.T, s *state.State, target, name string) int {
	t.Helper()
	v, err := s.ModuleValue(target, name)
	if err != nil {
		t.Fatalf("ModuleValue: %v", err)
	}
	if v == nil {
		return 0
	}
	return *v
}

func TestMatchedTargetEnriched(t *testing.T) {
	cat := testCatalog(t)
	s := openTestState(t)
	port := 57400
	if err := s.UpsertTarget("leaf1", new("10.0.0.1"), &port, new("nokia_srl"), ""); err != nil {
		t.Fatal(err)
	}
	tr := transport.NewFixtureTransport(nokiaCaps, nokiaSubs)
	outcome, err := DiscoverTarget(targetRow(t, s, "leaf1"), cat, s, fixedTransport(tr, nil), new(int64(1000)), nil)
	if err != nil {
		t.Fatalf("DiscoverTarget: %v", err)
	}
	if outcome.Status != StatusMatched {
		t.Fatalf("status = %q, want matched", outcome.Status)
	}
	target := targetRow(t, s, "leaf1")
	if got := rowInt(t, target, "discovered"); got != 1 {
		t.Fatalf("discovered = %d", got)
	}
	if got := rowInt(t, target, "last_discovery_run"); got != 1000 {
		t.Fatalf("last_discovery_run = %d", got)
	}
	if got := rowStr(t, target, "catalog_key"); got != "nokia-srlinux" {
		t.Fatalf("catalog_key = %q", got)
	}
	if got := rowStr(t, target, "vendor"); got != "nokia" {
		t.Fatalf("vendor = %q", got)
	}
	if got := rowStr(t, target, "model"); got != "7220 IXR-D2L" {
		t.Fatalf("model = %q", got)
	}
	if got := rowStr(t, target, "os_version"); got != "v24.10.4-244-g5c0afd1a61" {
		t.Fatalf("os_version = %q", got)
	}
	if target["last_error"] != nil {
		t.Fatalf("last_error = %v, want nil", target["last_error"])
	}
}

func TestUnknownTargetMarkedDiscoveredNullKey(t *testing.T) {
	cat := testCatalog(t)
	s := openTestState(t)
	port := 57400
	if err := s.UpsertTarget("mystery", new("10.0.0.9"), &port, new(""), ""); err != nil {
		t.Fatal(err)
	}
	tr := transport.NewFixtureTransport(map[string]any{
		"supported_models": []map[string]any{{"name": "x", "organization": "Acme"}},
	}, nil)
	outcome, err := DiscoverTarget(targetRow(t, s, "mystery"), cat, s, fixedTransport(tr, nil), new(int64(5)), nil)
	if err != nil {
		t.Fatalf("DiscoverTarget: %v", err)
	}
	if outcome.Status != StatusUnknown {
		t.Fatalf("status = %q, want unknown", outcome.Status)
	}
	target := targetRow(t, s, "mystery")
	if rowInt(t, target, "discovered") != 1 || target["catalog_key"] != nil {
		t.Fatalf("discovered/key = %v/%v", target["discovered"], target["catalog_key"])
	}
	if got := rowInt(t, target, "last_discovery_run"); got != 5 {
		t.Fatalf("last_discovery_run = %d", got)
	}
	// No profile: every registry module is reconciled to off.
	if got := moduleValue(t, s, "mystery", "ports"); got != 0 {
		t.Fatalf("ports = %d, want 0", got)
	}
}

func TestUnreachableTargetRecordsError(t *testing.T) {
	cat := testCatalog(t)
	s := openTestState(t)
	port := 57400
	if err := s.UpsertTarget("dead", new("10.0.0.254"), &port, new(""), ""); err != nil {
		t.Fatal(err)
	}
	boom := func(map[string]any, int) (transport.GnmiTransport, error) {
		return nil, errors.New("connection refused")
	}
	outcome, err := DiscoverTarget(targetRow(t, s, "dead"), cat, s, boom, new(int64(9)), nil)
	if err != nil {
		t.Fatalf("DiscoverTarget: %v", err)
	}
	if outcome.Status != StatusError {
		t.Fatalf("status = %q, want error", outcome.Status)
	}
	target := targetRow(t, s, "dead")
	if rowInt(t, target, "discovered") != 0 || rowInt(t, target, "last_discovery_run") != 0 {
		t.Fatalf("discovery state touched: %v", target)
	}
	lastError, _ := target["last_error"].(string)
	if !strings.Contains(lastError, "connection refused") {
		t.Fatalf("last_error = %q", lastError)
	}
}

func TestPortResolutionExplicitVendorDefaultGlobal(t *testing.T) {
	cat := testCatalog(t)
	if got := ResolvePort(map[string]any{"port": int64(12345), "platform_hint": "nokia_srl"}, cat); got != 12345 {
		t.Fatalf("explicit port = %d", got)
	}
	if got := ResolvePort(map[string]any{"port": nil, "platform_hint": "arista_eos"}, cat); got != 6030 {
		t.Fatalf("vendor default = %d", got)
	}
	if got := ResolvePort(map[string]any{"port": nil, "platform_hint": "unknownos"}, cat); got != transport.DefaultGNMIPort {
		t.Fatalf("global default = %d", got)
	}
}

func TestPortPersistedFromVendorDefault(t *testing.T) {
	cat := testCatalog(t)
	s := openTestState(t)
	if err := s.UpsertTarget("spine1", new("10.0.0.5"), nil, new("arista_eos"), ""); err != nil {
		t.Fatal(err)
	}
	var captured []connection
	tr := transport.NewFixtureTransport(aristaCaps, aristaSubs)
	if _, err := DiscoverTarget(targetRow(t, s, "spine1"), cat, s, fixedTransport(tr, &captured), new(int64(1)), nil); err != nil {
		t.Fatalf("DiscoverTarget: %v", err)
	}
	if len(captured) != 1 || captured[0] != (connection{"spine1", 6030}) {
		t.Fatalf("captured = %+v", captured)
	}
	if got := rowInt(t, targetRow(t, s, "spine1"), "port"); got != 6030 {
		t.Fatalf("port = %d", got)
	}
}

func TestDiscoveryReconcilesModuleColumns(t *testing.T) {
	cat := testCatalog(t)
	s := openTestState(t)
	port := 57400
	if err := s.UpsertTarget("leaf1", new("10.0.0.1"), &port, new("nokia_srl"), ""); err != nil {
		t.Fatal(err)
	}
	tr := transport.NewFixtureTransport(nokiaCaps, nokiaSubs)
	if _, err := DiscoverTarget(targetRow(t, s, "leaf1"), cat, s, fixedTransport(tr, nil), new(int64(1)), nil); err != nil {
		t.Fatalf("DiscoverTarget: %v", err)
	}
	for name, want := range map[string]int{"ports": 1, "bgp-peers": 1, "isis": 0, "acl": 0} {
		if got := moduleValue(t, s, "leaf1", name); got != want {
			t.Fatalf("%s = %d, want %d", name, got, want)
		}
	}

	// Operator force-on isis; re-discovery must preserve the forced value.
	if err := s.SetModule("leaf1", "isis", 2); err != nil {
		t.Fatal(err)
	}
	if _, err := DiscoverTarget(targetRow(t, s, "leaf1"), cat, s, fixedTransport(tr, nil), new(int64(2)), nil); err != nil {
		t.Fatalf("DiscoverTarget: %v", err)
	}
	if got := moduleValue(t, s, "leaf1", "isis"); got != 2 {
		t.Fatalf("isis = %d, want 2", got)
	}
	if got := moduleValue(t, s, "leaf1", "ports"); got != 1 {
		t.Fatalf("ports = %d, want 1", got)
	}
}

func TestRunDiscoveryCounts(t *testing.T) {
	cat := testCatalog(t)
	s := openTestState(t)
	port := 57400
	for _, name := range []string{"leaf1", "leaf2"} {
		if err := s.UpsertTarget(name, new("1"), &port, new("nokia_srl"), ""); err != nil {
			t.Fatal(err)
		}
	}
	tr := transport.NewFixtureTransport(nokiaCaps, nokiaSubs)
	counts, err := RunDiscovery(s, cat, "default", state.DefaultStaleWindow, nil, fixedTransport(tr, nil), new(int64(1)), nil)
	if err != nil {
		t.Fatalf("RunDiscovery: %v", err)
	}
	if counts[StatusMatched] != 2 || counts[StatusError] != 0 {
		t.Fatalf("counts = %v", counts)
	}
}

func TestDefaultTransportFactoryRequiresCredentials(t *testing.T) {
	t.Setenv("TARGET_USER", "")
	t.Setenv("TARGET_PASS", "")
	if _, err := DefaultTransportFactory(map[string]any{"address": "10.0.0.1"}, 57400); err == nil ||
		!strings.Contains(err.Error(), "gNMI credentials not set") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunDiscoveryContinuesAfterTargetError(t *testing.T) {
	cat := testCatalog(t)
	s := openTestState(t)
	port := 57400
	for _, name := range []string{"dead", "leaf1"} {
		if err := s.UpsertTarget(name, new("1"), &port, new("nokia_srl"), ""); err != nil {
			t.Fatal(err)
		}
	}
	good := transport.NewFixtureTransport(nokiaCaps, nokiaSubs)
	factory := func(target map[string]any, _ int) (transport.GnmiTransport, error) {
		if target["name"] == "dead" {
			return nil, errors.New("connection refused")
		}
		return good, nil
	}
	counts, err := RunDiscovery(s, cat, "default", state.DefaultStaleWindow, nil, factory, new(int64(1)), nil)
	if err != nil {
		t.Fatalf("RunDiscovery: %v", err)
	}
	if counts[StatusMatched] != 1 || counts[StatusError] != 1 || counts[StatusUnknown] != 0 {
		t.Fatalf("counts = %v", counts)
	}
	if rowStr(t, targetRow(t, s, "leaf1"), "catalog_key") != "nokia-srlinux" {
		t.Fatal("leaf1 was not discovered after dead target failed")
	}
}
