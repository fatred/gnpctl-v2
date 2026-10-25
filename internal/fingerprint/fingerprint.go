// Package fingerprint matches a capabilities profile to a catalog vendor
// (stage 1 of discovery).
package fingerprint

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/fatred/gnpctl-v2/internal/capabilities"
	"github.com/fatred/gnpctl-v2/internal/catalog"
)

// VendorMatch is the highest-priority matching vendor; empty Vendor means the
// device's capabilities matched no catalog entry.
type VendorMatch struct {
	Vendor    string
	Ambiguous bool
}

// MatchVendor returns the highest-priority matching vendor, ties broken by
// vendor name like the Python implementation.
func MatchVendor(profile *capabilities.Profile, cat *catalog.LayeredCatalog) VendorMatch {
	var matched []*catalog.VendorDef
	for _, vendor := range cat.Vendors() {
		if matches(profile, vendor.Fingerprint.Match) {
			matched = append(matched, vendor)
		}
	}
	if len(matched) == 0 {
		return VendorMatch{}
	}
	topPriority := matched[0].Fingerprint.Priority
	for _, vendor := range matched[1:] {
		if vendor.Fingerprint.Priority > topPriority {
			topPriority = vendor.Fingerprint.Priority
		}
	}
	var top []*catalog.VendorDef
	for _, vendor := range matched {
		if vendor.Fingerprint.Priority == topPriority {
			top = append(top, vendor)
		}
	}
	sort.SliceStable(top, func(i, j int) bool { return top[i].Vendor < top[j].Vendor })
	return VendorMatch{Vendor: top[0].Vendor, Ambiguous: len(top) > 1}
}

func matches(profile *capabilities.Profile, match catalog.MatchRule) bool {
	hasVersionMin := match.GNMIVersionMin != nil && *match.GNMIVersionMin != ""
	if len(match.OrgContains) == 0 && len(match.ModelsAny) == 0 &&
		len(match.EncodingsAny) == 0 && !hasVersionMin {
		// A vendor with no match conditions must not match every device.
		return false
	}
	for _, token := range match.OrgContains {
		found := false
		for org := range profile.Organizations {
			if strings.Contains(org, token) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if len(match.ModelsAny) > 0 {
		found := false
		for _, token := range match.ModelsAny {
			for model := range profile.Models {
				if strings.Contains(model, token) {
					found = true
					break
				}
			}
			if found {
				break
			}
		}
		if !found {
			return false
		}
	}
	if len(match.EncodingsAny) > 0 {
		found := false
		for _, encoding := range match.EncodingsAny {
			if _, ok := profile.Encodings[encoding]; ok {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if hasVersionMin && !versionGE(profile.GNMIVersion, *match.GNMIVersionMin) {
		return false
	}
	return true
}

// versionGE mirrors _version_ge: an empty actual version never satisfies a
// minimum, and tuples compare element-wise with a longer prefix winning.
func versionGE(actual any, minimum string) bool {
	if actual == nil || actual == "" { // Parse stores "" for a falsy version
		return false
	}
	return slices.Compare(versionTuple(fmt.Sprint(actual)), versionTuple(minimum)) >= 0
}

// versionTuple takes the leading digits of each dot-separated chunk (0 when a
// chunk has none), as _version_tuple does.
func versionTuple(value string) []int {
	chunks := strings.Split(value, ".")
	parts := make([]int, len(chunks))
	for i, chunk := range chunks {
		n := 0
		for _, r := range chunk {
			if r < '0' || r > '9' {
				break
			}
			n = n*10 + int(r-'0')
		}
		parts[i] = n
	}
	return parts
}
