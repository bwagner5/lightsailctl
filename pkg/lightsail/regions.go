package lightsail

import (
	"sort"
	"strings"
)

// SupportedRegions is the canonical list of AWS regions where Amazon
// Lightsail is available.
//
// Source: https://docs.aws.amazon.com/lightsail/latest/userguide/understanding-regions-and-availability-zones-in-amazon-lightsail.html
//
// Used as the candidate set for global fan-out (ListBuckets, ListInstances)
// and for the interactive region picker. --region <id> is NOT validated
// against this list so customers can start using a newly-launched region
// before we ship an update.
//
// Regions marked "opt-in" require the user to enable them in their AWS
// account (ap-southeast-3 Jakarta, ap-southeast-5 Malaysia). Our fan-out
// tolerates per-region errors, so calls there are no-ops for accounts
// that haven't enabled them.
func SupportedRegions() []string {
	return []string{
		"us-east-1",      // N. Virginia
		"us-east-2",      // Ohio
		"us-west-2",      // Oregon
		"ap-south-1",     // Mumbai
		"ap-northeast-1", // Tokyo
		"ap-northeast-2", // Seoul
		"ap-southeast-1", // Singapore
		"ap-southeast-2", // Sydney
		"ap-southeast-3", // Jakarta (opt-in)
		"ap-southeast-5", // Malaysia (opt-in)
		"ca-central-1",   // Canada (Central)
		"eu-central-1",   // Frankfurt
		"eu-west-1",      // Ireland
		"eu-west-2",      // London
		"eu-west-3",      // Paris
		"eu-north-1",     // Stockholm
	}
}

// regionLocations maps each Lightsail-supported region to its human-readable
// location. Unknown regions fall through to "" (only the ID is shown).
var regionLocations = map[string]string{
	"us-east-1":      "N. Virginia",
	"us-east-2":      "Ohio",
	"us-west-2":      "Oregon",
	"ap-south-1":     "Mumbai",
	"ap-northeast-1": "Tokyo",
	"ap-northeast-2": "Seoul",
	"ap-southeast-1": "Singapore",
	"ap-southeast-2": "Sydney",
	"ap-southeast-3": "Jakarta",
	"ap-southeast-5": "Malaysia",
	"ca-central-1":   "Central Canada",
	"eu-central-1":   "Frankfurt",
	"eu-west-1":      "Ireland",
	"eu-west-2":      "London",
	"eu-west-3":      "Paris",
	"eu-north-1":     "Stockholm",
}

// groupLabels maps the region prefix (first dash-separated segment) to a
// friendlier group name.
var groupLabels = map[string]string{
	"us": "US",
	"ca": "Canada",
	"eu": "Europe",
	"ap": "Asia Pacific",
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
