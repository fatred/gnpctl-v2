package catalog

import (
	"fmt"
	"maps"
	"slices"
)

// YangSelection is the registry release chosen for a device, with an optional advisory.
type YangSelection struct {
	Release *string
	Warning *string
}

// YangRegistry holds per-release YANG facts (catalog/yang/<vendor>/<release>.yaml).
type YangRegistry struct {
	facts map[string]map[string]map[string]*YangFact
}

func newYangRegistry(facts map[string]map[string]map[string]*YangFact) *YangRegistry {
	return &YangRegistry{facts: facts}
}

func (y *YangRegistry) Releases(vendor string) []string {
	// Python sorts the filename-ordered dict keys stably by version key, so ties
	// (4.32.0F vs 4.32.0M) resolve by name, not map iteration order.
	rels := slices.Sorted(maps.Keys(y.facts[vendor]))
	slices.SortStableFunc(rels, func(a, b string) int { return slices.Compare(versionKey(a), versionKey(b)) })
	return rels
}

// SelectRelease picks the registry release nearest below the device's
// os_version; the newest (with a warning) when the device is newer than every
// pinned release, the oldest (with a warning) when older than all.
func (y *YangRegistry) SelectRelease(vendor string, osVersion *string) YangSelection {
	rels := y.Releases(vendor)
	if len(rels) == 0 {
		warning := fmt.Sprintf("no YANG facts for vendor '%s'", vendor)
		return YangSelection{Warning: &warning}
	}
	if osVersion == nil {
		release := rels[len(rels)-1]
		return YangSelection{Release: &release}
	}
	dev := versionKey(*osVersion)
	chosen := -1
	for i, rel := range rels {
		if slices.Compare(versionKey(rel), dev) <= 0 {
			chosen = i
		}
	}
	if chosen >= 0 {
		newest := versionKey(rels[len(rels)-1])
		var warning *string
		// Warn only when the device is beyond the newest pinned *line* — a patch
		// within it (release key is a prefix of the device key) is covered.
		if slices.Compare(dev, newest) > 0 && !(len(dev) >= len(newest) && slices.Equal(dev[:len(newest)], newest)) {
			w := fmt.Sprintf(
				"device os_version '%s' is newer than the newest pinned %s YANG release '%s'; validating against it",
				*osVersion, vendor, rels[len(rels)-1])
			warning = &w
		}
		release := rels[chosen]
		return YangSelection{Release: &release, Warning: warning}
	}
	oldest := rels[0]
	warning := fmt.Sprintf(
		"device os_version '%s' is older than the oldest pinned %s YANG release '%s'; validating against it",
		*osVersion, vendor, oldest)
	return YangSelection{Release: &oldest, Warning: &warning}
}

func (y *YangRegistry) HasVendor(vendor string) bool {
	_, ok := y.facts[vendor]
	return ok
}

// IDs returns all fact ids known for a vendor across its releases.
func (y *YangRegistry) IDs(vendor string) map[string]bool {
	out := map[string]bool{}
	for _, rel := range y.facts[vendor] {
		for fid := range rel {
			out[fid] = true
		}
	}
	return out
}

func (y *YangRegistry) Fact(vendor, release, fid string) *YangFact {
	return y.facts[vendor][release][fid]
}

// versionKey is the integer-run tuple Python's _version_key produces.
func versionKey(value string) []int {
	var out []int
	n := 0
	inRun := false
	for _, r := range value {
		if r >= '0' && r <= '9' {
			n = n*10 + int(r-'0')
			inRun = true
			continue
		}
		if inRun {
			out = append(out, n)
			n, inRun = 0, false
		}
	}
	if inRun {
		out = append(out, n)
	}
	return out
}
