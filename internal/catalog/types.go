// Package catalog loads and validates the layered vendor → variant → version
// gNMI path catalog tree and resolves a device's effective profile.
package catalog

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// CatalogError is raised when a catalog entry is missing, malformed, or duplicated.
type CatalogError struct{ Message string }

func (e *CatalogError) Error() string { return e.Message }

func catalogErr(format string, args ...any) error {
	return &CatalogError{Message: fmt.Sprintf(format, args...)}
}

const (
	modeSample        = "sample"
	modeOnChange      = "on_change"
	modeTargetDefined = "target_defined"
)

var validModes = map[string]bool{
	modeSample:        true,
	modeOnChange:      true,
	modeTargetDefined: true,
}

var validMetricTypes = map[string]bool{"numeric": true, "state": true, "info": true}

// Sorted spellings of the valid sets, as Python's sorted() renders them in errors.
const (
	modesList       = "['on_change', 'sample', 'target_defined']"
	metricTypesList = "['info', 'numeric', 'state']"
)

// OrderedMap is a YAML mapping that keeps declaration order: registry module
// order and per-variant module order are user-visible in CLI output.
type OrderedMap[V any] struct {
	keys []string
	m    map[string]V
}

func (o *OrderedMap[V]) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("expected a mapping")
	}
	o.keys = make([]string, 0, len(node.Content)/2)
	o.m = make(map[string]V, len(node.Content)/2)
	for i := 0; i+1 < len(node.Content); i += 2 {
		k := node.Content[i].Value
		var v V
		if err := node.Content[i+1].Decode(&v); err != nil {
			return err
		}
		o.Set(k, v)
	}
	return nil
}

func (o *OrderedMap[V]) Set(k string, v V) {
	if o.m == nil {
		o.m = make(map[string]V)
	}
	if _, ok := o.m[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.m[k] = v
}

func (o OrderedMap[V]) Names() []string { return append([]string(nil), o.keys...) }

func (o OrderedMap[V]) Get(k string) (V, bool) { v, ok := o.m[k]; return v, ok }

func (o OrderedMap[V]) Len() int { return len(o.keys) }

type MatchRule struct {
	OrgContains    []string `yaml:"org_contains"`
	ModelsAny      []string `yaml:"models_any"`
	EncodingsAny   []string `yaml:"encodings_any"`
	GNMIVersionMin *string  `yaml:"gnmi_version_min"`
}

type SubscriptionObject struct {
	Key            *string `yaml:"key"`
	Path           string  `yaml:"path"`
	Mode           string  `yaml:"mode"`
	SampleInterval *string `yaml:"sample_interval"`
	Encoding       *string `yaml:"encoding"`
	Description    *string `yaml:"description"`
	Yang           *string `yaml:"yang"` // YANG-facts registry id of the subscribed object
}

// PathKey is the identity a version layer overrides by: key, else path.
func (s *SubscriptionObject) PathKey() string {
	if s.Key != nil && *s.Key != "" {
		return *s.Key
	}
	return s.Path
}

func (s *SubscriptionObject) clone() *SubscriptionObject {
	if s == nil {
		return nil
	}
	c := *s
	c.Key = clonePtr(s.Key)
	c.SampleInterval = clonePtr(s.SampleInterval)
	c.Encoding = clonePtr(s.Encoding)
	c.Description = clonePtr(s.Description)
	c.Yang = clonePtr(s.Yang)
	return &c
}

// MetricDecl is a normalized metric a module produces, anchored to its YANG object.
type MetricDecl struct {
	Type           string         `yaml:"type"`
	Yang           *string        `yaml:"yang"`
	Map            map[string]int `yaml:"map"` // state: enum name -> number
	RequiredLabels []string       `yaml:"required_labels"`
	Optional       bool           `yaml:"optional"`
	YangEnum       *bool          `yaml:"yang_enum"` // default true
	Federate       bool           `yaml:"federate"`
}

func (m *MetricDecl) clone() *MetricDecl {
	if m == nil {
		return nil
	}
	c := *m
	c.Yang = clonePtr(m.Yang)
	c.YangEnum = clonePtr(m.YangEnum)
	if m.Map != nil {
		c.Map = make(map[string]int, len(m.Map))
		for k, v := range m.Map {
			c.Map[k] = v
		}
	}
	c.RequiredLabels = append([]string{}, m.RequiredLabels...)
	return &c
}

// YangFact is a YANG object's facts for one release.
type YangFact struct {
	Model    string   `yaml:"model"`
	Path     string   `yaml:"path"`
	Type     string   `yaml:"type"`
	Units    *string  `yaml:"units"`
	Enum     []string `yaml:"enum"`
	Citation *string  `yaml:"citation"`
}

// Locator is a bare leaf name or a where-selected leaf (Where set, Leaf = the
// selected leaf). Bare locators have a nil Where.
type Locator struct {
	Leaf  string
	Where map[string]string
}

func (l *Locator) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		if node.Tag == "!!null" {
			return fmt.Errorf("locator must be a leaf name or a where/leaf mapping")
		}
		return node.Decode(&l.Leaf)
	case yaml.MappingNode:
		var raw struct {
			Where *map[string]string `yaml:"where"`
			Leaf  *string            `yaml:"leaf"`
		}
		if err := node.Decode(&raw); err != nil {
			return err
		}
		if raw.Where == nil || raw.Leaf == nil {
			return fmt.Errorf("locator mapping requires 'where' and 'leaf'")
		}
		l.Where, l.Leaf = *raw.Where, *raw.Leaf
		return nil
	}
	return fmt.Errorf("locator must be a leaf name or a where/leaf mapping")
}

