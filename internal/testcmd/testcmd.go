// Package testcmd implements the test command: Subscribe-ONCE each enabled
// module's paths and report per-path.
//
// It validates via gNMI Subscribe (ONCE) — the mechanism the renderer targets,
// and the only state read that works on Junos. Only modules enabled for the
// target (effective enum >= 1) are checked.
package testcmd

import (
	"github.com/fatred/gnpctl-v2/internal/catalog"
	"github.com/fatred/gnpctl-v2/internal/pipeline"
	"github.com/fatred/gnpctl-v2/internal/state"
)

// PathResult is one subscribed path's outcome.
type PathResult struct {
	Path   string
	OK     bool
	Detail string
}

// TargetTestResult is one target's per-path results.
type TargetTestResult struct {
	Name    string
	Ready   bool
	Results []PathResult
}

// RunTest subscribes the effective-profile paths of every enabled module and
// returns the results and whether any path failed. With no target, only
// discovered targets are tested; a named target is tested even when
// undiscovered (reported not-ready).
func RunTest(
	st *state.State,
	cat *catalog.LayeredCatalog,
	target *string,
	factory pipeline.TransportFactory,
) ([]TargetTestResult, bool, error) {
	names := cat.Modules().Names()
	if err := st.EnsureModuleColumns(names); err != nil {
		return nil, false, err
	}

	var targets []map[string]any
	if target != nil {
		rows, err := st.SelectTargets("default", state.DefaultStaleWindow, target, nil)
		if err != nil {
			return nil, false, err
		}
		targets = rows
	} else {
		rows, err := st.AllTargets()
		if err != nil {
			return nil, false, err
		}
		for _, row := range rows {
			if discovered := row["discovered"]; discovered == int64(1) {
				targets = append(targets, row)
			}
		}
	}

	results := []TargetTestResult{}
	anyFailed := false
	for _, row := range targets {
		result, failed, err := testTarget(st, cat, row, names, factory)
		if err != nil {
			return results, anyFailed, err
		}
		if failed {
			anyFailed = true
		}
		results = append(results, result)
	}
	return results, anyFailed, nil
}

// testTarget tests one target's enabled paths; the transport is closed when the
// target is done so a long fleet run does not hold every connection open.
func testTarget(
	st *state.State,
	cat *catalog.LayeredCatalog,
	row map[string]any,
	names []string,
	factory pipeline.TransportFactory,
) (TargetTestResult, bool, error) {
	name, _ := row["name"].(string)
	key, _ := row["catalog_key"].(string)
	variant := cat.Variant(key)
	if variant == nil {
		return TargetTestResult{Name: name, Ready: false}, false, nil
	}

	profile, err := cat.EffectiveProfile(key, rowOSVersion(row))
	if err != nil {
		return TargetTestResult{}, false, err
	}
	enabled := map[string]bool{}
	for _, module := range state.Enabled(row, names) {
		enabled[module] = true
	}
	var paths []string
	for _, moduleName := range profile.Modules.Names() {
		if !enabled[moduleName] {
			continue
		}
		module, _ := profile.Modules.Get(moduleName)
		for _, path := range module.Paths {
			paths = append(paths, path.Path)
		}
	}

	gnmi, err := factory(row, pipeline.ResolvePort(row, cat))
	if err != nil {
		// One unreachable target must not hide the results of the others.
		return TargetTestResult{Name: name, Ready: true, Results: []PathResult{{Path: "(connect)", Detail: err.Error()}}}, true, nil
	}
	defer func() {
		if closer, ok := gnmi.(interface{ Close() error }); ok {
			closer.Close()
		}
	}()

	pathResults := []PathResult{}
	anyFailed := false
	for _, path := range paths {
		updates, err := gnmi.SubscribeOnce([]string{path}, encoding(variant))
		if err != nil {
			pathResults = append(pathResults, PathResult{Path: path, Detail: err.Error()})
			anyFailed = true
			continue
		}
		ok := false
		for _, update := range updates {
			if update.Value != nil {
				ok = true
				break
			}
		}
		result := PathResult{Path: path, OK: ok}
		if !ok {
			result.Detail = "no data"
			anyFailed = true
		}
		pathResults = append(pathResults, result)
	}
	return TargetTestResult{Name: name, Ready: true, Results: pathResults}, anyFailed, nil
}

// encoding is the variant's preferred encoding; the transport imposes no
// default, so an absent preference means json_ietf (Python pygnmi's default).
func encoding(variant *catalog.Variant) string {
	if variant.Encoding != nil && *variant.Encoding != "" {
		return *variant.Encoding
	}
	return "json_ietf"
}

func rowOSVersion(row map[string]any) *string {
	if s, ok := row["os_version"].(string); ok && s != "" {
		return &s
	}
	return nil
}
