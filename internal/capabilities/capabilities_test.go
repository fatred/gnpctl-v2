package capabilities

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
)

var rawProfile = map[string]any{
	"supported_models": []any{
		map[string]any{"name": "openconfig-interfaces", "organization": "OpenConfig", "version": "2.0.0"},
		map[string]any{"name": "srl_nokia-system", "organization": "Nokia", "version": "1.0.0"},
	},
	"supported_encodings": []any{"json_ietf", "proto"},
	"gnmi_version":        "0.10.0",
}

func TestParsedIntoProfile(t *testing.T) {
	profile := Parse(rawProfile)
	for _, want := range []string{"openconfig-interfaces", "srl_nokia-system"} {
		if _, ok := profile.Models[want]; !ok {
			t.Errorf("models missing %q: %v", want, profile.Models)
		}
	}
	for _, want := range []string{"Nokia", "OpenConfig"} {
		if _, ok := profile.Organizations[want]; !ok {
			t.Errorf("organizations missing %q: %v", want, profile.Organizations)
		}
	}
	for _, want := range []string{"json_ietf", "proto"} {
		if _, ok := profile.Encodings[want]; !ok {
			t.Errorf("encodings missing %q: %v", want, profile.Encodings)
		}
	}
	if profile.GNMIVersion != "0.10.0" {
		t.Errorf("gnmi_version = %v", profile.GNMIVersion)
	}
}

func TestStableHash(t *testing.T) {
	a := Parse(rawProfile)
	b := Parse(rawProfile)
	if a.Hash() != b.Hash() {
		t.Fatalf("hash not stable: %s != %s", a.Hash(), b.Hash())
	}
}

func TestEmptyCapabilities(t *testing.T) {
	profile := Parse(map[string]any{})
	if len(profile.Models) != 0 {
		t.Errorf("models = %v", profile.Models)
	}
	if profile.GNMIVersion != "" {
		t.Errorf("gnmi_version = %v", profile.GNMIVersion)
	}
}

// TestPythonEscaping checks ensure_ascii escaping (non-ASCII, astral, DEL,
// quotes, tabs) against a hash produced by the Python implementation.
func TestPythonEscaping(t *testing.T) {
	raw := map[string]any{
		"supported_models": []any{
			map[string]any{"name": "m\u00f6del", "organization": `a"b`},
			map[string]any{"name": "tab\there"},
		},
		"supported_encodings": []any{"json_ietf"},
		"gnmi_version":        "0.10.0\u00e9\U0001F600\x7f",
	}
	const want = "0c8cc5cd8b48d68367f45ae16e498a905c774341da7f4c404dac2b510026fa56"
	if got := Parse(raw).Hash(); got != want {
		t.Errorf("hash = %s, want %s", got, want)
	}
}

// TestPythonFloatFormatting checks json.dumps float repr parity for a
// non-string gnmi_version.
func TestPythonFloatFormatting(t *testing.T) {
	cases := []struct {
		version any
		hash    string
	}{
		{1.0, "58aa69999699103f2193fb2a227b4e9d27330fd0f4ace0a76dace7139a7e4c82"},
		{1e16, "353473af625894c23c56e94abdb27c585347899016c698af8f4d41943ea3630d"},
		{1e-5, "8ca3a300a7549ace574a0344a4b644e8aba9c9ff336377b998d0daf70e40b737"},
		{9999999999999998.0, "1de28e825fcab0aa30e514fa3204b58acf21bffa272336b408fc5d8875716641"},
		{math.NaN(), "b2b766abec729017bd302733e219e52347dd98cc8f92ca318fa91c008c20b63e"},
		{math.Inf(1), "a46d563e96d218e03e7efe5a1b053b3414cbca61e565348ce3233bbbcb3e6661"},
	}
	for _, tc := range cases {
		profile := &Profile{GNMIVersion: tc.version}
		if got := profile.Hash(); got != tc.hash {
			t.Errorf("version %v: hash = %s, want %s", tc.version, got, tc.hash)
		}
	}
}

// TestGoldenHashes is the parity gate: each fixture's hash must equal the value
// produced by the Python implementation.
func TestGoldenHashes(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "golden", "capabilities_hashes.json"))
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]string
	if err := json.Unmarshal(data, &want); err != nil {
		t.Fatal(err)
	}
	if len(want) == 0 {
		t.Fatal("no golden hashes")
	}
	for name, hash := range want {
		path := filepath.Join("..", "..", "testdata", "fixtures", name, "capabilities.json")
		fixture, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var raw map[string]any
		if err := json.Unmarshal(fixture, &raw); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := Parse(raw).Hash(); got != hash {
			t.Errorf("%s: hash = %s, want %s", name, got, hash)
		}
	}
}
