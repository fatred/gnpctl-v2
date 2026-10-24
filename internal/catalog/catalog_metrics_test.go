package catalog

import (
	"strings"
	"testing"
)

const metricsModules = `modules:
  processors: on
  sensors: on
  bgp-peers: on
`

const metricsNokiaVendor = `vendor: nokia
fingerprint:
  match:
    org_contains: ["Nokia"]
`

const metricsYang2410 = `n-cpu:
  model: srl_nokia-platform-cpu
  path: /platform/control/cpu/total/instant
  type: uint8
n-oper-state:
  model: srl_nokia-common
  path: /platform/power-supply/oper-state
  type: enumeration
  enum: [up, down, empty]
n-bgp-state:
  model: srl_nokia-bgp
  path: /network-instance/protocols/bgp/neighbor/session-state
  type: enumeration
  enum: [idle, connect, active, opensent, openconfirm, established]
`

const metricsSrlinuxCommon = `key: nokia-srlinux
vendor: nokia
platform_match:
  models_any: ["srl_nokia"]
modules:
  processors:
    metrics:
      cpu_utilization: {type: numeric, yang: n-cpu}
    paths:
      - {key: cpu, path: /platform/control/cpu/total/instant, mode: sample, yang: n-cpu}
  sensors:
    metrics:
      power_supply_state:
        type: state
        yang: n-oper-state
        map: {up: 1, down: 0, empty: -1}
        required_labels: [oper_state]
    paths:
      - {key: psu, path: /platform/power-supply/oper-state, mode: sample, yang: n-oper-state}
  bgp-peers:
    metrics:
      bgp_neighbor_session_state:
        type: state
        yang: n-bgp-state
        map: {idle: 1, connect: 2, active: 3, opensent: 4, openconfirm: 5, established: 6}
      bgp_neighbor_peer_group: {type: info, required_labels: [peer_group]}
    paths:
      - {key: bgp, path: /network-instance/protocols/bgp/neighbor, mode: sample}
`

func metricsCatalog(t *testing.T, extra map[string]string) *LayeredCatalog {
	t.Helper()
	files := map[string]string{
		"modules.yaml":              metricsModules,
		"nokia/vendor.yaml":         metricsNokiaVendor,
		"nokia/srlinux/common.yaml": metricsSrlinuxCommon,
		"yang/nokia/v24.10.yaml":    metricsYang2410,
	}
	for rel, text := range extra {
		files[rel] = text
	}
	return mustLoad(t, writeTree(t, files))
}

func TestNumericMetricResolvedWithFact(t *testing.T) {
	version := "v24.10.1"
	eff, err := metricsCatalog(t, nil).EffectiveProfile("nokia-srlinux", &version)
	if err != nil {
		t.Fatal(err)
	}
	processors, _ := eff.Modules.Get("processors")
	m := processors.Metrics["cpu_utilization"]
	if m == nil || m.Type != "numeric" || m.Yang == nil || *m.Yang != "n-cpu" {
		t.Fatalf("metric = %+v", m)
	}
	if m.Fact == nil || m.Fact.Type != "uint8" {
		t.Fatalf("fact = %+v", m.Fact)
	}
}

func TestStateMetricExposesMapAndEnum(t *testing.T) {
	version := "v24.10.1"
	eff, err := metricsCatalog(t, nil).EffectiveProfile("nokia-srlinux", &version)
	if err != nil {
		t.Fatal(err)
	}
	bgp, _ := eff.Modules.Get("bgp-peers")
	m := bgp.Metrics["bgp_neighbor_session_state"]
	if m == nil || m.Type != "state" || m.Map["established"] != 6 {
		t.Fatalf("metric = %+v", m)
	}
	want := []string{"idle", "connect", "active", "opensent", "openconfirm", "established"}
	if strings.Join(m.Fact.Enum, ",") != strings.Join(want, ",") {
		t.Fatalf("enum = %v", m.Fact.Enum)
	}
}

func TestInfoMetricNeedsNoYang(t *testing.T) {
	version := "v24.10.1"
	eff, err := metricsCatalog(t, nil).EffectiveProfile("nokia-srlinux", &version)
	if err != nil {
		t.Fatal(err)
	}
	bgp, _ := eff.Modules.Get("bgp-peers")
	m := bgp.Metrics["bgp_neighbor_peer_group"]
	if m == nil || m.Type != "info" {
		t.Fatalf("metric = %+v", m)
	}
	if len(m.RequiredLabels) != 1 || m.RequiredLabels[0] != "peer_group" {
		t.Fatalf("labels = %v", m.RequiredLabels)
	}
	if m.Yang != nil || m.Fact != nil {
		t.Fatalf("info metric must not carry yang facts: %+v", m)
	}
}

func TestPathYangProvenanceExposed(t *testing.T) {
	version := "v24.10.1"
	eff, err := metricsCatalog(t, nil).EffectiveProfile("nokia-srlinux", &version)
	if err != nil {
		t.Fatal(err)
	}
	processors, _ := eff.Modules.Get("processors")
	if got := processors.Paths[0].Yang; got == nil || *got != "n-cpu" {
		t.Fatalf("path yang = %v", got)
	}
}

