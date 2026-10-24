// Package sources implements the built-in inventory sources and the normalized
// Target they yield.
package sources

import (
	"fmt"
	"strings"
)

// Target is the normalized host yielded by every inventory source.
type Target struct {
	Name         string
	Address      *string
	Port         *int
	PlatformHint *string
}

// Source yields the current list of targets from a source of truth.
type Source interface {
	Fetch() ([]Target, error)
}

// SourceError reports a misconfigured source or a failed fetch.
type SourceError struct {
	Msg string
}

func (e *SourceError) Error() string { return e.Msg }

// ValidateBase enforces a source type's required params and credential
// reference, mirroring BaseSource.validate_params.
func ValidateBase(
	typeName string,
	required []string,
	requiresCredential bool,
	params map[string]string,
	credentialRef string,
) error {
	var missing []string
	for _, p := range required {
		if params[p] == "" {
			missing = append(missing, p)
		}
	}
	if len(missing) > 0 {
		return &SourceError{Msg: fmt.Sprintf(
			"source type '%s' missing required parameter(s): %s",
			typeName, strings.Join(missing, ", "),
		)}
	}
	if requiresCredential && credentialRef == "" {
		return &SourceError{Msg: fmt.Sprintf(
			"source type '%s' requires a credential reference (token env-var name)",
			typeName,
		)}
	}
	return nil
}