// ParseRule is a regex with named groups applied to a (possibly where-selected) leaf.
type ParseRule struct {
	From    string            `yaml:"from"`
	Pattern string            `yaml:"pattern"`
	Where   map[string]string `yaml:"where"`
}

// PlatformProbe is a Subscribe-ONCE read whose leaves/parse yield platform identity.
type PlatformProbe struct {
	Path     string             `yaml:"path"`
	Extract  map[string]Locator `yaml:"extract"`
	Parse    []ParseRule        `yaml:"parse"`
	Encoding *string            `yaml:"encoding"`
}

type VendorFingerprint struct {
	Match    MatchRule `yaml:"match"`
	Priority int       `yaml:"priority"`
}

type VendorDef struct {
	Vendor        string            `yaml:"vendor"`
	Fingerprint   VendorFingerprint `yaml:"fingerprint"`
	PlatformProbe *PlatformProbe    `yaml:"platform_probe"`
}

// PlatformMatch holds variant selection conditions.
type PlatformMatch struct {
	ModelsAny []string `yaml:"models_any"` // substring vs advertised models
	Model     []string `yaml:"model"`      // globs vs probe identity model
	Os        []string `yaml:"os"`         // globs vs probe identity os
}

// ModuleBlock is a module's paths within a variant or version layer.
type ModuleBlock struct {
	Default    *bool                  `yaml:"default"`
	Processors *[]string              `yaml:"processors"`
	Metrics    map[string]*MetricDecl `yaml:"metrics"`
	Paths      []*SubscriptionObject  `yaml:"paths"`
}

type Variant struct {
	Key           string                   `yaml:"key"`
	Vendor        string                   `yaml:"vendor"`
	PlatformMatch PlatformMatch            `yaml:"platform_match"`
	DefaultPort   *int                     `yaml:"default_port"`
	PlatformHints []string                 `yaml:"platform_hints"`
	Encoding      *string                  `yaml:"encoding"`
	Modules       OrderedMap[*ModuleBlock] `yaml:"modules"`
}

type VersionLayer struct {
	VersionMatch []string                 `yaml:"version_match"`
	Order        *int                     `yaml:"order"`
	Modules      OrderedMap[*ModuleBlock] `yaml:"modules"`
}

// ModuleRegistry holds the canonical module names and their global default enablement.
type ModuleRegistry struct {
	entries OrderedMap[bool]
}

func (r *ModuleRegistry) Names() []string { return r.entries.Names() }

func (r *ModuleRegistry) DefaultFor(name string) bool {
	v, ok := r.entries.Get(name)
	return ok && v
}

func (r *ModuleRegistry) Contains(name string) bool {
	_, ok := r.entries.Get(name)
	return ok
}

// ProcessorLibrary holds named, gnmic-native event-processor specs (egress-side).
// Get returns the raw YAML node so callers can re-emit the spec in file order.
type ProcessorLibrary struct {
	names []string
	specs map[string]*yaml.Node
}

func (p *ProcessorLibrary) Names() []string { return append([]string(nil), p.names...) }

func (p *ProcessorLibrary) Get(name string) *yaml.Node { return p.specs[name] }

func (p *ProcessorLibrary) Contains(name string) bool { return p.specs[name] != nil }

func clonePtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// yamlBool resolves a registry value the way PyYAML plus Python's bool() do:
// a quoted scalar stays a string (bool("false") is True), while a plain YAML
// 1.1 bool word is a boolean (bool(no) is False).
func yamlBool(node *yaml.Node) bool {
	if node.Kind == yaml.ScalarNode && node.Style != 0 {
		return node.Value != ""
	}
	var value any
	if err := node.Decode(&value); err != nil {
		return true
	}
	return pyBool(value)
}

