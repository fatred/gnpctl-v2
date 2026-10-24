package catalog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const treeModules = `modules:
  ports: on
  processors: on
  storage: on
  bgp-peers: off
  isis: off
`

const nokiaVendor = `vendor: nokia
fingerprint:
  priority: 100
  match:
    org_contains: ["Nokia"]
platform_probe:
  path: /system/information
  extract:
    os_version: version
  parse:
    - from: description
      pattern: '^(?P<os>\S+?)-\S+\s+(?P<model>.+?)\s+Copyright'
`

const srlinuxCommon = `key: nokia-srlinux
vendor: nokia
platform_match:
  models_any: ["srl_nokia"]
default_port: 57400
encoding: proto
platform_hints: ["nokia_srl"]
modules:
  ports:
    paths:
      - {key: if-counters, path: /interfaces/interface/state/counters, mode: sample, sample_interval: 30s}
  bgp-peers:
    default: on
    paths:
      - {key: bgp, path: /network-instance/bgp/neighbor, mode: sample}
`

const srosCommon = `key: nokia-sros
vendor: nokia
platform_match:
  models_any: ["nokia-state"]
modules:
  ports:
    paths:
      - {key: if, path: /state/port, mode: sample}
`

const srlinux2410 = `version_match: ["v24.10*"]
order: 10
modules:
  processors:
    paths:
      - {key: cpu, path: /platform/control/cpu, mode: sample, sample_interval: 15s}
  ports:
    paths:
      - {key: if-counters, path: /interfaces/interface/state/counters-v2, mode: sample, sample_interval: 10s}
      - {key: if-errors, path: /interfaces/interface/state/errors, mode: sample}
`

const juniperVendor = `vendor: juniper
fingerprint:
  priority: 100
  match:
    org_contains: ["Juniper"]
platform_probe:
  path: /components/component[name=*]/state
  extract:
    os_version: {where: {type: CONTROLLER_CARD}, leaf: software-version}
  parse:
    - where: {type: CONTROLLER_CARD}
      from: description
      pattern: '^(?:RE-)?(?P<model>.+)$'
`

const juniperMX = `key: juniper-mx
vendor: juniper
platform_match:
  model: ["MX*", "QFX10*"]
default_port: 57400
encoding: proto
modules:
  ports:
    paths:
      - {key: if, path: /interfaces/interface/state, mode: sample}
`

const juniperQFX = `key: juniper-qfx
vendor: juniper
platform_match:
  model: ["QFX5*"]
modules:
  ports:
    paths:
      - {key: if, path: /interfaces/interface/state, mode: sample}
`

