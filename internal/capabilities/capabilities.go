// Package capabilities normalizes a raw gNMI Capabilities response into a
// comparable profile with a stable hash shared with the Python implementation.
package capabilities

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Profile is the normalized capabilities view. Sets, like Python's frozensets,
// hold only the truthy model names, organizations and encodings.
type Profile struct {
	Models        map[string]struct{}
	Organizations map[string]struct{}
	Encodings     map[string]struct{}
	GNMIVersion   any
}

// Parse builds a Profile from a pygnmi-style Capabilities response.
func Parse(raw map[string]any) *Profile {
	profile := &Profile{
		Models:        map[string]struct{}{},
		Organizations: map[string]struct{}{},
		Encodings:     map[string]struct{}{},
		GNMIVersion:   "",
	}
	for _, model := range modelObjects(raw["supported_models"]) {
		if name, ok := truthyString(model, "name"); ok {
			profile.Models[name] = struct{}{}
		}
		if org, ok := truthyString(model, "organization"); ok {
			profile.Organizations[org] = struct{}{}
		}
	}
	for _, encoding := range stringList(raw["supported_encodings"]) {
		profile.Encodings[encoding] = struct{}{}
	}
	if version, ok := raw["gnmi_version"]; ok && !falsy(version) {
		profile.GNMIVersion = version
	}
	return profile
}

// Hash is sha256 of the sorted-key JSON payload. Byte-identical to Python's
// json.dumps(..., sort_keys=True) (ensure_ascii, default separators).
func (p *Profile) Hash() string {
	payload := fmt.Sprintf(
		`{"encodings": [%s], "gnmi_version": %s, "models": [%s], "organizations": [%s]}`,
		quotedList(p.Encodings), pyJSON(p.GNMIVersion),
		quotedList(p.Models), quotedList(p.Organizations),
	)
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

func modelObjects(value any) []map[string]any {
	switch items := value.(type) {
	case []map[string]any:
		return items
	case []any:
		out := make([]map[string]any, 0, len(items))
		for _, item := range items {
			if model, ok := item.(map[string]any); ok {
				out = append(out, model)
			}
		}
		return out
	}
	return nil
}

func stringList(value any) []string {
	switch items := value.(type) {
	case []string:
		return items
	case []any:
		out := make([]string, 0, len(items))
		for _, item := range items {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func truthyString(object map[string]any, key string) (string, bool) {
	s, ok := object[key].(string)
	return s, ok && s != ""
}

// falsy mirrors Python truthiness for the JSON scalars a capabilities response
// can carry (used for `raw.get(key) or ""`).
func falsy(value any) bool {
	switch v := value.(type) {
	case nil:
		return true
	case string:
		return v == ""
	case bool:
		return !v
	case float64:
		return v == 0
	case int:
		return v == 0
	case int64:
		return v == 0
	}
	return false
}

func quotedList(set map[string]struct{}) string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for i, key := range keys {
		keys[i] = pyJSONString(key)
	}
	return strings.Join(keys, ", ")
}

// pyJSON renders a scalar the way json.dumps(..., ensure_ascii=True) does.
func pyJSON(value any) string {
	switch v := value.(type) {
	case nil:
		return "null"
	case string:
		return pyJSONString(v)
	case bool:
		if v {
			return "true"
		}
		return "false"
	case float64:
		return pyFloat(v)
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "null"
	}
	return string(data)
}

// pyFloat matches CPython's float repr (json.dumps without a float hook):
// scientific notation outside [1e-4, 1e16), and a trailing ".0" when integral.
func pyFloat(v float64) string {
	switch {
	case math.IsNaN(v):
		return "NaN"
	case math.IsInf(v, 1):
		return "Infinity"
	case math.IsInf(v, -1):
		return "-Infinity"
	}
	scientific := strconv.FormatFloat(v, 'e', -1, 64)
	exponent, _ := strconv.Atoi(scientific[strings.IndexByte(scientific, 'e')+1:])
	if exponent < -4 || exponent >= 16 {
		return scientific
	}
	fixed := strconv.FormatFloat(v, 'f', -1, 64)
	if !strings.Contains(fixed, ".") {
		fixed += ".0"
	}
	return fixed
}

func pyJSONString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			switch {
			case r >= 0x20 && r <= 0x7e:
				b.WriteRune(r)
			case r <= 0xffff:
				fmt.Fprintf(&b, `\u%04x`, r)
			default:
				r -= 0x10000
				fmt.Fprintf(&b, `\u%04x\u%04x`, 0xd800+(r>>10), 0xdc00+(r&0x3ff))
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}
