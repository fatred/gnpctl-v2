package pipeline

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fatred/gnpctl-v2/internal/catalog"
	"github.com/fatred/gnpctl-v2/internal/inventory"
	"github.com/fatred/gnpctl-v2/internal/state"
	"github.com/fatred/gnpctl-v2/internal/transport"
)

func readJSON(t *testing.T, path string, dst any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := json.Unmarshal(data, dst); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
}

// e2eFactory mirrors scripts/gen_golden.py transport_for: captured capabilities
// and probe leaves plus a sample for every subscription path of the vendor.
func e2eFactory(t *testing.T, cat *catalog.LayeredCatalog) TransportFactory {
	t.Helper()
	fixtures := filepath.Join("..", "..", "testdata", "fixtures")
	return func(target map[string]any, _ int) (transport.GnmiTransport, error) {
		platform, _ := target["platform_hint"].(string)
		var capsName, subsName, vendor string
		switch {
		case strings.Contains(platform, "juniper"):
			capsName, subsName, vendor = "juniper/capabilities.json", "juniper/qfx10k_components.json", "juniper"
		case strings.Contains(platform, "nokia") || strings.Contains(platform, "srl"):
			capsName, subsName, vendor = "nokia_srl/capabilities.json", "nokia_srl/subscribe.json", "nokia"
		default:
			capsName, subsName, vendor = "arista_eos/capabilities.json", "arista_eos/subscribe.json", "arista"
		}
		caps := map[string]any{}
		subs := map[string]map[string]any{}
		readJSON(t, filepath.Join(fixtures, capsName), &caps)
		readJSON(t, filepath.Join(fixtures, subsName), &subs)
		for _, variant := range cat.VariantsFor(vendor) {
			profile, err := cat.EffectiveProfile(variant.Key, nil)
			if err != nil {
				return nil, err
			}
			for _, name := range profile.Modules.Names() {
				module, _ := profile.Modules.Get(name)
				for _, p := range module.Paths {
					if _, ok := subs[p.Path]; !ok {
						subs[p.Path] = map[string]any{p.Path: 1}
					}
				}
			}
		}
		return transport.NewFixtureTransport(caps, subs), nil
	}
}

func goldenBody(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "golden", name))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	i := strings.IndexByte(string(data), '\n')
	if i < 0 {
		t.Fatalf("%s: missing exit line", name)
	}
	return string(data)[i+1:]
}

func TestDiscoverMatchesGolden(t *testing.T) {
	cat := testCatalog(t)
	s := openTestState(t)
	if err := inventory.AddSource(s, "clab", "nornir", map[string]string{
		"path": filepath.Join("..", "..", "testdata", "fixtures", "clab_nornir.yml"),
	}, ""); err != nil {
		t.Fatalf("AddSource: %v", err)
	}
	if _, err := inventory.Pull(s, nil, nil); err != nil {
		t.Fatalf("Pull: %v", err)
	}

	var out bytes.Buffer
	counts, err := RunDiscovery(s, cat, "default", state.DefaultStaleWindow, nil, e2eFactory(t, cat), nil, &out)
	if err != nil {
		t.Fatalf("RunDiscovery: %v", err)
	}
	fmt.Fprintf(&out, "matched=%d unknown=%d error=%d\n",
		counts[StatusMatched], counts[StatusUnknown], counts[StatusError])
	if got, want := out.String(), goldenBody(t, "discover.txt"); got != want {
		t.Fatalf("discover output mismatch:\n got:\n%s\nwant:\n%s", got, want)
	}

	leaf1 := targetRow(t, s, "leaf1")
	for key, want := range map[string]string{
		"catalog_key": "nokia-srlinux",
		"vendor":      "nokia",
		"model":       "7220 IXR-D2L",
		"os_version":  "v24.10.4-244-g5c0afd1a61",
	} {
		if got := rowStr(t, leaf1, key); got != want {
			t.Fatalf("leaf1 %s = %q, want %q", key, got, want)
		}
	}
	if rowInt(t, leaf1, "discovered") != 1 || rowInt(t, leaf1, "port") != 57400 {
		t.Fatalf("leaf1 row = %v", leaf1)
	}

	spine1 := targetRow(t, s, "spine1")
	if rowStr(t, spine1, "catalog_key") != "arista-eos" ||
		rowStr(t, spine1, "os_version") != "4.33.4M" ||
		rowInt(t, spine1, "port") != 6030 {
		t.Fatalf("spine1 row = %v", spine1)
	}
}