const processorsYAML = `add-device-labels:
  event-add-tag:
    overwrite: false
    add:
      source: gnpctl
`

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "catalog")
	for rel, text := range files {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func fullTree(t *testing.T, extra map[string]string) string {
	t.Helper()
	files := map[string]string{
		"modules.yaml":              treeModules,
		"nokia/vendor.yaml":         nokiaVendor,
		"nokia/srlinux/common.yaml": srlinuxCommon,
		"nokia/srlinux/24.10.yaml":  srlinux2410,
		"nokia/sros/common.yaml":    srosCommon,
		"juniper/vendor.yaml":       juniperVendor,
		"juniper/mx/common.yaml":    juniperMX,
		"juniper/qfx/common.yaml":   juniperQFX,
	}
	for rel, text := range extra {
		files[rel] = text
	}
	return writeTree(t, files)
}

func mustLoad(t *testing.T, dir string) *LayeredCatalog {
	t.Helper()
	cat, err := Load(dir)
	if err != nil {
		t.Fatalf("Load(%s): %v", dir, err)
	}
	return cat
}

func wantLoadError(t *testing.T, dir string, want ...string) {
	t.Helper()
	_, err := Load(dir)
	if err == nil {
		t.Fatal("Load succeeded, want error")
	}
	for _, sub := range want {
		if !strings.Contains(err.Error(), sub) {
			t.Errorf("error %q does not contain %q", err, sub)
		}
	}
}

func variantKeys(variants []*Variant) map[string]bool {
	out := map[string]bool{}
	for _, v := range variants {
		out[v.Key] = true
	}
	return out
}

func TestTreeLoadsVendorsVariantsLayers(t *testing.T) {
	cat := mustLoad(t, fullTree(t, nil))
	vendors := map[string]bool{}
	for _, v := range cat.Vendors() {
		vendors[v.Vendor] = true
	}
	if !vendors["nokia"] || !vendors["juniper"] || len(vendors) != 2 {
		t.Fatalf("vendors = %v", vendors)
	}
	srlinux := cat.Variant("nokia-srlinux")
	if srlinux == nil || *srlinux.DefaultPort != 57400 || *srlinux.Encoding != "proto" {
		t.Fatalf("variant = %+v", srlinux)
	}
	keys := variantKeys(cat.VariantsFor("nokia"))
	if !keys["nokia-srlinux"] || !keys["nokia-sros"] || len(keys) != 2 {
		t.Fatalf("nokia variants = %v", keys)
	}
}

func TestVendorProbeParsed(t *testing.T) {
	cat := mustLoad(t, fullTree(t, nil))
	nokia := cat.Vendor("nokia")
	if got := nokia.Fingerprint.Match.OrgContains; len(got) != 1 || got[0] != "Nokia" {
		t.Fatalf("org_contains = %v", got)
	}
	if nokia.PlatformProbe.Path != "/system/information" {
		t.Fatalf("probe path = %q", nokia.PlatformProbe.Path)
	}
	if got := nokia.PlatformProbe.Extract["os_version"]; got.Leaf != "version" || got.Where != nil {
		t.Fatalf("extract = %+v", got)
	}
	if nokia.PlatformProbe.Parse[0].From != "description" {
		t.Fatalf("parse from = %q", nokia.PlatformProbe.Parse[0].From)
	}
	jp := cat.Vendor("juniper").PlatformProbe
	loc := jp.Extract["os_version"]
	if loc.Where["type"] != "CONTROLLER_CARD" || loc.Leaf != "software-version" {
		t.Fatalf("juniper extract = %+v", loc)
	}
	if jp.Parse[0].Where["type"] != "CONTROLLER_CARD" {
		t.Fatalf("juniper parse where = %v", jp.Parse[0].Where)
	}
}

func TestRegistryNamesAndDefaults(t *testing.T) {
	cat := mustLoad(t, fullTree(t, nil))
	reg := cat.Modules()
	if !reg.DefaultFor("ports") || reg.DefaultFor("bgp-peers") || !reg.Contains("isis") {
		t.Fatalf("registry defaults wrong")
	}
	if names := reg.Names(); len(names) != 5 || names[0] != "ports" || names[4] != "isis" {
		t.Fatalf("names = %v", names)
	}
}

func TestUnknownModuleInCommonRejected(t *testing.T) {
	bad := strings.Replace(srlinuxCommon, "  ports:", "  not-a-module:\n    paths: []\n  ports:", 1)
	wantLoadError(t, fullTree(t, map[string]string{"nokia/srlinux/common.yaml": bad}),
		"not-a-module", "common.yaml")
}

func TestUnknownModuleInVersionLayerRejected(t *testing.T) {
	bad := strings.Replace(srlinux2410, "  processors:", "  bogus:\n    paths: []\n  processors:", 1)
	wantLoadError(t, fullTree(t, map[string]string{"nokia/srlinux/24.10.yaml": bad}),
		"bogus", "24.10.yaml")
}

func TestDuplicateVariantKeyRejected(t *testing.T) {
	wantLoadError(t, fullTree(t, map[string]string{"nokia/dupe/common.yaml": srlinuxCommon}),
		"nokia-srlinux", "srlinux", "dupe")
}

func TestInvalidModeRejected(t *testing.T) {
	bad := strings.Replace(srlinuxCommon, "mode: sample", "mode: streaming", 1)
	wantLoadError(t, fullTree(t, map[string]string{"nokia/srlinux/common.yaml": bad}),
		"common.yaml", "modules.ports.paths.0.mode", "invalid mode 'streaming'")
}

func TestLegacyFlatEntryRejected(t *testing.T) {
	flat := "key: old\nvendor: nokia\nsubscription:\n  - {path: /x, mode: sample}\n"
	wantLoadError(t, fullTree(t, map[string]string{"legacy.yaml": flat}), "legacy")
}

func TestMissingRegistryRejected(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"nokia/vendor.yaml":         nokiaVendor,
		"nokia/srlinux/common.yaml": srlinuxCommon,
	})
	wantLoadError(t, dir, "modules.yaml")
}

