package inventory

import (
	"strings"

	"github.com/fatred/gnpctl-v2/internal/inventory/sources"
	"github.com/fatred/gnpctl-v2/internal/state"
)

// DiffResult is the source-vs-database variance, in report order.
type DiffResult struct {
	Added   []string
	Changed []string
	Removed []string
}

// Diff reports added/changed/removed target names per source; it never writes.
func Diff(st *state.State, sourceNames []string) (DiffResult, error) {
	result := DiffResult{Added: []string{}, Changed: []string{}, Removed: []string{}}
	allTargets, err := st.AllTargets()
	if err != nil {
		return result, err
	}
	configured, err := selectedSources(st, sourceNames)
	if err != nil {
		return result, err
	}
	for _, src := range configured {
		name, _ := src["name"].(string)
		source, err := InstantiateSource(src)
		if err != nil {
			return result, err
		}
		fetched, order, err := fetchByName(source)
		if err != nil {
			return result, err
		}
		inDB, dbOrder := dbByName(allTargets, name)
		for _, target := range order {
			if row, ok := inDB[target]; ok {
				if changed(fetched[target], row) {
					result.Changed = append(result.Changed, target)
				}
			} else {
				result.Added = append(result.Added, target)
			}
		}
		for _, target := range dbOrder {
			if _, ok := fetched[target]; !ok {
				result.Removed = append(result.Removed, target)
			}
		}
	}
	return result, nil
}

// fetchByName keys fetched targets by name, keeping first-seen order and the
// last value for duplicate names (Python dict-comprehension semantics).
func fetchByName(source sources.Source) (map[string]sources.Target, []string, error) {
	targets, err := source.Fetch()
	if err != nil {
		return nil, nil, err
	}
	byName := make(map[string]sources.Target, len(targets))
	order := make([]string, 0, len(targets))
	for _, target := range targets {
		if _, seen := byName[target.Name]; !seen {
			order = append(order, target.Name)
		}
		byName[target.Name] = target
	}
	return byName, order, nil
}

// dbByName indexes the database rows owned by one source, in table order.
func dbByName(allTargets []map[string]any, source string) (map[string]map[string]any, []string) {
	byName := make(map[string]map[string]any, len(allTargets))
	order := make([]string, 0, len(allTargets))
	for _, row := range allTargets {
		if rowSource, _ := row["source"].(string); rowSource != source {
			continue
		}
		name, _ := row["name"].(string)
		byName[name] = row
		order = append(order, name)
	}
	return byName, order
}

// changed mirrors Python's _changed: empty/missing values compare as nil and a
// nil source port never counts as a change.
func changed(target sources.Target, row map[string]any) bool {
	if deref(target.Address) != rowText(row["address"]) {
		return true
	}
	if deref(target.PlatformHint) != rowText(row["platform_hint"]) {
		return true
	}
	if target.Port != nil {
		port, ok := row["port"].(int64)
		if !ok || int64(*target.Port) != port {
			return true
		}
	}
	return false
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func rowText(value any) string {
	text, _ := value.(string)
	return text
}

// FormatDiff renders a diff as the CLI lines (empty when there is no variance).
func FormatDiff(result DiffResult) string {
	lines := make([]string, 0, len(result.Added)+len(result.Changed)+len(result.Removed))
	for _, name := range result.Added {
		lines = append(lines, "+ "+name)
	}
	for _, name := range result.Changed {
		lines = append(lines, "~ "+name)
	}
	for _, name := range result.Removed {
		lines = append(lines, "- "+name)
	}
	return strings.Join(lines, "\n")
}
