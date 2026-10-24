package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fatred/gnpctl-v2/internal/catalog"
	"github.com/fatred/gnpctl-v2/internal/pipeline"
	"github.com/fatred/gnpctl-v2/internal/transport"
)

// invokeErr is invoke plus the captured stderr, for the ClickException paths.
func invokeErr(cfg string, args ...string) (int, string, string) {
	var out, errw bytes.Buffer
	code := run(append([]string{"--config", cfg}, args...), &out, &errw)
	return code, out.String(), errw.String()
}

func TestBadParamFails(t *testing.T) {
	cfg := writeConfig(t, t.TempDir())
	code, _, stderr := invokeErr(cfg, "inventory", "add", "x", "--type", "nornir", "-p", "nopath")
	if code != 1 || stderr != "Error: --param must be key=value: nopath\n" {
		t.Errorf("exit=%d stderr=%q", code, stderr)
	}
}

func TestUnknownSourceTypeFails(t *testing.T) {
	cfg := writeConfig(t, t.TempDir())
	code, _, stderr := invokeErr(cfg, "inventory", "add", "x", "--type", "nope")
	if code != 1 || !strings.Contains(stderr, "unknown source type 'nope'") {
		t.Errorf("exit=%d stderr=%q", code, stderr)
	}
}

func TestMissingConfigFails(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.yaml")
	var out, errw bytes.Buffer
	code := run([]string{"--config", missing, "inventory", "list"}, &out, &errw)
	if code != 1 || !strings.Contains(errw.String(), "nope.yaml") {
		t.Errorf("exit=%d stderr=%q", code, errw.String())
	}
}

func TestRenderRequiresRenderBlock(t *testing.T) {
	dir := t.TempDir()
	catalogDir, err := filepath.Abs("../../catalog")
	if err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "gnpctl.yaml")
	text := "database: " + filepath.Join(dir, "state.db") + "\ncatalog: " + catalogDir + "\nlog:\n  level: warning\n"
	if err := os.WriteFile(cfg, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := invokeErr(cfg, "render")
	if code != 1 || !strings.Contains(stderr, "render needs a 'render' block") {
		t.Errorf("exit=%d stderr=%q", code, stderr)
	}
}

// TestUsageErrorsExitTwo covers click's usage exit status: flag, argument and
// subcommand mistakes are 2, while runtime failures stay 1.
func TestUsageErrorsExitTwo(t *testing.T) {
	cfg := writeConfig(t, t.TempDir())
	for _, args := range [][]string{
		{},
		{"nosuch"},
		{"inventory"},
		{"inventory", "nope"},
		{"modules"},
		{"modules", "nope"},
		{"discover", "--bogus"},
		{"discover", "extra"},
		{"render", "extra"},
		{"modules", "enable", "bgp"},
		{"modules", "reset", "bgp"},
		{"inventory", "add", "x"},
	} {
		code, _, stderr := invokeErr(cfg, args...)
		if code != 2 || stderr == "" {
			t.Errorf("%v: exit=%d stderr=%q, want exit 2", args, code, stderr)
		}
	}
}

// pullNornirFixture registers the clab nornir inventory and pulls its targets.
func pullNornirFixture(t *testing.T, cfg string) {
	t.Helper()
	inventory, err := filepath.Abs("../../testdata/fixtures/clab_nornir.yml")
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := invoke(cfg, "inventory", "add", "clab", "--type", "nornir", "-p", "path="+inventory); code != 0 {
		t.Fatalf("inventory add exit=%d", code)
	}
	if code, _ := invoke(cfg, "inventory", "pull"); code != 0 {
		t.Fatalf("inventory pull exit=%d", code)
	}
}

func TestInventoryPullExitsOneWhenSourceFails(t *testing.T) {
	cfg := writeConfig(t, t.TempDir())
	missing := filepath.Join(t.TempDir(), "missing.yml")
	if code, _ := invoke(cfg, "inventory", "add", "bad", "--type", "nornir", "-p", "path="+missing); code != 0 {
		t.Fatalf("inventory add exit=%d", code)
	}
	code, out := invoke(cfg, "inventory", "pull")
	if code != 1 || !strings.Contains(out, "pulled 0 target(s) from 1 source(s)") {
		t.Errorf("exit=%d stdout=%q", code, out)
	}
}

func TestDiscoverExitsOneWhenTargetsError(t *testing.T) {
	cfg := writeConfig(t, t.TempDir())
	pullNornirFixture(t, cfg)
	previous := pipeline.DefaultTransportFactory
	pipeline.DefaultTransportFactory = func(map[string]any, int) (transport.GnmiTransport, error) {
		return nil, errors.New("boom")
	}
	t.Cleanup(func() { pipeline.DefaultTransportFactory = previous })

	code, out := invoke(cfg, "discover")
	if code != 1 || !strings.Contains(out, "matched=0 unknown=0 error=") {
		t.Errorf("exit=%d stdout=%q", code, out)
	}
}

// failSubscribeTransport answers capabilities but every subscription fails.
type failSubscribeTransport struct{}

func (failSubscribeTransport) Capabilities() (map[string]any, error) {
	return nil, errors.New("unused")
}

func (failSubscribeTransport) SubscribeOnce([]string, string) ([]transport.LeafUpdate, error) {
	return nil, errors.New("boom")
}

func TestTestExitsOneWhenAPathFails(t *testing.T) {
	cat, err := catalog.Load("../../catalog")
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	cfg := writeConfig(t, t.TempDir())
	pullNornirFixture(t, cfg)
	previous := pipeline.DefaultTransportFactory
	pipeline.DefaultTransportFactory = fixtureFactory(t, cat)
	t.Cleanup(func() { pipeline.DefaultTransportFactory = previous })
	if code, out := invoke(cfg, "discover"); code != 0 {
		t.Fatalf("discover exit=%d\n%s", code, out)
	}

	pipeline.DefaultTransportFactory = func(map[string]any, int) (transport.GnmiTransport, error) {
		return failSubscribeTransport{}, nil
	}
	code, out := invoke(cfg, "test")
	if code != 1 || !strings.Contains(out, "FAIL (boom)") {
		t.Errorf("exit=%d stdout=%q", code, out)
	}
}

func TestModulesUnknowsModuleAndTargetFail(t *testing.T) {
	cfg := writeConfig(t, t.TempDir())
	if code, _, stderr := invokeErr(cfg, "modules", "enable", "nope", "--target", "leaf1"); code != 1 ||
		stderr != "Error: unknown module 'nope'\n" {
		t.Errorf("unknown module: exit=%d stderr=%q", code, stderr)
	}
	if code, _, stderr := invokeErr(cfg, "modules", "show", "--target", "ghost"); code != 1 ||
		stderr != "Error: no such target: ghost\n" {
		t.Errorf("unknown target: exit=%d stderr=%q", code, stderr)
	}
}