func TestMissingVendorYAMLRejected(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"modules.yaml":              treeModules,
		"nokia/srlinux/common.yaml": srlinuxCommon,
	})
	wantLoadError(t, dir, "vendor.yaml")
}

func TestEffectiveProfileCommonOnlyWhenNoVersionMatch(t *testing.T) {
	cat := mustLoad(t, fullTree(t, nil))
	version := "v23.3.1"
	eff, err := cat.EffectiveProfile("nokia-srlinux", &version)
	if err != nil {
		t.Fatal(err)
	}
	if eff.Modules.Len() != 2 {
		t.Fatalf("modules = %v", eff.Modules.Names())
	}
	ports, _ := eff.Modules.Get("ports")
	if len(ports.Paths) != 1 || ports.Paths[0].Path != "/interfaces/interface/state/counters" {
		t.Fatalf("paths = %+v", ports.Paths)
	}
}

func TestLayerAddsModuleAndPath(t *testing.T) {
	cat := mustLoad(t, fullTree(t, nil))
	version := "v24.10.4-244-g5c0afd1a61"
	eff, err := cat.EffectiveProfile("nokia-srlinux", &version)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := eff.Modules.Get("processors"); !ok {
		t.Fatal("processors module added by layer missing")
	}
	ports, _ := eff.Modules.Get("ports")
	paths := map[string]string{}
	for _, p := range ports.Paths {
		paths[*p.Key] = p.Path
	}
	if _, ok := paths["if-errors"]; !ok {
		t.Fatalf("paths = %v", paths)
	}
}

func TestLayerOverridesPathByKey(t *testing.T) {
	cat := mustLoad(t, fullTree(t, nil))
	version := "v24.10.4"
	eff, err := cat.EffectiveProfile("nokia-srlinux", &version)
	if err != nil {
		t.Fatal(err)
	}
	ports, _ := eff.Modules.Get("ports")
	byKey := map[string]*SubscriptionObject{}
	for _, p := range ports.Paths {
		byKey[*p.Key] = p
	}
	if got := byKey["if-counters"]; got.Path != "/interfaces/interface/state/counters-v2" || *got.SampleInterval != "10s" {
		t.Fatalf("overridden path = %+v", got)
	}
	base := "v23.3"
	eff, err = cat.EffectiveProfile("nokia-srlinux", &base)
	if err != nil {
		t.Fatal(err)
	}
	ports, _ = eff.Modules.Get("ports")
	for _, p := range ports.Paths {
		if *p.Key == "if-counters" && p.Path != "/interfaces/interface/state/counters" {
			t.Fatalf("non-matching version path = %q", p.Path)
		}
	}
}

func TestModuleDefaultFromVariantOverridesRegistry(t *testing.T) {
	cat := mustLoad(t, fullTree(t, nil))
	eff, err := cat.EffectiveProfile("nokia-srlinux", nil)
	if err != nil {
		t.Fatal(err)
	}
	bgp, _ := eff.Modules.Get("bgp-peers")
	if !bgp.Default {
		t.Fatal("bgp-peers default from variant should win")
	}
	ports, _ := eff.Modules.Get("ports")
	if !ports.Default {
		t.Fatal("ports default comes from the registry")
	}
}

func TestEqualOrderLayersRecordedAsAmbiguity(t *testing.T) {
	cat := mustLoad(t, fullTree(t, map[string]string{"nokia/srlinux/24.10-b.yaml": srlinux2410}))
	version := "v24.10.4"
	eff, err := cat.EffectiveProfile("nokia-srlinux", &version)
	if err != nil {
		t.Fatal(err)
	}
	if len(eff.Ambiguities) == 0 {
		t.Fatal("want ambiguity for equal-order layers")
	}
	if !strings.Contains(eff.Ambiguities[0], "share order 10") {
		t.Fatalf("ambiguity = %q", eff.Ambiguities[0])
	}
}

func TestResolveVariantByCapabilityModels(t *testing.T) {
	cat := mustLoad(t, fullTree(t, nil))
	srl := cat.ResolveVariant("nokia", []string{"srl_nokia-7220"}, map[string]any{})
	if srl.Variant == nil || srl.Variant.Key != "nokia-srlinux" {
		t.Fatalf("srl match = %+v", srl)
	}
	sros := cat.ResolveVariant("nokia", []string{"nokia-state-foo"}, map[string]any{})
	if sros.Variant == nil || sros.Variant.Key != "nokia-sros" {
		t.Fatalf("sros match = %+v", sros)
	}
}

