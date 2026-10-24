package detail

import (
	"path/filepath"
	"sort"
	"testing"

	"github.com/fatred/gnpctl-v2/internal/capabilities"
	"github.com/fatred/gnpctl-v2/internal/catalog"
	"github.com/fatred/gnpctl-v2/internal/fingerprint"
	"github.com/fatred/gnpctl-v2/internal/transport"
)

func nokiaVendor() *catalog.VendorDef {
	return &catalog.VendorDef{
		Vendor: "nokia",
		PlatformProbe: &catalog.PlatformProbe{
			Path:    "/system/information",
			Extract: map[string]catalog.Locator{"os_version": {Leaf: "version"}},
			Parse: []catalog.ParseRule{{
				From:    "description",
				Pattern: `^(?P<os>\S+?)-\S+\s+(?P<model>.+?)\s+Copyright`,
			}},
		},
	}
}

// Subscribe returns YANG module-qualified leaf paths; locators match the trailing name.
var nokiaSubs = map[string]map[string]any{
	"/system/information": {
		"srl_nokia-system:system/srl_nokia-system-info:information/version":     "v24.10.4-244-g5c0afd1a61",
		"srl_nokia-system:system/srl_nokia-system-info:information/description": "SRLinux-v24.10.4-244-g5c0afd1a61 7220 IXR-D2L Copyright (c) 2000-2025 Nokia.",
	},
}

func juniperVendor() *catalog.VendorDef {
	return &catalog.VendorDef{
		Vendor: "juniper",
		PlatformProbe: &catalog.PlatformProbe{
			Path: "/components/component[name=*]/state",
			Extract: map[string]catalog.Locator{
				"os_version": {Where: map[string]string{"type": "CONTROLLER_CARD"}, Leaf: "software-version"},
			},
			Parse: []catalog.ParseRule{{
				Where:   map[string]string{"type": "CONTROLLER_CARD"},
				From:    "description",
				Pattern: `^(?:RE-)?(?P<model>.+)$`,
			}},
		},
	}
}

var juniperQFX5K = map[string]map[string]any{
	"/components/component[name=*]/state": {
		"/components/component[name=Chassis]/state/type":                     "CHASSIS",
		"/components/component[name=Chassis]/state/description":              "JNP48Y8C-CHAS",
		"/components/component[name=Routing Engine0]/state/type":             "CONTROLLER_CARD",
		"/components/component[name=Routing Engine0]/state/description":      "RE-QFX5120-48Y-8C",
		"/components/component[name=Routing Engine0]/state/software-version": "23.4R2-S4.11",
	},
}

