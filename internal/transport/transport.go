// Package transport is the gNMI read abstraction: a protocol with live (gRPC)
// and fixture implementations.
//
// The single state-read primitive is SubscribeOnce — a gNMI Subscribe in ONCE
// mode. It works across Nokia, Arista and Juniper and is required where a
// device's Get does not serve operational state (Junos serves Get for
// configuration only). There is deliberately no Get operation.
package transport

import (
	"encoding/json"
	"fmt"
	"os"
)

// DefaultGNMIPort is the port used when a target does not specify one.
const DefaultGNMIPort = 57400

// LeafUpdate is one (leaf-path, value) update. Leaf paths may carry YANG
// module prefixes (e.g. "srl_nokia-system-info:.../version").
type LeafUpdate struct {
	Path  string
	Value any
}

// GnmiTransport reads capabilities and a flat list of leaves from a target.
type GnmiTransport interface {
	Capabilities() (map[string]any, error)
	SubscribeOnce(paths []string, encoding string) ([]LeafUpdate, error)
}

// FixtureTransport replays a captured Capabilities response and Subscribe-ONCE
// leaf updates. Subs maps a requested gNMI path to the leaves it yields;
// SubscribeOnce returns the union of the leaves for the requested paths, with
// no network access.
type FixtureTransport struct {
	caps map[string]any
	subs map[string]map[string]any
}

// NewFixtureTransport builds a fixture transport from already-decoded JSON.
// Nil arguments mean empty.
func NewFixtureTransport(caps map[string]any, subs map[string]map[string]any) *FixtureTransport {
	if caps == nil {
		caps = map[string]any{}
	}
	if subs == nil {
		subs = map[string]map[string]any{}
	}
	return &FixtureTransport{caps: caps, subs: subs}
}

// LoadFixture reads a captured capabilities JSON object and a subscribe JSON
// object ({requested_path: {leaf_path: value}}). An empty path leaves that
// half of the fixture empty.
func LoadFixture(capsPath, subsPath string) (*FixtureTransport, error) {
	caps := map[string]any{}
	if capsPath != "" {
		if err := readJSON(capsPath, &caps); err != nil {
			return nil, err
		}
	}
	subs := map[string]map[string]any{}
	if subsPath != "" {
		if err := readJSON(subsPath, &subs); err != nil {
			return nil, err
		}
	}
	return NewFixtureTransport(caps, subs), nil
}

func readJSON(path string, dst any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, dst); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

func (f *FixtureTransport) Capabilities() (map[string]any, error) {
	return f.caps, nil
}

func (f *FixtureTransport) SubscribeOnce(paths []string, encoding string) ([]LeafUpdate, error) {
	var updates []LeafUpdate
	for _, path := range paths {
		for leaf, value := range f.subs[path] {
			updates = append(updates, LeafUpdate{Path: leaf, Value: value})
		}
	}
	return updates, nil
}