// pyBool mirrors Python's bool() after PyYAML has already resolved YAML 1.1
// booleans (on/off/yes/no/true/false) in the raw text.
func pyBool(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		switch strings.ToLower(t) {
		case "on", "yes", "true":
			return true
		case "off", "no", "false":
			return false
		}
		return t != ""
	case int:
		return t != 0
	case int64:
		return t != 0
	case float64:
		return t != 0
	case []any:
		return len(t) > 0
	case map[string]any:
		return len(t) > 0
	}
	return true
}

func pyStr(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `'`, `\'`, "\n", `\n`, "\t", `\t`, "\r", `\r`)
	return "'" + r.Replace(s) + "'"
}

func pyStrList(items []string) string {
	parts := make([]string, len(items))
	for i, s := range items {
		parts[i] = pyStr(s)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// validationError renders a pydantic-style validation error message.
func validationError(model, loc, body string) error {
	msg := "1 validation error for " + model + "\n"
	if loc != "" {
		msg += loc + "\n"
	}
	return &CatalogError{Message: msg + "  " + body}
}

func fieldRequired(model, loc string) error {
	return validationError(model, loc, "Field required")
}

func valueError(model, loc, msg string) error {
	return validationError(model, loc, "Value error, "+msg)
}

func (v *VendorDef) validate() error {
	if v.Vendor == "" {
		return fieldRequired("VendorDef", "vendor")
	}
	if p := v.PlatformProbe; p != nil {
		if p.Path == "" {
			return fieldRequired("VendorDef", "platform_probe.path")
		}
		for i, rule := range p.Parse {
			if rule.From == "" {
				return fieldRequired("VendorDef", fmt.Sprintf("platform_probe.parse.%d.from", i))
			}
			if rule.Pattern == "" {
				return fieldRequired("VendorDef", fmt.Sprintf("platform_probe.parse.%d.pattern", i))
			}
		}
	}
	return nil
}

func (v *Variant) validate() error {
	if v.Key == "" {
		return fieldRequired("Variant", "key")
	}
	if v.Vendor == "" {
		return fieldRequired("Variant", "vendor")
	}
	for _, name := range v.Modules.Names() {
		block, _ := v.Modules.Get(name)
		if err := block.validate("Variant", "modules."+name); err != nil {
			return err
		}
	}
	return nil
}

func (l *VersionLayer) validate() error {
	for _, name := range l.Modules.Names() {
		block, _ := l.Modules.Get(name)
		if err := block.validate("VersionLayer", "modules."+name); err != nil {
			return err
		}
	}
	return nil
}

func (b *ModuleBlock) validate(model, prefix string) error {
	if b == nil {
		return nil
	}
	for i, p := range b.Paths {
		loc := fmt.Sprintf("%s.paths.%d", prefix, i)
		if p == nil {
			return valueError(model, loc, "Input should be a valid dictionary or instance of SubscriptionObject")
		}
		if p.Path == "" {
			return fieldRequired(model, loc+".path")
		}
		if p.Mode == "" {
			return fieldRequired(model, loc+".mode")
		}
		if !validModes[p.Mode] {
			return valueError(model, loc+".mode",
				fmt.Sprintf("invalid mode '%s' (expected one of %s)", p.Mode, modesList))
		}
	}
	names := make([]string, 0, len(b.Metrics))
	for name := range b.Metrics {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		decl := b.Metrics[name]
		loc := prefix + ".metrics." + name
		if decl == nil {
			return valueError(model, loc, "Input should be a valid dictionary or instance of MetricDecl")
		}
		if decl.Type == "" {
			return fieldRequired(model, loc+".type")
		}
		if !validMetricTypes[decl.Type] {
			return valueError(model, loc+".type",
				fmt.Sprintf("invalid metric type '%s' (expected one of %s)", decl.Type, metricTypesList))
		}
		if (decl.Type == "numeric" || decl.Type == "state") && (decl.Yang == nil || *decl.Yang == "") {
			return valueError(model, loc, fmt.Sprintf("metric type '%s' requires a 'yang' reference", decl.Type))
		}
	}
	return nil
}

func (f *YangFact) validate() error {
	if f.Model == "" {
		return fieldRequired("YangFact", "model")
	}
	if f.Path == "" {
		return fieldRequired("YangFact", "path")
	}
	if f.Type == "" {
		return fieldRequired("YangFact", "type")
	}
	return nil
}
