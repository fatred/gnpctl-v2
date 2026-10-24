package sources

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"gopkg.in/yaml.v3"
)

const NornirType = "nornir"

var NornirRequiredParams = []string{"path"}

// NornirSource parses a containerlab nornir inventory file.
type NornirSource struct {
	Params        map[string]string
	CredentialRef string
}

func ValidateNornirParams(params map[string]string, credentialRef string) error {
	return ValidateBase(NornirType, NornirRequiredParams, false, params, credentialRef)
}

func (s *NornirSource) Fetch() ([]Target, error) {
	path := s.Params["path"]
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, &SourceError{Msg: fmt.Sprintf("nornir inventory not found: %s", path)}
	}
	if err != nil {
		return nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, &SourceError{Msg: fmt.Sprintf("invalid nornir inventory %s: %v", path, err)}
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, &SourceError{Msg: fmt.Sprintf("nornir inventory %s must be a mapping of hosts", path)}
	}
	hosts := doc.Content[0]
	if h := mapValue(hosts, "hosts"); h != nil && h.Kind == yaml.MappingNode {
		hosts = h
	}
	var targets []Target
	for i := 0; i+1 < len(hosts.Content); i += 2 {
		attrs := hosts.Content[i+1]
		addr := mapString(attrs, "hostname")
		if addr == nil {
			addr = mapString(attrs, "address")
		}
		targets = append(targets, Target{Name: hosts.Content[i].Value, Address: addr, PlatformHint: mapString(attrs, "platform")})
	}
	return targets, nil
}

func mapValue(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

// mapString returns a non-empty scalar value, or nil (Python's `or` semantics).
func mapString(n *yaml.Node, key string) *string {
	v := mapValue(n, key)
	if v == nil || v.Kind != yaml.ScalarNode || v.Tag == "!!null" || v.Value == "" {
		return nil
	}
	return &v.Value
}
