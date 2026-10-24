package catalog

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

func loadErr(path string, err error) error {
	return catalogErr("%s: %v", path, err)
}

// readMapping parses a YAML file that must contain a mapping.
func readMapping(path string) (*yaml.Node, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, catalogErr("%s: invalid YAML: %v", path, err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil, catalogErr("%s: expected a YAML mapping", path)
	}
	node := doc.Content[0]
	if node.Kind != yaml.MappingNode {
		return nil, catalogErr("%s: expected a YAML mapping", path)
	}
	return node, nil
}

func mappingValue(node *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

// yamlFiles lists a directory's *.yaml and *.yml files, sorted by name.
func yamlFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml") {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out, nil
}

func dirNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, entry := range entries {
		// os.Stat (not DirEntry.IsDir) follows symlinks, like Python's is_dir().
		info, err := os.Stat(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		if info.IsDir() {
			out = append(out, entry.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

func loadRegistry(path string) (*ModuleRegistry, error) {
	node, err := readMapping(path)
	if err != nil {
		return nil, err
	}
	modules := mappingValue(node, "modules")
	if modules == nil || modules.Kind != yaml.MappingNode || len(modules.Content) == 0 {
		return nil, catalogErr("%s: module registry must declare a 'modules' mapping", path)
	}
	registry := &ModuleRegistry{}
	for i := 0; i+1 < len(modules.Content); i += 2 {
		registry.entries.Set(modules.Content[i].Value, yamlBool(modules.Content[i+1]))
	}
	return registry, nil
}

// loadProcessors loads named gnmic-native event-processors from catalog/processors/.
func loadProcessors(root string) (*ProcessorLibrary, error) {
	library := &ProcessorLibrary{specs: map[string]*yaml.Node{}}
	dir := filepath.Join(root, "processors")
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return library, nil
	}
	files, err := yamlFiles(dir)
	if err != nil {
		return nil, err
	}
	seen := map[string]string{}
	for _, name := range files {
		path := filepath.Join(dir, name)
		node, err := readMapping(path)
		if err != nil {
			return nil, err
		}
		for i := 0; i+1 < len(node.Content); i += 2 {
			proc := node.Content[i].Value
			if prev, ok := seen[proc]; ok {
				return nil, catalogErr("%s: duplicate processor '%s' (also in %s)", path, proc, prev)
			}
			seen[proc] = path
			library.names = append(library.names, proc)
			library.specs[proc] = node.Content[i+1]
		}
	}
	return library, nil
}

// loadYang loads per-release YANG facts from catalog/yang/<vendor>/<release>.yaml.
func loadYang(root string) (*YangRegistry, error) {
	facts := map[string]map[string]map[string]*YangFact{}
	dir := filepath.Join(root, "yang")
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return newYangRegistry(facts), nil
	}
	vendors, err := dirNames(dir)
	if err != nil {
		return nil, err
	}
	for _, vendor := range vendors {
		vendorDir := filepath.Join(dir, vendor)
		releases := map[string]map[string]*YangFact{}
		files, err := yamlFiles(vendorDir)
		if err != nil {
			return nil, err
		}
		for _, name := range files {
			path := filepath.Join(vendorDir, name)
			node, err := readMapping(path)
			if err != nil {
				return nil, err
			}
			idMap := map[string]*YangFact{}
			for i := 0; i+1 < len(node.Content); i += 2 {
				fid := node.Content[i].Value
				spec := node.Content[i+1]
				if spec.Kind != yaml.MappingNode {
					return nil, catalogErr("%s: YANG fact '%s' must be a mapping", path, fid)
				}
				var fact YangFact
				if err := spec.Decode(&fact); err != nil {
					return nil, loadErr(path, err)
				}
				if err := fact.validate(); err != nil {
					return nil, loadErr(path, err)
				}
				idMap[fid] = &fact
			}
			release := strings.TrimSuffix(strings.TrimSuffix(name, ".yaml"), ".yml")
			releases[release] = idMap
		}
		facts[vendor] = releases
	}
	return newYangRegistry(facts), nil
}

func rejectLegacy(root string) error {
	files, err := yamlFiles(root)
	if err != nil {
		return err
	}
	for _, name := range files {
		if name == "modules.yaml" {
			continue
		}
		return catalogErr(
			"%s: legacy flat catalog entry detected; the catalog is now a "+
				"<vendor>/vendor.yaml tree with per-variant common.yaml + version "+
				"layers and a top-level modules.yaml", filepath.Join(root, name))
	}
	return nil
}

// Load loads and validates the layered vendor → variant → version catalog tree.
func Load(dir string) (*LayeredCatalog, error) {
	root := filepath.Clean(dir)
	if _, err := os.Stat(root); err != nil {
		return nil, catalogErr("catalog directory not found: %s", root)
	}

	registryPath := filepath.Join(root, "modules.yaml")
	if _, err := os.Stat(registryPath); err != nil {
		return nil, catalogErr("module registry not found: %s", registryPath)
	}
	registry, err := loadRegistry(registryPath)
	if err != nil {
		return nil, err
	}
	library, err := loadProcessors(root)
	if err != nil {
		return nil, err
	}
	yang, err := loadYang(root)
	if err != nil {
		return nil, err
	}
	if err := rejectLegacy(root); err != nil {
		return nil, err
	}

	cat := &LayeredCatalog{
		registry:   registry,
		processors: library,
		yang:       yang,
		vendorBy:   map[string]*VendorDef{},
		variantBy:  map[string]*Variant{},
		layers:     map[string][]namedLayer{},
	}

	vendors, err := dirNames(root)
	if err != nil {
		return nil, err
	}
	variantFiles := map[string]string{}
	for _, name := range vendors {
		if name == "processors" || name == "yang" {
			continue // the processor library / yang-facts registry, not a vendor
		}
		vendorDir := filepath.Join(root, name)
		vendorPath := filepath.Join(vendorDir, "vendor.yaml")
		if _, err := os.Stat(vendorPath); err != nil {
			return nil, catalogErr("%s: missing vendor.yaml", vendorDir)
		}
		node, err := readMapping(vendorPath)
		if err != nil {
			return nil, err
		}
		var vendor VendorDef
		if err := node.Decode(&vendor); err != nil {
			return nil, loadErr(vendorPath, err)
		}
		if err := vendor.validate(); err != nil {
			return nil, loadErr(vendorPath, err)
		}
		if _, dup := cat.vendorBy[vendor.Vendor]; dup {
			return nil, catalogErr("%s: duplicate vendor '%s'", vendorPath, vendor.Vendor)
		}
		cat.vendorBy[vendor.Vendor] = &vendor
		cat.vendors = append(cat.vendors, &vendor)

		variantDirs, err := dirNames(vendorDir)
		if err != nil {
			return nil, err
		}
		for _, variantDirName := range variantDirs {
			variantDir := filepath.Join(vendorDir, variantDirName)
			commonPath := filepath.Join(variantDir, "common.yaml")
			if _, err := os.Stat(commonPath); err != nil {
				return nil, catalogErr("%s: missing common.yaml", variantDir)
			}
			node, err := readMapping(commonPath)
			if err != nil {
				return nil, err
			}
			var variant Variant
			if err := node.Decode(&variant); err != nil {
				return nil, loadErr(commonPath, err)
			}
			if err := variant.validate(); err != nil {
				return nil, loadErr(commonPath, err)
			}
			if err := validateModules(variant.Modules, registry, library, commonPath); err != nil {
				return nil, err
			}
			if err := validateYangRefs(variant.Modules, yang, vendor.Vendor, commonPath); err != nil {
				return nil, err
			}
			if _, dup := cat.variantBy[variant.Key]; dup {
				return nil, catalogErr("duplicate variant key '%s' in %s and %s",
					variant.Key, commonPath, variantFiles[variant.Key])
			}
			cat.variantBy[variant.Key] = &variant
			cat.variants = append(cat.variants, &variant)
			variantFiles[variant.Key] = commonPath

			var variantLayers []namedLayer
			layerNames, err := yamlFiles(variantDir)
			if err != nil {
				return nil, err
			}
			for _, layerName := range layerNames {
				if layerName == "common.yaml" {
					continue
				}
				layerPath := filepath.Join(variantDir, layerName)
				node, err := readMapping(layerPath)
				if err != nil {
					return nil, err
				}
				var layer VersionLayer
				if err := node.Decode(&layer); err != nil {
					return nil, loadErr(layerPath, err)
				}
				if err := layer.validate(); err != nil {
					return nil, loadErr(layerPath, err)
				}
				if err := validateModules(layer.Modules, registry, library, layerPath); err != nil {
					return nil, err
				}
				if err := validateYangRefs(layer.Modules, yang, vendor.Vendor, layerPath); err != nil {
					return nil, err
				}
				variantLayers = append(variantLayers, namedLayer{name: layerName, layer: &layer})
			}
			cat.layers[variant.Key] = variantLayers
		}
	}

	return cat, nil
}

func validateModules(modules OrderedMap[*ModuleBlock], registry *ModuleRegistry, library *ProcessorLibrary, path string) error {
	for _, name := range modules.Names() {
		block, _ := modules.Get(name)
		if block == nil {
			return catalogErr("%s: module '%s' must be a mapping", path, name)
		}
		if !registry.Contains(name) {
			return catalogErr("%s: unknown module '%s' (not in the module registry)", path, name)
		}
		if block.Processors != nil {
			for _, proc := range *block.Processors {
				if !library.Contains(proc) {
					return catalogErr("%s: module '%s' references unknown processor '%s' "+
						"(not in the processor library)", path, name, proc)
				}
			}
		}
	}
	return nil
}

// validateYangRefs checks every path/metric `yang:` ref names a registry id for
// the vendor. Skipped when the vendor has no YANG facts yet.
func validateYangRefs(modules OrderedMap[*ModuleBlock], yang *YangRegistry, vendor, path string) error {
	if !yang.HasVendor(vendor) {
		return nil
	}
	known := yang.IDs(vendor)
	for _, name := range modules.Names() {
		block, _ := modules.Get(name)
		if block == nil {
			return catalogErr("%s: module '%s' must be a mapping", path, name)
		}
		for _, p := range block.Paths {
			if p.Yang != nil && *p.Yang != "" && !known[*p.Yang] {
				return catalogErr("%s: module '%s' path '%s' references unknown yang id '%s'",
					path, name, p.PathKey(), *p.Yang)
			}
		}
		metricNames := make([]string, 0, len(block.Metrics))
		for mname := range block.Metrics {
			metricNames = append(metricNames, mname)
		}
		sort.Strings(metricNames)
		for _, mname := range metricNames {
			decl := block.Metrics[mname]
			if decl.Yang != nil && *decl.Yang != "" && !known[*decl.Yang] {
				return catalogErr("%s: module '%s' metric '%s' references unknown yang id '%s'",
					path, name, mname, *decl.Yang)
			}
		}
	}
	return nil
}

// LayeredCatalog is the loaded, validated catalog tree.
type LayeredCatalog struct {
	registry   *ModuleRegistry
	processors *ProcessorLibrary
	yang       *YangRegistry
	vendors    []*VendorDef
	vendorBy   map[string]*VendorDef
	variants   []*Variant
	variantBy  map[string]*Variant
	layers     map[string][]namedLayer
}

type namedLayer struct {
	name  string
	layer *VersionLayer
}

func (c *LayeredCatalog) Vendors() []*VendorDef { return append([]*VendorDef(nil), c.vendors...) }

func (c *LayeredCatalog) Vendor(name string) *VendorDef { return c.vendorBy[name] }

func (c *LayeredCatalog) VariantsFor(vendor string) []*Variant {
	var out []*Variant
	for _, variant := range c.variants {
		if variant.Vendor == vendor {
			out = append(out, variant)
		}
	}
	return out
}

func (c *LayeredCatalog) Variant(key string) *Variant { return c.variantBy[key] }

// Modules returns the module registry; its Names() order is the modules.yaml order.
func (c *LayeredCatalog) Modules() *ModuleRegistry { return c.registry }

func (c *LayeredCatalog) Processors() *ProcessorLibrary { return c.processors }

func (c *LayeredCatalog) Yang() *YangRegistry { return c.yang }

// DefaultPortForPlatform is the first variant default_port whose platform_hints
// include hint.
func (c *LayeredCatalog) DefaultPortForPlatform(hint *string) *int {
	if hint == nil || *hint == "" {
		return nil
	}
	for _, variant := range c.variants {
		if contains(variant.PlatformHints, *hint) && variant.DefaultPort != nil {
			return variant.DefaultPort
		}
	}
	return nil
}

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
