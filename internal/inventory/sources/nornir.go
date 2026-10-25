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
		// Decode into a map so YAML aliases and merge keys (<<) resolve like
		// PyYAML's safe_load; the node itself keeps the host order.
		var attrs map[string]any
		if err := hosts.Content[i+1].Decode(&attrs); err != nil {
			return nil, &SourceError{Msg: fmt.Sprintf("invalid nornir inventory %s: %v", path, err)}
		}
		address := attrString(attrs, "hostname", true)
		if address == nil {
			address = attrString(attrs, "address", true)
		}
		targets = append(targets, Target{
			Name:         hosts.Content[i].Value,
			Address:      address,
			PlatformHint: attrString(attrs, "platform", false),
		})
	}
	return targets, nil
}

// attrString returns a host attribute as text, or nil when absent. With
// pythonTruthy it also drops falsy values, matching Python's `x or y`.
func attrString(attrs map[string]any, key string, pythonTruthy bool) *string {
	value, ok := attrs[key]
	if !ok || value == nil || (pythonTruthy && !truthy(value)) {
		return nil
	}
	text := fmt.Sprint(value)
	return &text
}

func truthy(value any) bool {
	switch t := value.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return t != ""
	case int:
		return t != 0
	case int64:
		return t != 0
	case float64:
		return t != 0
	}
	return true
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
