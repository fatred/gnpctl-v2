package catalog

import (
	"fmt"
	"path"
	"sort"
	"strings"
)

// ResolvedMetric is a module metric resolved against the version-selected YANG facts.
type ResolvedMetric struct {
	Name           string
	Type           string
	Yang           *string
	Map            map[string]int
	RequiredLabels []string
	Optional       bool
	YangEnum       bool // False = state anchored to a non-enum (string) leaf
	Federate       bool
	Fact           *YangFact
}

// ResolvedModule is a module's effective paths/processors/metrics for one device.
type ResolvedModule struct {
	Name       string
	Default    bool
	Paths      []*SubscriptionObject
	Processors []string
	Metrics    map[string]*ResolvedMetric
}

// EffectiveProfile is the variant common layered with the matching version files.
type EffectiveProfile struct {
	VariantKey  string
	Modules     OrderedMap[*ResolvedModule]
	Ambiguities []string
}

// VariantMatch is the variant selected for a device, if any.
type VariantMatch struct {
	Variant   *Variant
	Ambiguous bool
}

// ResolveVariant selects the variant whose platform_match holds for this device.
func (c *LayeredCatalog) ResolveVariant(vendor string, models []string, identity map[string]any) VariantMatch {
	var matched []*Variant
	for _, variant := range c.variants {
		if variant.Vendor != vendor {
			continue
		}
		if c.matches(variant.PlatformMatch, models, identity) {
			matched = append(matched, variant)
		}
	}
	if len(matched) == 0 {
		return VariantMatch{}
	}
	sort.Slice(matched, func(i, j int) bool { return matched[i].Key < matched[j].Key })
	return VariantMatch{Variant: matched[0], Ambiguous: len(matched) > 1}
}

func (c *LayeredCatalog) matches(pm PlatformMatch, models []string, identity map[string]any) bool {
	if len(pm.ModelsAny) > 0 && !modelsMatch(models, pm.ModelsAny) {
		return false
	}
	if len(pm.Model) > 0 && !globAny(identityValue(identity, "model"), pm.Model) {
		return false
	}
	if len(pm.Os) > 0 && !globAny(identityValue(identity, "os"), pm.Os) {
		return false
	}
	return true
}

func identityValue(identity map[string]any, key string) *string {
	value, ok := identity[key]
	if !ok || value == nil {
		return nil
	}
	s := fmt.Sprint(value)
	return &s
}

// modelsMatch reports whether any advertised model contains any wanted substring.
func modelsMatch(advertised, wanted []string) bool {
	if contains(wanted, "*") {
		return true
	}
	for _, token := range wanted {
		for _, model := range advertised {
			if strings.Contains(model, token) {
				return true
			}
		}
	}
	return false
}

// globAny reports whether value matches any glob; ["*"] matches even a missing value.
func globAny(value *string, globs []string) bool {
	if contains(globs, "*") {
		return true
	}
	if value == nil {
		return false
	}
	for _, glob := range globs {
		// shortcut: path.Match, not fnmatch's [!...] classes; catalog globs use '*'
		if ok, _ := path.Match(glob, *value); ok {
			return true
		}
	}
	return false
}