func TestModuleWithoutMetricsIsEmpty(t *testing.T) {
	common := metricsSrlinuxCommon + "  ports:\n    paths:\n      - {key: if, path: /interface, mode: sample}\n"
	cat := metricsCatalog(t, map[string]string{
		"modules.yaml":              metricsModules + "  ports: on\n",
		"nokia/srlinux/common.yaml": common,
	})
	version := "v24.10.1"
	eff, err := cat.EffectiveProfile("nokia-srlinux", &version)
	if err != nil {
		t.Fatal(err)
	}
	ports, _ := eff.Modules.Get("ports")
	if len(ports.Metrics) != 0 {
		t.Fatalf("metrics = %v", ports.Metrics)
	}
}

func TestUnknownMetricYangRefRejected(t *testing.T) {
	bad := strings.Replace(metricsSrlinuxCommon,
		"cpu_utilization: {type: numeric, yang: n-cpu}",
		"cpu_utilization: {type: numeric, yang: no-such-id}", 1)
	_, err := Load(writeTree(t, map[string]string{
		"modules.yaml":              metricsModules,
		"nokia/vendor.yaml":         metricsNokiaVendor,
		"nokia/srlinux/common.yaml": bad,
		"yang/nokia/v24.10.yaml":    metricsYang2410,
	}))
	if err == nil {
		t.Fatal("Load succeeded, want error")
	}
	if !strings.Contains(err.Error(), "no-such-id") || !strings.Contains(err.Error(), "cpu_utilization") {
		t.Fatalf("error = %q", err)
	}
}

func TestUnknownPathYangRefRejected(t *testing.T) {
	bad := strings.Replace(metricsSrlinuxCommon,
		"path: /platform/control/cpu/total/instant, mode: sample, yang: n-cpu}",
		"path: /platform/control/cpu/total/instant, mode: sample, yang: bogus-id}", 1)
	_, err := Load(writeTree(t, map[string]string{
		"modules.yaml":              metricsModules,
		"nokia/vendor.yaml":         metricsNokiaVendor,
		"nokia/srlinux/common.yaml": bad,
		"yang/nokia/v24.10.yaml":    metricsYang2410,
	}))
	if err == nil || !strings.Contains(err.Error(), "bogus-id") {
		t.Fatalf("error = %v", err)
	}
}

func TestInvalidMetricTypeRejected(t *testing.T) {
	bad := strings.Replace(metricsSrlinuxCommon,
		"cpu_utilization: {type: numeric, yang: n-cpu}",
		"cpu_utilization: {type: gauge, yang: n-cpu}", 1)
	if _, err := Load(writeTree(t, map[string]string{
		"modules.yaml":              metricsModules,
		"nokia/vendor.yaml":         metricsNokiaVendor,
		"nokia/srlinux/common.yaml": bad,
		"yang/nokia/v24.10.yaml":    metricsYang2410,
	})); err == nil {
		t.Fatal("Load succeeded, want error")
	}
}

func TestNumericMetricRequiresYang(t *testing.T) {
	bad := strings.Replace(metricsSrlinuxCommon,
		"cpu_utilization: {type: numeric, yang: n-cpu}",
		"cpu_utilization: {type: numeric}", 1)
	if _, err := Load(writeTree(t, map[string]string{
		"modules.yaml":              metricsModules,
		"nokia/vendor.yaml":         metricsNokiaVendor,
		"nokia/srlinux/common.yaml": bad,
		"yang/nokia/v24.10.yaml":    metricsYang2410,
	})); err == nil {
		t.Fatal("Load succeeded, want error")
	}
}

func TestVersionLayerOverridesMetricMap(t *testing.T) {
	layer := `version_match: ["v25.*"]
order: 10
modules:
  bgp-peers:
    metrics:
      bgp_neighbor_session_state:
        type: state
        yang: n-bgp-state
        map: {idle: 1, connect: 2, active: 3, opensent: 4, openconfirm: 5, established: 9}
`
	cat := metricsCatalog(t, map[string]string{"nokia/srlinux/25.3.yaml": layer})
	base := "v24.10.1"
	over := "v25.3.1"
	baseEff, err := cat.EffectiveProfile("nokia-srlinux", &base)
	if err != nil {
		t.Fatal(err)
	}
	overEff, err := cat.EffectiveProfile("nokia-srlinux", &over)
	if err != nil {
		t.Fatal(err)
	}
	baseBGP, _ := baseEff.Modules.Get("bgp-peers")
	overBGP, _ := overEff.Modules.Get("bgp-peers")
	if got := baseBGP.Metrics["bgp_neighbor_session_state"].Map["established"]; got != 6 {
		t.Fatalf("base map = %d", got)
	}
	if got := overBGP.Metrics["bgp_neighbor_session_state"].Map["established"]; got != 9 {
		t.Fatalf("layer map = %d", got)
	}
}
