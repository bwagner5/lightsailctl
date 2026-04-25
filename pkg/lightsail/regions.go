package lightsail

import (
	"sort"
	"strings"
)

// regionLocations maps AWS region IDs to human-readable locations. Used for
// friendlier region pickers in the TUI.
//
// Sourced from AWS docs; updated as new regions launch. Unknown regions
// fall through to "" (only the ID is shown).
var regionLocations = map[string]string{
	// Americas
	"us-east-1":     "N. Virginia",
	"us-east-2":     "Ohio",
	"us-west-1":     "N. California",
	"us-west-2":     "Oregon",
	"us-gov-east-1": "US Gov East",
	"us-gov-west-1": "US Gov West",
	"ca-central-1":  "Central Canada",
	"ca-west-1":     "Calgary",
	"sa-east-1":     "São Paulo",
	"mx-central-1":  "Mexico",

	// Europe
	"eu-west-1":    "Ireland",
	"eu-west-2":    "London",
	"eu-west-3":    "Paris",
	"eu-central-1": "Frankfurt",
	"eu-central-2": "Zurich",
	"eu-north-1":   "Stockholm",
	"eu-south-1":   "Milan",
	"eu-south-2":   "Spain",

	// Asia Pacific
	"ap-east-1":      "Hong Kong",
	"ap-south-1":     "Mumbai",
	"ap-south-2":     "Hyderabad",
	"ap-southeast-1": "Singapore",
	"ap-southeast-2": "Sydney",
	"ap-southeast-3": "Jakarta",
	"ap-southeast-4": "Melbourne",
	"ap-southeast-5": "Malaysia",
	"ap-southeast-7": "Thailand",
	"ap-northeast-1": "Tokyo",
	"ap-northeast-2": "Seoul",
	"ap-northeast-3": "Osaka",

	// Middle East + Africa
	"me-south-1":   "Bahrain",
	"me-central-1": "UAE",
	"il-central-1": "Tel Aviv",
	"af-south-1":   "Cape Town",

	// China
	"cn-north-1":     "Beijing",
	"cn-northwest-1": "Ningxia",
}

// groupLabels maps the region prefix (first dash-separated segment) to a
// friendlier group name.
var groupLabels = map[string]string{
	"us": "US",
	"ca": "Canada",
	"sa": "South America",
	"mx": "Mexico",
	"eu": "Europe",
	"ap": "Asia Pacific",
	"me": "Middle East",
	"af": "Africa",
	"il": "Israel",
	"cn": "China",
}

// RegionLocation returns the human-readable location for a region, or ""
// if unknown.
func RegionLocation(region string) string { return regionLocations[region] }

// RegionGroup returns a friendly group name for the region's prefix. Falls
// back to the uppercased prefix.
func RegionGroup(region string) string {
	p := regionPrefix(region)
	if g, ok := groupLabels[p]; ok {
		return g
	}
	return strings.ToUpper(p)
}

// regionPrefix is the first dash-separated segment (us / eu / ap / …).
func regionPrefix(region string) string {
	if i := strings.Index(region, "-"); i > 0 {
		return region[:i]
	}
	return region
}

// SortRegionsByGroup sorts regions so regions with the same prefix stay
// adjacent, groups are alphabetized by their friendly name, and within a
// group regions are alphabetized by ID.
func SortRegionsByGroup(regions []string) []string {
	out := make([]string, len(regions))
	copy(out, regions)
	sort.SliceStable(out, func(i, j int) bool {
		gi, gj := RegionGroup(out[i]), RegionGroup(out[j])
		if gi != gj {
			return gi < gj
		}
		return out[i] < out[j]
	})
	return out
}
