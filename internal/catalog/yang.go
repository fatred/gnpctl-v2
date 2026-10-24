package catalog

import "fmt"

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

func (y *YangRegistry) Vendors() []string {
	out := make([]string, 0, len(y.facts))
	for vendor := range y.facts {
		out = append(out, vendor)
	}
	return out
}

func (y *YangRegistry) Releases(vendor string) []string {
	rels := make([]string, 0, len(y.facts[vendor]))
	for release := range y.facts[vendor] {
		rels = append(rels, release)
	}
	sortByVersion(rels)
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
		if compareVersionKeys(versionKey(rel), dev) <= 0 {
			chosen = i
		}
	}
	if chosen >= 0 {
		newest := versionKey(rels[len(rels)-1])
		var warning *string
		// Warn only when the device is beyond the newest pinned *line* — a patch
		// within it (release key is a prefix of the device key) is covered.
		if compareVersionKeys(dev, newest) > 0 && !hasKeyPrefix(dev, newest) {
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

func (y *YangRegistry) Resolve(vendor string, osVersion *string, fid string) (*YangFact, error) {
	sel := y.SelectRelease(vendor, osVersion)
	if sel.Release == nil {
		return nil, catalogErr("no YANG facts for vendor '%s'", vendor)
	}
	found := y.Fact(vendor, *sel.Release, fid)
	if found == nil {
		return nil, catalogErr("unknown YANG fact id '%s' for vendor '%s' release '%s'",
			fid, vendor, *sel.Release)
	}
	return found, nil
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

func compareVersionKeys(a, b []int) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	}
	return 0
}

func hasKeyPrefix(key, prefix []int) bool {
	if len(key) < len(prefix) {
		return false
	}
	for i := range prefix {
		if key[i] != prefix[i] {
			return false
		}
	}
	return true
}

func sortByVersion(items []string) {
	// Stable, like Python's sorted(key=_version_key) over insertion-ordered keys.
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && compareVersionKeys(versionKey(items[j]), versionKey(items[j-1])) < 0; j-- {
			items[j], items[j-1] = items[j-1], items[j]
		}
	}
}
