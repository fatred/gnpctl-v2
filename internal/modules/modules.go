// Package modules implements per-device module enablement: the logic behind
// `gnpctl modules`.
//
// Each target carries one enum per registry module: -1 force-off / 0 off / 1
// on / 2 force-on (effective when >= 1). Enable/Disable pin operator values
// (2 / -1); Reset returns the column to the discovery-owned default; Show
// renders the per-target state.
package modules

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/fatred/gnpctl-v2/internal/catalog"
	"github.com/fatred/gnpctl-v2/internal/state"
)

var labels = map[int]string{-1: "forced-off", 0: "off", 1: "on", 2: "forced-on"}

// ModulesError is raised on an unknown module or target.
type ModulesError struct{ Message string }

func (e *ModulesError) Error() string { return e.Message }

// Row is one target's module state; Modules preserves registry order.
type Row struct {
	Name       string
	CatalogKey string
	Modules    catalog.OrderedMap[string]
}

func rowString(row map[string]any, key string) string {
	s, _ := row[key].(string)
	return s
}

func rowOSVersion(row map[string]any) *string {
	if s, ok := row["os_version"].(string); ok && s != "" {
		return &s
	}
	return nil
}

func label(value int) string {
	if l, ok := labels[value]; ok {
		return l
	}
	return strconv.Itoa(value)
}

func validate(st *state.State, cat *catalog.LayeredCatalog, target, module string) (map[string]any, error) {
	if !cat.Modules().Contains(module) {
		return nil, &ModulesError{Message: fmt.Sprintf("unknown module '%s'", module)}
	}
	row, err := st.GetTarget(target)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, &ModulesError{Message: fmt.Sprintf("no such target: %s", target)}
	}
	return row, nil
}

// effectiveDefault is the discovery-owned default (1/0) for a module given the
// target's profile.
func effectiveDefault(cat *catalog.LayeredCatalog, row map[string]any, module string) (int, error) {
	key := rowString(row, "catalog_key")
	if key == "" || cat.Variant(key) == nil {
		return 0, nil
	}
	profile, err := cat.EffectiveProfile(key, rowOSVersion(row))
	if err != nil {
		return 0, err
	}
	resolved, _ := profile.Modules.Get(module)
	if resolved != nil && len(resolved.Paths) > 0 && resolved.Default {
		return 1, nil
	}
	return 0, nil
}

// lacksWarning warns when force-on targets a module the device's profile
// cannot collect. An empty return means no warning.
func lacksWarning(cat *catalog.LayeredCatalog, row map[string]any, module string) (string, error) {
	key := rowString(row, "catalog_key")
	if key == "" || cat.Variant(key) == nil {
		return fmt.Sprintf("target '%s' has no resolved platform; '%s' will collect nothing",
			rowString(row, "name"), module), nil
	}
	profile, err := cat.EffectiveProfile(key, rowOSVersion(row))
	if err != nil {
		return "", err
	}
	resolved, _ := profile.Modules.Get(module)
	if resolved == nil || len(resolved.Paths) == 0 {
		return fmt.Sprintf("module '%s' has no paths in %s's profile; force-on will collect nothing",
			module, key), nil
	}
	return "", nil
}

// Enable forces a module on (2) for a target and returns a warning when the
// target's profile cannot collect it ("" = none).
func Enable(st *state.State, cat *catalog.LayeredCatalog, target, module string) (string, error) {
	row, err := validate(st, cat, target, module)
	if err != nil {
		return "", err
	}
	if err := st.EnsureModuleColumns(cat.Modules().Names()); err != nil {
		return "", err
	}
	if err := st.SetModule(target, module, 2); err != nil {
		return "", err
	}
	return lacksWarning(cat, row, module)
}

// Disable forces a module off (-1) for a target.
func Disable(st *state.State, cat *catalog.LayeredCatalog, target, module string) error {
	if _, err := validate(st, cat, target, module); err != nil {
		return err
	}
	if err := st.EnsureModuleColumns(cat.Modules().Names()); err != nil {
		return err
	}
	return st.SetModule(target, module, -1)
}

// Reset clears an operator pin, returning the module to its discovered
// default (1/0).
func Reset(st *state.State, cat *catalog.LayeredCatalog, target, module string) (int, error) {
	row, err := validate(st, cat, target, module)
	if err != nil {
		return 0, err
	}
	if err := st.EnsureModuleColumns(cat.Modules().Names()); err != nil {
		return 0, err
	}
	value, err := effectiveDefault(cat, row, module)
	if err != nil {
		return 0, err
	}
	return value, st.SetModule(target, module, value)
}

// Show returns per-target module labels, for one target or every target when
// target is nil.
func Show(st *state.State, cat *catalog.LayeredCatalog, target *string) ([]Row, error) {
	if err := st.EnsureModuleColumns(cat.Modules().Names()); err != nil {
		return nil, err
	}
	names := cat.Modules().Names()
	var rows []map[string]any
	if target != nil {
		row, err := st.GetTarget(*target)
		if err != nil {
			return nil, err
		}
		if row == nil {
			return nil, &ModulesError{Message: fmt.Sprintf("no such target: %s", *target)}
		}
		rows = []map[string]any{row}
	} else {
		var err error
		rows, err = st.AllTargets()
		if err != nil {
			return nil, err
		}
	}
	out := make([]Row, 0, len(rows))
	for _, row := range rows {
		r := Row{Name: rowString(row, "name"), CatalogKey: rowString(row, "catalog_key")}
		for _, name := range names {
			v, _ := row[state.ModuleColumn(name)].(int64)
			r.Modules.Set(name, label(int(v)))
		}
		out = append(out, r)
	}
	return out, nil
}

// FormatModules renders Show output as per-target blocks.
func FormatModules(rows []Row) string {
	var lines []string
	for _, row := range rows {
		key := row.CatalogKey
		if key == "" {
			key = "-"
		}
		lines = append(lines, fmt.Sprintf("%s [%s]", row.Name, key))
		names := row.Modules.Names()
		width := 0
		for _, name := range names {
			if len(name) > width {
				width = len(name)
			}
		}
		for _, name := range names {
			l, _ := row.Modules.Get(name)
			lines = append(lines, fmt.Sprintf("  %-*s  %s", width, name, l))
		}
	}
	return strings.Join(lines, "\n")
}
