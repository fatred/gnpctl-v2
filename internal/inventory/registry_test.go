package inventory

import (
	"errors"
	"strings"
	"testing"

	"github.com/fatred/gnpctl-v2/internal/inventory/sources"
)

func TestBuiltinsResolveFromRegistry(t *testing.T) {
	for _, name := range []string{"nornir", "netbox"} {
		if _, err := Lookup(name); err != nil {
			t.Fatalf("Lookup(%q): %v", name, err)
		}
	}
	nornir, err := Instantiate("nornir", map[string]string{"path": "hosts.yml"}, "")
	if err != nil {
		t.Fatalf("instantiate nornir: %v", err)
	}
	if _, ok := nornir.(*sources.NornirSource); !ok {
		t.Fatalf("nornir source = %T", nornir)
	}
	netbox, err := Instantiate("netbox", map[string]string{"url": "https://nb", "tag": "t"}, "NB")
	if err != nil {
		t.Fatalf("instantiate netbox: %v", err)
	}
	if _, ok := netbox.(*sources.NetBoxSource); !ok {
		t.Fatalf("netbox source = %T", netbox)
	}
}

func TestUnknownTypeRejectedListsAvailable(t *testing.T) {
	_, err := Lookup("does-not-exist")
	var unknown *UnknownSourceType
	if !errors.As(err, &unknown) {
		t.Fatalf("err = %v, want *UnknownSourceType", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "does-not-exist") || !strings.Contains(msg, "nornir") {
		t.Fatalf("msg = %q", msg)
	}
	if msg != "unknown source type 'does-not-exist'; available: netbox, nornir" {
		t.Fatalf("msg = %q", msg)
	}
}

func TestValidateParamsDelegatesToSourceType(t *testing.T) {
	err := ValidateParams("nornir", map[string]string{}, "")
	var sourceErr *sources.SourceError
	if !errors.As(err, &sourceErr) {
		t.Fatalf("err = %v, want *sources.SourceError", err)
	}
	if sourceErr.Msg != "source type 'nornir' missing required parameter(s): path" {
		t.Fatalf("msg = %q", sourceErr.Msg)
	}
	if err := ValidateParams("netbox", map[string]string{"url": "http://nb", "tag": "t"}, "NB"); err == nil {
		t.Fatal("http netbox url accepted")
	}
	if err := ValidateParams("netbox", map[string]string{"url": "https://nb", "tag": "t"}, "NB"); err != nil {
		t.Fatalf("https netbox url rejected: %v", err)
	}
}
