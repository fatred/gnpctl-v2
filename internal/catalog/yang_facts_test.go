package catalog

import (
	"slices"
	"strings"
	"testing"
)

const yangModules = `modules:
  ports: on
`

const yangNokiaVendor = `vendor: nokia
fingerprint:
  match:
    org_contains: ["Nokia"]
`

const yangSrlinuxCommon = `key: nokia-srlinux
vendor: nokia
platform_match:
  models_any: ["srl_nokia"]
modules:
  ports:
    paths:
      - {key: if, path: /interfaces/interface/state, mode: sample}
`

const yangV2410 = `oc-bgp-neighbor-session-state:
  model: openconfig-bgp-neighbor
  path: /network-instances/network-instance/protocols/protocol/bgp/neighbors/neighbor/state/session-state
  type: enumeration
  enum: [IDLE, CONNECT, ACTIVE, OPENSENT, OPENCONFIRM, ESTABLISHED]
srl-cpu-utilization:
  model: srl_nokia-platform-cpu
  path: /platform/control/cpu/total/instant
  type: percentage
  units: percent
`

const yangV2503 = `oc-bgp-neighbor-session-state:
  model: openconfig-bgp-neighbor
  path: /network-instances/network-instance/protocols/protocol/bgp/neighbors/neighbor/state/session-state
  type: enumeration
  enum: [IDLE, CONNECT, ACTIVE, OPENSENT, OPENCONFIRM, ESTABLISHED, DELETED]
srl-cpu-utilization:
  model: srl_nokia-platform-cpu
  path: /platform/control/cpu/total/instant
  type: percentage
  units: percent
`

func yangCatalog(t *testing.T, extra map[string]string) *LayeredCatalog {
	t.Helper()
	files := map[string]string{
		"modules.yaml":              yangModules,
		"nokia/vendor.yaml":         yangNokiaVendor,
		"nokia/srlinux/common.yaml": yangSrlinuxCommon,
		"yang/nokia/v24.10.yaml":    yangV2410,
		"yang/nokia/v25.3.yaml":     yangV2503,
	}
	for rel, text := range extra {
		files[rel] = text
	}
	return mustLoad(t, writeTree(t, files))
}

func TestYangFactsLoadAndResolveByID(t *testing.T) {
	cat := yangCatalog(t, nil)
	fact := cat.Yang().Fact("nokia", "v24.10", "oc-bgp-neighbor-session-state")
	if fact == nil || fact.Type != "enumeration" {
		t.Fatalf("fact = %+v", fact)
	}
	if len(fact.Enum) != 6 || fact.Enum[5] != "ESTABLISHED" {
		t.Fatalf("enum = %v", fact.Enum)
	}
}

func TestYangFactFieldsExposed(t *testing.T) {
	cat := yangCatalog(t, nil)
	cpu := cat.Yang().Fact("nokia", "v24.10", "srl-cpu-utilization")
	if cpu.Model != "srl_nokia-platform-cpu" ||
		!strings.HasSuffix(cpu.Path, "/total/instant") ||
		cpu.Type != "percentage" ||
		cpu.Units == nil || *cpu.Units != "percent" ||
		len(cpu.Enum) != 0 {
		t.Fatalf("cpu = %+v", cpu)
	}
}

func TestReleasesSortedByVersion(t *testing.T) {
	cat := yangCatalog(t, nil)
	got := cat.Yang().Releases("nokia")
	if len(got) != 2 || got[0] != "v24.10" || got[1] != "v25.3" {
		t.Fatalf("releases = %v", got)
	}
}

func TestSelectionNearestBelow(t *testing.T) {
	cat := yangCatalog(t, nil)
	version := "v24.10.4-244-g5c0afd1a61"
	sel := cat.Yang().SelectRelease("nokia", &version)
	if sel.Release == nil || *sel.Release != "v24.10" || sel.Warning != nil {
		t.Fatalf("selection = %+v", sel)
	}
	version = "v25.3.1"
	sel = cat.Yang().SelectRelease("nokia", &version)
	if sel.Release == nil || *sel.Release != "v25.3" || sel.Warning != nil {
		t.Fatalf("selection = %+v", sel)
	}
}