func TestResolveVariantByProbeModel(t *testing.T) {
	cat := mustLoad(t, fullTree(t, nil))
	mx := cat.ResolveVariant("juniper", nil, map[string]any{"model": "QFX10002-72Q"})
	if mx.Variant == nil || mx.Variant.Key != "juniper-mx" {
		t.Fatalf("mx match = %+v", mx)
	}
	qfx := cat.ResolveVariant("juniper", nil, map[string]any{"model": "QFX5120-48Y-8C"})
	if qfx.Variant == nil || qfx.Variant.Key != "juniper-qfx" {
		t.Fatalf("qfx match = %+v", qfx)
	}
}

func TestResolveVariantNoMatch(t *testing.T) {
	cat := mustLoad(t, fullTree(t, nil))
	res := cat.ResolveVariant("juniper", nil, map[string]any{"model": "EX4300"})
	if res.Variant != nil {
		t.Fatalf("want no match, got %+v", res)
	}
}

func TestProcessorLibraryLoadedAndReferenced(t *testing.T) {
	withProcs := strings.Replace(srlinuxCommon, "  ports:\n", "  ports:\n    processors: [add-device-labels]\n", 1)
	cat := mustLoad(t, fullTree(t, map[string]string{
		"processors/labels.yaml":    processorsYAML,
		"nokia/srlinux/common.yaml": withProcs,
	}))
	if !cat.Processors().Contains("add-device-labels") {
		t.Fatalf("processor names = %v", cat.Processors().Names())
	}
	eff, err := cat.EffectiveProfile("nokia-srlinux", nil)
	if err != nil {
		t.Fatal(err)
	}
	ports, _ := eff.Modules.Get("ports")
	if len(ports.Processors) != 1 || ports.Processors[0] != "add-device-labels" {
		t.Fatalf("processors = %v", ports.Processors)
	}
}

func TestModuleWithoutProcessorsIsIdentity(t *testing.T) {
	cat := mustLoad(t, fullTree(t, map[string]string{"processors/labels.yaml": processorsYAML}))
	eff, err := cat.EffectiveProfile("nokia-srlinux", nil)
	if err != nil {
		t.Fatal(err)
	}
	ports, _ := eff.Modules.Get("ports")
	if ports.Processors == nil || len(ports.Processors) != 0 {
		t.Fatalf("processors = %#v", ports.Processors)
	}
}

func TestUnknownProcessorReferenceRejected(t *testing.T) {
	bad := strings.Replace(srlinuxCommon, "  ports:\n", "  ports:\n    processors: [does-not-exist]\n", 1)
	wantLoadError(t, fullTree(t, map[string]string{
		"processors/labels.yaml":    processorsYAML,
		"nokia/srlinux/common.yaml": bad,
	}), "does-not-exist", "common.yaml")
}

func TestDefaultPortForPlatform(t *testing.T) {
	cat := mustLoad(t, fullTree(t, nil))
	hint := "nokia_srl"
	if got := cat.DefaultPortForPlatform(&hint); got == nil || *got != 57400 {
		t.Fatalf("default port = %v", got)
	}
	empty := ""
	if got := cat.DefaultPortForPlatform(&empty); got != nil {
		t.Fatalf("empty hint = %v", got)
	}
	if got := cat.DefaultPortForPlatform(nil); got != nil {
		t.Fatalf("nil hint = %v", got)
	}
}

func TestNullEntriesRejected(t *testing.T) {
	header := "key: nokia-srlinux\nvendor: nokia\nplatform_match:\n  models_any: [\"srl_nokia\"]\nmodules:\n"
	cases := map[string]string{
		"null module": header + "  ports:\n",
		"null path":   header + "  ports:\n    paths:\n      - \n",
		"null metric": header + "  ports:\n    metrics:\n      m1:\n",
	}
	for name, common := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(fullTree(t, map[string]string{"nokia/srlinux/common.yaml": common})); err == nil {
				t.Fatal("Load succeeded, want error")
			}
		})
	}
}
