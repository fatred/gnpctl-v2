package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
