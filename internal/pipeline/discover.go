package pipeline

import (
	"io"

	"github.com/fatred/gnpctl-v2/internal/catalog"
	"github.com/fatred/gnpctl-v2/internal/state"
)

// RunDiscovery discovers the selected targets and returns the
// matched/unknown/error counts. mode is "default" (undiscovered), "periodic"
// (discovered, not run within window) or "force" (all); target, when non-nil,
// narrows the run to that name. report (optional) receives progress lines.
func RunDiscovery(
	st *state.State,
	cat *catalog.LayeredCatalog,
	mode string,
	window int64,
	target *string,
	factory TransportFactory,
	now *int64,
	report io.Writer,
) (map[string]int, error) {
	if mode == "" {
		mode = "default"
	}
	if factory == nil {
		factory = DefaultTransportFactory
	}
	targets, err := st.SelectTargets(mode, window, target, now)
	if err != nil {
		return nil, err
	}
	counts := map[string]int{StatusMatched: 0, StatusUnknown: 0, StatusError: 0}
	for _, row := range targets {
		outcome, err := DiscoverTarget(row, cat, st, factory, now, report)
		if err != nil {
			return counts, err
		}
		counts[outcome.Status]++
	}
	return counts, nil
}
