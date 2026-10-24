// Package detail runs a vendor's platform probe (stage 2 of discovery) and
// extracts the platform identity from the Subscribe-ONCE result.
package detail

import (
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/fatred/gnpctl-v2/internal/catalog"
	"github.com/fatred/gnpctl-v2/internal/transport"
)

// Identity is a platform probe's extracted identity. The three well-known
// fields are nil when the probe could not extract them; any other extracted
// field lands in Fields.
type Identity struct {
	Vendor    string
	OS        any
	Model     any
	OSVersion any
	Fields    map[string]any
}

// Assign stores a well-known identity field, or an extra field in Fields.
func (i *Identity) Assign(name string, value any) {
	switch name {
	case "model":
		i.Model = value
	case "os":
		i.OS = value
	case "os_version":
		i.OSVersion = value
	default:
		if i.Fields == nil {
			i.Fields = map[string]any{}
		}
		i.Fields[name] = value
	}
}

// AsDict is the identity map variant selection consumes.
func (i *Identity) AsDict() map[string]any {
	return map[string]any{"model": i.Model, "os": i.OS, "os_version": i.OSVersion}
}

// Resolve runs the vendor's platform probe and extracts identity, tolerating
// absent fields. Transport and regex errors are returned to the caller.
func Resolve(t transport.GnmiTransport, vendor *catalog.VendorDef) (*Identity, error) {
	identity := &Identity{Vendor: vendor.Vendor, Fields: map[string]any{}}
	probe := vendor.PlatformProbe
	if probe == nil {
		return identity, nil
	}

	encoding := ""
	if probe.Encoding != nil {
		encoding = *probe.Encoding
	}
	updates, err := t.SubscribeOnce([]string{probe.Path}, encoding)
	if err != nil {
		return nil, err
	}
	elements := group(updates)

	for name, locator := range probe.Extract {
		value := locate(elements, locator)
		if value == nil {
			slog.Warn(fmt.Sprintf("probe %s: could not extract '%s'", probe.Path, name))
			continue
		}
		identity.Assign(name, value)
	}

	for _, rule := range probe.Parse {
		leaves := selectElement(elements, rule.Where)
		var text any
		if leaves != nil {
			text = leaves[leafName(rule.From)]
		}
		if text == nil {
			slog.Warn(fmt.Sprintf("probe %s: parse source '%s' absent", probe.Path, rule.From))
			continue
		}
		re, err := regexp.Compile(rule.Pattern)
		if err != nil {
			return nil, err
		}
		source := pyString(text)
		match := re.FindStringSubmatchIndex(source)
		if match == nil {
			slog.Warn(fmt.Sprintf("probe %s: parse '%s' did not match", probe.Path, rule.Pattern))
			continue
		}
		for i, name := range re.SubexpNames() {
			if i == 0 || name == "" || match[2*i] < 0 {
				continue
			}
			identity.Assign(name, source[match[2*i]:match[2*i+1]])
		}
	}

	return identity, nil
}

// elementGroup is one list element's leaves. Groups keep Subscribe order so
// selection is deterministic like Python's insertion-ordered dict.
type elementGroup struct {
	id     string
	leaves map[string]any
}

// leafName is the trailing leaf segment, stripped of any YANG module prefix.
func leafName(path string) string {
	last := strings.TrimRight(path, "/")
	if idx := strings.LastIndex(last, "/"); idx >= 0 {
		last = last[idx+1:]
	}
	if idx := strings.LastIndex(last, ":"); idx >= 0 {
		last = last[idx+1:]
	}
	return last
}

// elementID identifies the list element a leaf belongs to via its last key
// predicate; a leaf with no predicate groups under "".
func elementID(path string) string {
	if idx := strings.LastIndex(path, "]"); idx >= 0 {
		return path[:idx+1]
	}
	return ""
}

// group flattens leaf updates into elements. A container blob (a dict value)
// contributes its keys as leaves; a scalar update contributes its leaf name.
func group(updates []transport.LeafUpdate) []*elementGroup {
	var elements []*elementGroup
	index := map[string]*elementGroup{}
	for _, update := range updates {
		id := elementID(update.Path)
		element := index[id]
		if element == nil {
			element = &elementGroup{id: id, leaves: map[string]any{}}
			index[id] = element
			elements = append(elements, element)
		}
		if blob, ok := update.Value.(map[string]any); ok {
			for key, value := range blob {
				element.leaves[leafName(key)] = value
			}
		} else {
			element.leaves[leafName(update.Path)] = update.Value
		}
	}
	return elements
}

// selectElement picks the element matching every where leaf, or with no where
// merges all elements (a bare locator finds a container leaf anywhere).
func selectElement(elements []*elementGroup, where map[string]string) map[string]any {
	if len(where) == 0 {
		merged := map[string]any{}
		for _, element := range elements {
			for key, value := range element.leaves {
				merged[key] = value
			}
		}
		return merged
	}
	for _, element := range elements {
		ok := true
		for key, want := range where {
			if pyString(element.leaves[key]) != pyString(want) {
				ok = false
				break
			}
		}
		if ok {
			return element.leaves
		}
	}
	return nil
}

func locate(elements []*elementGroup, locator catalog.Locator) any {
	leaves := selectElement(elements, locator.Where)
	if leaves == nil {
		return nil
	}
	return leaves[leafName(locator.Leaf)]
}

// pyString mirrors Python's str() for the scalar values probes compare and
// match (nil -> "None" so a missing leaf never equals a real where value).
func pyString(value any) string {
	switch v := value.(type) {
	case nil:
		return "None"
	case string:
		return v
	case bool:
		if v {
			return "True"
		}
		return "False"
	}
	return fmt.Sprint(value)
}
