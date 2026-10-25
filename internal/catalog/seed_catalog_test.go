package catalog

import (
	"path/filepath"
	"testing"
)

// Catalog-only port of tests/test_seed_catalog.py: the fingerprint/detail halves
// of those tests belong to internal/fingerprint and internal/detail.
func TestSeedCatalogLoadsCleanly(t *testing.T) {
	cat := mustLoad(t, filepath.Join("..", "..", "catalog"))
	vendors := map[string]bool{}
	for _, v := range cat.Vendors() {
		vendors[v.Vendor] = true
	}
	for _, want := range []string{"nokia", "nokia-sros", "arista", "juniper"} {
		if !vendors[want] {
			t.Fatalf("missing vendor %q in %v", want, vendors)
		}
	}
	if len(vendors) != 4 {
		t.Fatalf("vendors = %v", vendors)
	}
	if cat.Variant("nokia-srlinux") == nil || cat.Variant("arista-eos") == nil {
		t.Fatal("expected nokia-srlinux and arista-eos variants")
	}
	keys := variantKeys(cat.VariantsFor("juniper"))
	if !keys["juniper-mx"] || !keys["juniper-qfx"] || len(keys) != 2 {
		t.Fatalf("juniper variants = %v", keys)
	}
}

func TestSeedNokiaSrosVariantSplit(t *testing.T) {
	cat := mustLoad(t, filepath.Join("..", "..", "catalog"))
	srosModels := []string{"nokia-conf", "nokia-state", "nokia-sr-openconfig-system-deviations", "openconfig-system"}
	srlModels := []string{"srl_nokia-system", "srl_nokia-interfaces", "openconfig-interfaces"}

	sros := cat.ResolveVariant("nokia-sros", srosModels, map[string]any{"model": "7750 SR", "os_version": "25.10.R1"})
	if sros.Variant == nil || sros.Variant.Key != "nokia-sros" {
		t.Fatalf("SR OS match = %+v", sros)
	}
	// SR OS models must not match the SR Linux vendor unit.
	if matched := cat.ResolveVariant("nokia", srosModels, map[string]any{}); matched.Variant != nil {
		t.Fatalf("nokia matched SR OS models: %+v", matched.Variant)
	}
	srl := cat.ResolveVariant("nokia", srlModels,
		map[string]any{"os": "SRLinux", "model": "7220 IXR-D2L", "os_version": "v24.10.4-244-g5c0afd1a61"})
	if srl.Variant == nil || srl.Variant.Key != "nokia-srlinux" {
		t.Fatalf("SR Linux match = %+v", srl)
	}
}

func TestSeedAristaVariant(t *testing.T) {
	cat := mustLoad(t, filepath.Join("..", "..", "catalog"))
	identity := map[string]any{"os_version": "4.33.4M", "os": "cEOS", "model": nil}
	sel := cat.ResolveVariant("arista", []string{"openconfig-interfaces", "openconfig-platform", "arista-intf-augments"}, identity)
	if sel.Variant == nil || sel.Variant.Key != "arista-eos" {
		t.Fatalf("arista match = %+v", sel)
	}
}

func TestSeedJuniperModelSelectsVariant(t *testing.T) {
	cat := mustLoad(t, filepath.Join("..", "..", "catalog"))
	qfx5k := map[string]any{"model": "QFX5120-48Y-8C", "os_version": "23.4R2-S4.11"}
	if got := cat.ResolveVariant("juniper", nil, qfx5k).Variant; got == nil || got.Key != "juniper-qfx" {
		t.Fatalf("qfx5k match = %+v", got)
	}
	qfx10k := map[string]any{"model": "QFX10002-72Q", "os_version": "23.4R2-S6.6"}
	if got := cat.ResolveVariant("juniper", nil, qfx10k).Variant; got == nil || got.Key != "juniper-mx" {
		t.Fatalf("qfx10k match = %+v", got)
	}
}

func TestSeedYangReleaseSelection(t *testing.T) {
	cat := mustLoad(t, filepath.Join("..", "..", "catalog"))
	version := "23.4R2-S4.11"
	sel := cat.Yang().SelectRelease("juniper", &version)
	if sel.Release == nil || *sel.Release != "23.4R2" || sel.Warning != nil {
		t.Fatalf("juniper selection = %+v", sel)
	}
	version = "v24.10.4-244-g5c0afd1a61"
	sel = cat.Yang().SelectRelease("nokia", &version)
	if sel.Release == nil || *sel.Release != "24.10" || sel.Warning != nil {
		t.Fatalf("nokia selection = %+v", sel)
	}
	version = "25.10.R1"
	sel = cat.Yang().SelectRelease("nokia-sros", &version)
	if sel.Release == nil || *sel.Release != "25.10" || sel.Warning != nil {
		t.Fatalf("sros selection = %+v", sel)
	}
}
