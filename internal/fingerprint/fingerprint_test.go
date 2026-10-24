package fingerprint

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/fatred/gnpctl-v2/internal/capabilities"
	"github.com/fatred/gnpctl-v2/internal/catalog"
)

func testCatalog(t *testing.T, vendors map[string]string) *catalog.LayeredCatalog {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "modules.yaml"), []byte("modules:\n  system: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, body := range vendors {
		vendorDir := filepath.Join(dir, name)
		if err := os.MkdirAll(vendorDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(vendorDir, "vendor.yaml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cat, err := catalog.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

func vendorYAML(name string, priority int, match string) string {
	return "vendor: " + name + "\nfingerprint:\n  priority: " + strconv.Itoa(priority) + "\n  match:\n" + match
}

func profile(models, orgs, encodings []string, version any) *capabilities.Profile {
	p := &capabilities.Profile{
		Models:        map[string]struct{}{},
		Organizations: map[string]struct{}{},
		Encodings:     map[string]struct{}{},
		GNMIVersion:   version,
	}
	for _, m := range models {
		p.Models[m] = struct{}{}
	}
	for _, o := range orgs {
		p.Organizations[o] = struct{}{}
	}
	for _, e := range encodings {
		p.Encodings[e] = struct{}{}
	}
	return p
}

func TestVersionGE(t *testing.T) {
	cases := []struct {
		actual   any
		minimum  string
		expected bool
	}{
		{"0.10.0", "0.8.0", true},
		{"0.7.0", "0.8.0", false},
		{"", "0.8.0", false},
		{nil, "0.8.0", false},
		{"0.10", "0.10.0", false}, // a shorter tuple is smaller
		{"0.10.0", "0.10", true},
		{"25.3.2-rc1", "25.3.2", true},
		{"4.32.0F", "4.32.0", true},
		{"26.3.1", "25.10.R1", true},
	}
	for _, tc := range cases {
		if got := versionGE(tc.actual, tc.minimum); got != tc.expected {
			t.Errorf("versionGE(%v, %q) = %v, want %v", tc.actual, tc.minimum, got, tc.expected)
		}
	}
}

func TestHighestPriorityMatchWins(t *testing.T) {
	cat := testCatalog(t, map[string]string{
		"generic": vendorYAML("generic", 50, "    org_contains: [\"Nokia\"]\n"),
		"nokia":   vendorYAML("nokia", 100, "    org_contains: [\"Nokia\"]\n"),
	})
	result := MatchVendor(profile(nil, []string{"Nokia Networks"}, nil, "0.10.0"), cat)
	if result.Vendor != "nokia" {
		t.Errorf("vendor = %q, want nokia", result.Vendor)
	}
	if result.Ambiguous {
		t.Error("ambiguous = true, want false")
	}
}

func TestAllConditionsRequired(t *testing.T) {
	cat := testCatalog(t, map[string]string{
		"nokia": vendorYAML("nokia", 100,
			"    org_contains: [\"Nokia\"]\n    models_any: [\"srl_nokia\"]\n"),
	})
	// org matches but no srl_nokia model present -> no match
	result := MatchVendor(profile([]string{"openconfig-interfaces"}, []string{"Nokia"}, nil, "0.10.0"), cat)
	if result.Vendor != "" {
		t.Errorf("vendor = %q, want none", result.Vendor)
	}
}

func TestNoMatchIsUnknown(t *testing.T) {
	cat := testCatalog(t, map[string]string{
		"arista": vendorYAML("arista", 10, "    org_contains: [\"Arista\"]\n"),
	})
	if got := MatchVendor(profile(nil, []string{"Nokia"}, nil, "0.10.0"), cat).Vendor; got != "" {
		t.Errorf("vendor = %q, want none", got)
	}
}

func TestEqualPriorityTieIsAmbiguousAndDeterministic(t *testing.T) {
	cat := testCatalog(t, map[string]string{
		"bbb": vendorYAML("bbb", 100, "    org_contains: [\"X\"]\n"),
		"aaa": vendorYAML("aaa", 100, "    org_contains: [\"X\"]\n"),
	})
	result := MatchVendor(profile(nil, []string{"X Corp"}, nil, "0.10.0"), cat)
	if !result.Ambiguous {
		t.Error("ambiguous = false, want true")
	}
	if result.Vendor != "aaa" {
		t.Errorf("vendor = %q, want aaa (sorted by vendor name)", result.Vendor)
	}
}

func TestModelsAnySubstringAndVersionMin(t *testing.T) {
	cat := testCatalog(t, map[string]string{
		"nokia": vendorYAML("nokia", 100,
			"    models_any: [\"srl_nokia\"]\n    gnmi_version_min: \"0.8.0\"\n"),
	})
	ok := MatchVendor(profile([]string{"srl_nokia-system"}, nil, nil, "0.10.0"), cat)
	if ok.Vendor != "nokia" {
		t.Errorf("vendor = %q, want nokia", ok.Vendor)
	}
	tooOld := MatchVendor(profile([]string{"srl_nokia-system"}, nil, nil, "0.7.0"), cat)
	if tooOld.Vendor != "" {
		t.Errorf("vendor = %q, want none", tooOld.Vendor)
	}
}