func TestSelectionBetweenReleasesPicksLower(t *testing.T) {
	cat := yangCatalog(t, nil)
	version := "v25.1.2"
	sel := cat.Yang().SelectRelease("nokia", &version)
	if sel.Release == nil || *sel.Release != "v24.10" || sel.Warning != nil {
		t.Fatalf("selection = %+v", sel)
	}
}

func TestDeviceNewerThanPinnedWarns(t *testing.T) {
	cat := yangCatalog(t, nil)
	version := "v26.1.0"
	sel := cat.Yang().SelectRelease("nokia", &version)
	if sel.Release == nil || *sel.Release != "v25.3" {
		t.Fatalf("selection = %+v", sel)
	}
	if sel.Warning == nil || !strings.Contains(strings.ToLower(*sel.Warning), "newer") {
		t.Fatalf("warning = %v", sel.Warning)
	}
}

func TestDeviceOlderThanPinnedWarns(t *testing.T) {
	cat := yangCatalog(t, nil)
	version := "v23.1.0"
	sel := cat.Yang().SelectRelease("nokia", &version)
	if sel.Release == nil || *sel.Release != "v24.10" {
		t.Fatalf("selection = %+v", sel)
	}
	if sel.Warning == nil || !strings.Contains(strings.ToLower(*sel.Warning), "older") {
		t.Fatalf("warning = %v", sel.Warning)
	}
}

func TestResolveUsesSelectedRelease(t *testing.T) {
	cat := yangCatalog(t, nil)
	v25, v24 := "v25.3.1", "v24.10.4"
	f25 := cat.Yang().Fact("nokia", *cat.Yang().SelectRelease("nokia", &v25).Release, "oc-bgp-neighbor-session-state")
	if !slices.Contains(f25.Enum, "DELETED") {
		t.Fatalf("enum = %v", f25.Enum)
	}
	f24 := cat.Yang().Fact("nokia", *cat.Yang().SelectRelease("nokia", &v24).Release, "oc-bgp-neighbor-session-state")
	if slices.Contains(f24.Enum, "DELETED") {
		t.Fatalf("enum = %v", f24.Enum)
	}
}

func TestAbsentYangDirIsEmptyRegistry(t *testing.T) {
	cat := mustLoad(t, writeTree(t, map[string]string{
		"modules.yaml":              yangModules,
		"nokia/vendor.yaml":         yangNokiaVendor,
		"nokia/srlinux/common.yaml": yangSrlinuxCommon,
	}))
	if rels := cat.Yang().Releases("nokia"); len(rels) != 0 {
		t.Fatalf("releases = %v", rels)
	}
	version := "v24.10"
	if sel := cat.Yang().SelectRelease("nokia", &version); sel.Release != nil {
		t.Fatalf("selection = %+v", sel)
	}
}

func TestNonMappingYangEntryRejected(t *testing.T) {
	bad := "oc-bad: not-a-mapping\n"
	_, err := Load(writeTree(t, map[string]string{
		"modules.yaml":              yangModules,
		"nokia/vendor.yaml":         yangNokiaVendor,
		"nokia/srlinux/common.yaml": yangSrlinuxCommon,
		"yang/nokia/v24.10.yaml":    bad,
		"yang/nokia/v25.3.yaml":     yangV2503,
	}))
	if err == nil || !strings.Contains(err.Error(), "v24.10") {
		t.Fatalf("error = %v", err)
	}
}

func TestReleaseVersionTieIsDeterministic(t *testing.T) {
	cat := mustLoad(t, writeTree(t, map[string]string{
		"modules.yaml":              yangModules,
		"nokia/vendor.yaml":         yangNokiaVendor,
		"nokia/srlinux/common.yaml": yangSrlinuxCommon,
		"yang/nokia/4.32.0F.yaml":   yangV2410,
		"yang/nokia/4.32.0M.yaml":   yangV2503,
	}))
	version := "4.33.1F"
	for i := 0; i < 100; i++ {
		sel := cat.Yang().SelectRelease("nokia", &version)
		if sel.Release == nil || *sel.Release != "4.32.0M" {
			t.Fatalf("run %d: selection = %+v, want 4.32.0M", i, sel)
		}
	}
}