func TestLeafName(t *testing.T) {
	cases := map[string]string{
		"srl_nokia-system:system/srl_nokia-system-info:information/version": "version",
		"/components/component[name=Chassis]/state/type":                    "type",
		"/components/component[name=Chassis]/state/":                        "state",
		"": "",
	}
	for path, want := range cases {
		if got := leafName(path); got != want {
			t.Errorf("leafName(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestIdentityFromLeafAndParseModulePrefixTolerant(t *testing.T) {
	identity, err := Resolve(transport.NewFixtureTransport(nil, nokiaSubs), nokiaVendor())
	if err != nil {
		t.Fatal(err)
	}
	if identity.Vendor != "nokia" {
		t.Errorf("vendor = %q", identity.Vendor)
	}
	if identity.OSVersion != "v24.10.4-244-g5c0afd1a61" {
		t.Errorf("os_version = %v", identity.OSVersion)
	}
	if identity.OS != "SRLinux" {
		t.Errorf("os = %v", identity.OS)
	}
	if identity.Model != "7220 IXR-D2L" {
		t.Errorf("model = %v", identity.Model)
	}
}

func TestWhereSelectsControllerCardNotChassis(t *testing.T) {
	identity, err := Resolve(transport.NewFixtureTransport(nil, juniperQFX5K), juniperVendor())
	if err != nil {
		t.Fatal(err)
	}
	// chassis description is the JNP assembly code; the RE carries the model
	if identity.Model != "QFX5120-48Y-8C" {
		t.Errorf("model = %v", identity.Model)
	}
	if identity.OSVersion != "23.4R2-S4.11" {
		t.Errorf("os_version = %v", identity.OSVersion)
	}
}

func TestPartialProbeTolerated(t *testing.T) {
	subs := map[string]map[string]any{
		"/system/information": {"info:version": "v24.10.4"}, // no description
	}
	identity, err := Resolve(transport.NewFixtureTransport(nil, subs), nokiaVendor())
	if err != nil {
		t.Fatal(err)
	}
	if identity.OSVersion != "v24.10.4" {
		t.Errorf("os_version = %v", identity.OSVersion)
	}
	if identity.Model != nil {
		t.Errorf("model = %v, want nil", identity.Model)
	}
	if identity.OS != nil {
		t.Errorf("os = %v, want nil", identity.OS)
	}
}

func TestContainerBlobUpdateIsFlattened(t *testing.T) {
	// Live SR Linux (json_ietf) returns /system/information as ONE update whose value is
	// the whole container dict — its keys are the leaves, not separate leaf updates.
	blob := map[string]map[string]any{
		"/system/information": {
			"srl_nokia-system:system/srl_nokia-system-info:information": map[string]any{
				"version":     "v24.10.1-492-gf8858c5836",
				"description": "SRLinux-v24.10.1-492-gf8858c5836 7220 IXR-D2L Copyright (c) 2000-2020 Nokia.",
				"last-booted": "2025-01-16T18:39:55.415Z",
			},
		},
	}
	identity, err := Resolve(transport.NewFixtureTransport(nil, blob), nokiaVendor())
	if err != nil {
		t.Fatal(err)
	}
	if identity.OSVersion != "v24.10.1-492-gf8858c5836" {
		t.Errorf("os_version = %v", identity.OSVersion)
	}
	if identity.OS != "SRLinux" {
		t.Errorf("os = %v", identity.OS)
	}
	if identity.Model != "7220 IXR-D2L" {
		t.Errorf("model = %v", identity.Model)
	}
}

// --- seed-catalog halves of tests/test_seed_catalog.py (fingerprint + probe) ---

func mustCatalog(t *testing.T) *catalog.LayeredCatalog {
	t.Helper()
	cat, err := catalog.Load(filepath.Join("..", "..", "catalog"))
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

func loadFixture(t *testing.T, capsPath, subsPath string) *transport.FixtureTransport {
	t.Helper()
	base := filepath.Join("..", "..", "testdata", "fixtures")
	if capsPath != "" {
		capsPath = filepath.Join(base, capsPath)
	}
	if subsPath != "" {
		subsPath = filepath.Join(base, subsPath)
	}
	tt, err := transport.LoadFixture(capsPath, subsPath)
	if err != nil {
		t.Fatal(err)
	}
	return tt
}

func loadCaps(t *testing.T, rel string) map[string]any {
	t.Helper()
	raw, err := loadFixture(t, rel, "").Capabilities()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func modelList(p *capabilities.Profile) []string {
	models := make([]string, 0, len(p.Models))
	for model := range p.Models {
		models = append(models, model)
	}
	sort.Strings(models)
	return models
}

func TestSeedNokiaSrosFingerprintSplitAndProbe(t *testing.T) {
	cat := mustCatalog(t)
	sros := capabilities.Parse(loadCaps(t, "nokia_sros/capabilities.json"))
	srl := capabilities.Parse(loadCaps(t, "nokia_srl/capabilities.json"))
	if got := fingerprint.MatchVendor(sros, cat).Vendor; got != "nokia-sros" {
		t.Errorf("SR OS fingerprint = %q, want nokia-sros", got)
	}
	if got := fingerprint.MatchVendor(srl, cat).Vendor; got != "nokia" {
		t.Errorf("SR Linux fingerprint = %q, want nokia", got)
	}
	identity, err := Resolve(loadFixture(t, "", "nokia_sros/subscribe.json"), cat.Vendor("nokia-sros"))
	if err != nil {
		t.Fatal(err)
	}
	if identity.Vendor != "nokia-sros" {
		t.Errorf("vendor = %q", identity.Vendor)
	}
	if identity.Model != "7750 SR" {
		t.Errorf("model = %v", identity.Model)
	}
	if identity.OSVersion != "25.10.R1" { // not "B-25.10.R1" — load prefix stripped
		t.Errorf("os_version = %v", identity.OSVersion)
	}
}

func TestSeedNokiaVendorProbeAndVariant(t *testing.T) {
	cat := mustCatalog(t)
	tt := loadFixture(t, "nokia_srl/capabilities.json", "")
	raw, err := tt.Capabilities()
	if err != nil {
		t.Fatal(err)
	}
	profile := capabilities.Parse(raw)
	if got := fingerprint.MatchVendor(profile, cat).Vendor; got != "nokia" {
		t.Errorf("fingerprint = %q, want nokia", got)
	}
	identity, err := Resolve(loadFixture(t, "", "nokia_srl/subscribe.json"), cat.Vendor("nokia"))
	if err != nil {
		t.Fatal(err)
	}
	if identity.Vendor != "nokia" || identity.OS != "SRLinux" ||
		identity.Model != "7220 IXR-D2L" || identity.OSVersion != "v24.10.4-244-g5c0afd1a61" {
		t.Errorf("identity = %+v", identity)
	}
	sel := cat.ResolveVariant("nokia", modelList(profile), identity.AsDict())
	if sel.Variant == nil || sel.Variant.Key != "nokia-srlinux" {
		t.Errorf("variant = %+v", sel.Variant)
	}
}

func TestSeedAristaVendorProbeAndVariant(t *testing.T) {
	cat := mustCatalog(t)
	tt := loadFixture(t, "arista_eos/capabilities.json", "")
	raw, err := tt.Capabilities()
	if err != nil {
		t.Fatal(err)
	}
	profile := capabilities.Parse(raw)
	if got := fingerprint.MatchVendor(profile, cat).Vendor; got != "arista" {
		t.Errorf("fingerprint = %q, want arista", got)
	}
	identity, err := Resolve(loadFixture(t, "", "arista_eos/subscribe.json"), cat.Vendor("arista"))
	if err != nil {
		t.Fatal(err)
	}
	if identity.OSVersion != "4.33.4M" {
		t.Errorf("os_version = %v", identity.OSVersion)
	}
	if identity.OS != "cEOS" {
		t.Errorf("os = %v", identity.OS)
	}
	sel := cat.ResolveVariant("arista", modelList(profile), identity.AsDict())
	if sel.Variant == nil || sel.Variant.Key != "arista-eos" {
		t.Errorf("variant = %+v", sel.Variant)
	}
}

func TestSeedJuniperModelSelectsVariant(t *testing.T) {
	cat := mustCatalog(t)
	tt := loadFixture(t, "juniper/capabilities.json", "")
	raw, err := tt.Capabilities()
	if err != nil {
		t.Fatal(err)
	}
	profile := capabilities.Parse(raw)
	if got := fingerprint.MatchVendor(profile, cat).Vendor; got != "juniper" {
		t.Errorf("fingerprint = %q, want juniper", got)
	}

	qfx5k, err := Resolve(loadFixture(t, "", "juniper/qfx5k_components.json"), cat.Vendor("juniper"))
	if err != nil {
		t.Fatal(err)
	}
	if qfx5k.Model != "QFX5120-48Y-8C" || qfx5k.OSVersion != "23.4R2-S4.11" {
		t.Errorf("qfx5k = %+v", qfx5k)
	}
	if sel := cat.ResolveVariant("juniper", nil, qfx5k.AsDict()); sel.Variant == nil || sel.Variant.Key != "juniper-qfx" {
		t.Errorf("qfx5k variant = %+v", sel.Variant)
	}

	qfx10k, err := Resolve(loadFixture(t, "", "juniper/qfx10k_components.json"), cat.Vendor("juniper"))
	if err != nil {
		t.Fatal(err)
	}
	if qfx10k.Model != "QFX10002-72Q" || qfx10k.OSVersion != "23.4R2-S6.6" {
		t.Errorf("qfx10k = %+v", qfx10k)
	}
	if sel := cat.ResolveVariant("juniper", nil, qfx10k.AsDict()); sel.Variant == nil || sel.Variant.Key != "juniper-mx" {
		t.Errorf("qfx10k variant = %+v", sel.Variant)
	}
}
