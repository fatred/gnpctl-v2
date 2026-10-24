package sources

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

const nornirInventory = `
clab-lab-leaf1:
  hostname: 172.20.20.3
  platform: nokia_srl
clab-lab-spine1:
  hostname: 172.20.20.4
  platform: arista_eos
`

func fetchNornir(t *testing.T, text string) []Target {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hosts.yml")
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	targets, err := (&NornirSource{Params: map[string]string{"path": path}}).Fetch()
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	return targets
}

func byName(targets []Target) map[string]Target {
	byName := make(map[string]Target, len(targets))
	for _, target := range targets {
		byName[target.Name] = target
	}
	return byName
}

func TestNornirParsesClabInventory(t *testing.T) {
	targets := fetchNornir(t, nornirInventory)
	if len(targets) != 2 {
		t.Fatalf("got %d targets, want 2", len(targets))
	}
	// Python keeps YAML insertion order.
	if targets[0].Name != "clab-lab-leaf1" || targets[1].Name != "clab-lab-spine1" {
		t.Fatalf("target order = %q, %q", targets[0].Name, targets[1].Name)
	}
	byName := byName(targets)
	leaf := byName["clab-lab-leaf1"]
	if leaf.Address == nil || *leaf.Address != "172.20.20.3" {
		t.Fatalf("leaf address = %v", leaf.Address)
	}
	if leaf.PlatformHint == nil || *leaf.PlatformHint != "nokia_srl" {
		t.Fatalf("leaf platform = %v", leaf.PlatformHint)
	}
	if leaf.Port != nil {
		t.Fatalf("leaf port = %v, want unset", *leaf.Port)
	}
	spine := byName["clab-lab-spine1"]
	if spine.PlatformHint == nil || *spine.PlatformHint != "arista_eos" {
		t.Fatalf("spine platform = %v", spine.PlatformHint)
	}
}

func TestNornirReadsClabFixture(t *testing.T) {
	targets, err := (&NornirSource{
		Params: map[string]string{"path": "../../../testdata/fixtures/clab_nornir.yml"},
	}).Fetch()
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	byName := byName(targets)
	if len(byName) != 2 {
		t.Fatalf("got %d targets, want 2", len(byName))
	}
	if got := byName["leaf1"]; got.Address == nil || *got.Address != "172.20.20.3" ||
		got.PlatformHint == nil || *got.PlatformHint != "nokia_srl" {
		t.Fatalf("leaf1 = %+v", got)
	}
	if got := byName["spine1"]; got.Address == nil || *got.Address != "172.20.20.4" ||
		got.PlatformHint == nil || *got.PlatformHint != "arista_eos" {
		t.Fatalf("spine1 = %+v", got)
	}
}

func TestNornirNestedHostsAndFallbacks(t *testing.T) {
	targets := fetchNornir(t, `
hosts:
  r1:
    address: 10.0.0.1
    platform: nokia_srl
    groups: [leaf]
  r2:
`)
	byName := byName(targets)
	if len(byName) != 2 {
		t.Fatalf("got %d targets, want 2", len(byName))
	}
	r1 := byName["r1"]
	if r1.Address == nil || *r1.Address != "10.0.0.1" {
		t.Fatalf("r1 address = %v", r1.Address)
	}
	r2 := byName["r2"]
	if r2.Address != nil || r2.PlatformHint != nil || r2.Port != nil {
		t.Fatalf("r2 = %+v, want empty target", r2)
	}
}

func TestNornirMissingInventoryErrors(t *testing.T) {
	_, err := (&NornirSource{
		Params: map[string]string{"path": filepath.Join(t.TempDir(), "nope.yml")},
	}).Fetch()
	var sourceErr *SourceError
	if !errors.As(err, &sourceErr) {
		t.Fatalf("err = %v, want *SourceError", err)
	}
}
