// Package inventory is the configured-source registry: built-in source types
// plus validated source construction.
package inventory

import (
	"fmt"
	"sort"
	"strings"

	"github.com/fatred/gnpctl-v2/internal/inventory/sources"
	"github.com/fatred/gnpctl-v2/internal/state"
)

// UnknownSourceType is returned when a source type has no built-in registration.
type UnknownSourceType struct {
	Name string
}

func (e *UnknownSourceType) Error() string {
	available := make([]string, 0, len(sourceTypes))
	for name := range sourceTypes {
		available = append(available, name)
	}
	sort.Strings(available)
	joined := strings.Join(available, ", ")
	return fmt.Sprintf("unknown source type '%s'; available: %s", e.Name, joined)
}

// SourceType is one built-in source-type registration.
type SourceType struct {
	Validate func(params map[string]string, credentialRef string) error
	New      func(params map[string]string, credentialRef string) sources.Source
}

var sourceTypes = map[string]SourceType{
	sources.NornirType: {
		Validate: sources.ValidateNornirParams,
		New: func(params map[string]string, credentialRef string) sources.Source {
			return &sources.NornirSource{Params: params, CredentialRef: credentialRef}
		},
	},
	sources.NetBoxType: {
		Validate: sources.ValidateNetBoxParams,
		New: func(params map[string]string, credentialRef string) sources.Source {
			return &sources.NetBoxSource{Params: params, CredentialRef: credentialRef}
		},
	},
}

// InstantiateSource builds a source instance from a stored inventory_sources
// row.
func InstantiateSource(row map[string]any) (sources.Source, error) {
	typeName, _ := row["type"].(string)
	credentialRef, _ := row["credential_ref"].(string)
	params := map[string]string{}
	if stored, ok := row["parameters"].(map[string]any); ok {
		for key, value := range stored {
			if text, ok := value.(string); ok {
				params[key] = text
			} else if value != nil {
				params[key] = fmt.Sprint(value)
			}
		}
	}
	sourceType, ok := sourceTypes[typeName]
	if !ok {
		return nil, &UnknownSourceType{Name: typeName}
	}
	return sourceType.New(params, credentialRef), nil
}

// AddSource validates the type and params against the source class, then
// persists the source.
func AddSource(st *state.State, name, typeName string, params map[string]string, credentialRef string) error {
	sourceType, ok := sourceTypes[typeName]
	if !ok {
		return &UnknownSourceType{Name: typeName}
	}
	if err := sourceType.Validate(params, credentialRef); err != nil {
		return err
	}
	anyParams := make(map[string]any, len(params))
	for key, value := range params {
		anyParams[key] = value
	}
	return st.AddSource(name, typeName, anyParams, credentialRef)
}