func (c *LayeredCatalog) matchingLayers(variantKey string, osVersion *string) []namedLayer {
	if osVersion == nil {
		return nil
	}
	var out []namedLayer
	for _, nl := range c.layers[variantKey] {
		for _, glob := range nl.layer.VersionMatch {
			if ok, _ := path.Match(glob, *osVersion); ok {
				out = append(out, nl)
				break
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		oi, oj := 0, 0
		if out[i].layer.Order != nil {
			oi = *out[i].layer.Order
		}
		if out[j].layer.Order != nil {
			oj = *out[j].layer.Order
		}
		if oi != oj {
			return oi < oj
		}
		return out[i].name < out[j].name
	})
	return out
}

type workSlot struct {
	def        bool
	processors []string
	metrics    map[string]*MetricDecl
	paths      []*SubscriptionObject
	index      map[string]int
}

// mergeBlock additively layers a block into its module slot.
func (c *LayeredCatalog) mergeBlock(work map[string]*workSlot, order *[]string, name string, block *ModuleBlock) {
	slot, ok := work[name]
	if !ok {
		slot = &workSlot{
			def:     c.registry.DefaultFor(name),
			metrics: make(map[string]*MetricDecl, len(block.Metrics)),
			index:   make(map[string]int, len(block.Paths)),
		}
		if block.Default != nil {
			slot.def = *block.Default
		}
		if block.Processors != nil {
			slot.processors = append([]string{}, (*block.Processors)...)
		}
		for mname, decl := range block.Metrics {
			slot.metrics[mname] = decl.clone()
		}
		for _, p := range block.Paths {
			cp := p.clone()
			slot.index[cp.PathKey()] = len(slot.paths)
			slot.paths = append(slot.paths, cp)
		}
		work[name] = slot
		*order = append(*order, name)
		return
	}
	if block.Default != nil {
		slot.def = *block.Default
	}
	if block.Processors != nil {
		slot.processors = append([]string{}, (*block.Processors)...)
	}
	for mname, decl := range block.Metrics { // version layer overrides by name
		slot.metrics[mname] = decl.clone()
	}
	for _, p := range block.Paths {
		cp := p.clone()
		key := cp.PathKey()
		if i, ok := slot.index[key]; ok {
			slot.paths[i] = cp
		} else {
			slot.index[key] = len(slot.paths)
			slot.paths = append(slot.paths, cp)
		}
	}
}

// EffectiveProfile builds a device's profile: variant common plus every matching
// version layer, applied in ascending order.
func (c *LayeredCatalog) EffectiveProfile(variantKey string, osVersion *string) (*EffectiveProfile, error) {
	variant := c.variantBy[variantKey]
	if variant == nil {
		return nil, catalogErr("unknown variant key '%s'", variantKey)
	}
	work := map[string]*workSlot{}
	var order []string
	for _, name := range variant.Modules.Names() {
		block, _ := variant.Modules.Get(name)
		c.mergeBlock(work, &order, name, block)
	}

	var ambiguities []string
	byOrder := map[int][]string{}
	var orderKeys []int
	for _, nl := range c.matchingLayers(variantKey, osVersion) {
		if nl.layer.Order != nil {
			if _, seen := byOrder[*nl.layer.Order]; !seen {
				orderKeys = append(orderKeys, *nl.layer.Order)
			}
			byOrder[*nl.layer.Order] = append(byOrder[*nl.layer.Order], nl.name)
		}
		for _, name := range nl.layer.Modules.Names() {
			block, _ := nl.layer.Modules.Get(name)
			c.mergeBlock(work, &order, name, block)
		}
	}
	for _, o := range orderKeys {
		files := byOrder[o]
		if len(files) > 1 {
			sorted := append([]string(nil), files...)
			sort.Strings(sorted)
			ambiguities = append(ambiguities, fmt.Sprintf(
				"variant '%s': version layers %s share order %d", variantKey, pyStrList(sorted), o))
		}
	}

	release := c.yang.SelectRelease(variant.Vendor, osVersion).Release

	profile := &EffectiveProfile{VariantKey: variantKey, Ambiguities: ambiguities}
	for _, name := range order {
		slot := work[name]
		processors := slot.processors
		if processors == nil {
			processors = []string{}
		}
		module := &ResolvedModule{
			Name:       name,
			Default:    slot.def,
			Paths:      slot.paths,
			Processors: processors,
			Metrics:    make(map[string]*ResolvedMetric, len(slot.metrics)),
		}
		for mname, decl := range slot.metrics {
			var fact *YangFact
			if decl.Yang != nil && *decl.Yang != "" && release != nil {
				fact = c.yang.Fact(variant.Vendor, *release, *decl.Yang)
			}
			enum := decl.YangEnum == nil || *decl.YangEnum
			metricMap := map[string]int{}
			if len(decl.Map) > 0 {
				for k, v := range decl.Map {
					metricMap[k] = v
				}
			}
			module.Metrics[mname] = &ResolvedMetric{
				Name:           mname,
				Type:           decl.Type,
				Yang:           decl.Yang,
				Map:            metricMap,
				RequiredLabels: append([]string{}, decl.RequiredLabels...),
				Optional:       decl.Optional,
				YangEnum:       enum,
				Federate:       decl.Federate,
				Fact:           fact,
			}
		}
		profile.Modules.Set(name, module)
	}
	return profile, nil
}
