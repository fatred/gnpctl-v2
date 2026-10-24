package inventory

import (
	"github.com/fatred/gnpctl-v2/internal/state"
)

// PullError is one source's failed pull; the CLI prints it and exits non-zero.
type PullError struct {
	Name    string
	Message string
}

// PullSummary reports what a pull processed.
type PullSummary struct {
	Sources int
	Targets int
	Errors  []PullError
}

// Pull fetches each source's targets and upserts them, recording per-source
// poll outcome. A failing source is recorded and skipped; the run continues.
func Pull(st *state.State, sourceNames []string, now *int64) (PullSummary, error) {
	summary := PullSummary{}
	sources, err := selectedSources(st, sourceNames)
	if err != nil {
		return summary, err
	}
	for _, row := range sources {
		summary.Sources++
		name, _ := row["name"].(string)
		targets, err := pullSource(st, row, name)
		if err != nil {
			if pollErr := st.RecordSourcePoll(name, err, nil); pollErr != nil {
				return summary, pollErr
			}
			summary.Errors = append(summary.Errors, PullError{Name: name, Message: err.Error()})
			continue
		}
		if err := st.RecordSourcePoll(name, nil, now); err != nil {
			return summary, err
		}
		summary.Targets += targets
	}
	return summary, nil
}

// pullSource upserts one source's targets and reports how many were written.
func pullSource(st *state.State, row map[string]any, name string) (int, error) {
	source, err := InstantiateSource(row)
	if err != nil {
		return 0, err
	}
	targets, err := source.Fetch()
	if err != nil {
		return 0, err
	}
	for _, target := range targets {
		if err := st.UpsertTarget(
			target.Name, target.Address, target.Port, target.PlatformHint, name,
		); err != nil {
			return 0, err
		}
	}
	return len(targets), nil
}

// selectedSources lists the configured sources, optionally narrowed to names.
func selectedSources(st *state.State, names []string) ([]map[string]any, error) {
	rows, err := st.ListSources()
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return rows, nil
	}
	wanted := make(map[string]bool, len(names))
	for _, name := range names {
		wanted[name] = true
	}
	filtered := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		if name, _ := row["name"].(string); wanted[name] {
			filtered = append(filtered, row)
		}
	}
	return filtered, nil
}
