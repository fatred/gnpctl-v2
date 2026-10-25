package transport

import (
	"path/filepath"
	"testing"
)

var _ GnmiTransport = (*FixtureTransport)(nil)

func fixturePath(parts ...string) string {
	return filepath.Join(append([]string{"..", "..", "testdata", "fixtures"}, parts...)...)
}

func TestFixtureReplaysCapabilitiesAndSubscribeOnce(t *testing.T) {
	tr, err := LoadFixture(
		fixturePath("nokia_srl", "capabilities.json"),
		fixturePath("nokia_srl", "subscribe.json"),
	)
	if err != nil {
		t.Fatal(err)
	}

	caps, err := tr.Capabilities()
	if err != nil {
		t.Fatal(err)
	}
	if got := caps["gnmi_version"]; got != "0.10.0" {
		t.Errorf("gnmi_version = %v, want 0.10.0", got)
	}
	models, ok := caps["supported_models"].([]any)
	if !ok || len(models) != 3 {
		t.Errorf("supported_models = %v, want 3 entries", caps["supported_models"])
	}

	updates, err := tr.SubscribeOnce([]string{"/system/information"}, "")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"srl_nokia-system:system/srl_nokia-system-info:information/version":     "v24.10.4-244-g5c0afd1a61",
		"srl_nokia-system:system/srl_nokia-system-info:information/description": "SRLinux-v24.10.4-244-g5c0afd1a61 7220 IXR-D2L Copyright (c) 2000-2025 Nokia.",
	}
	if len(updates) != len(want) {
		t.Fatalf("updates = %v, want %d", updates, len(want))
	}
	for _, update := range updates {
		if want[update.Path] != update.Value {
			t.Errorf("update %q = %v, want %q", update.Path, update.Value, want[update.Path])
		}
	}
}

func TestSubscribeOnceUnknownPathReturnsEmpty(t *testing.T) {
	tr := NewFixtureTransport(nil, map[string]map[string]any{"/a": {"x": 1}})
	updates, err := tr.SubscribeOnce([]string{"/missing"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(updates) != 0 {
		t.Errorf("updates = %v, want none", updates)
	}
}
