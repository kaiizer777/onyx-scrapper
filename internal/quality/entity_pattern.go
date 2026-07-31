package quality

import (
	"regexp"
	"strings"
)

// EntityDetector flags claims that are "freshness-sensitive" because they
// name a specific versioned entity, role holder, or current valuation.
type EntityDetector struct {
	versionPattern *regexp.Regexp
	yearPattern    *regexp.Regexp
	rolePattern    *regexp.Regexp
	pricePattern   *regexp.Regexp
}

func NewEntityDetector() *EntityDetector {
	return &EntityDetector{
		// Matches Capitalized words followed optionally by version tokens
		// Simplified regex for identifying proper noun + version/model/ordinal
		versionPattern: regexp.MustCompile(`([A-Z][a-zA-Z0-9-]*\s*)+\b(v?\d+(\.\d+)*|[A-Z]+-?\d{1,4}|latest|newest|current|next-gen)\b`),
		
		// Matches a bare year 2024-2029
		yearPattern: regexp.MustCompile(`\b202[4-9]\b`),

		// Matches CEO of X or X's CEO
		rolePattern: regexp.MustCompile(`(?i)\b(CEO|CTO|President|Prime Minister|Chair|Director)\s+of\s+([A-Z][a-z]+(\s+[A-Z][a-z]+)*)|([A-Z][a-z]+(\s+[A-Z][a-z]+)*)'s\s+(CEO|CTO|President|Prime Minister|Chair|Director)\b`),
		
		// Matches currency adjacent to numbers with "current", "now", "latest", "as of"
		pricePattern: regexp.MustCompile(`(?i)(current|now|as of|latest).*?[$€£¥]\s*\d+([,.]\d+)?([kKmMbBtT]|billion|million|trillion)?|[$€£¥]\s*\d+([,.]\d+)?([kKmMbBtT]|billion|million|trillion)?.*?(current|now|as of|latest)`),
	}
}

// IsFreshnessSensitive returns true if the claim text matches known patterns
// that are highly likely to go stale quickly (versions, leadership, current prices).
func (d *EntityDetector) IsFreshnessSensitive(claim string) bool {
	if d.versionPattern.MatchString(claim) {
		return true
	}
	if d.yearPattern.MatchString(claim) {
		return true
	}
	if d.rolePattern.MatchString(claim) {
		return true
	}
	if d.pricePattern.MatchString(claim) {
		return true
	}
	
	// Fast-path fallback for plain text keywords that might be missed by strict regex
	lower := strings.ToLower(claim)
	if strings.Contains(lower, "current valuation") || strings.Contains(lower, "current price") {
		return true
	}

	return false
}

// ExtractEntity tries to pull out the main entity string for the second-source search query.
// This is a naive heuristic extractor.
func (d *EntityDetector) ExtractEntity(claim string) string {
	// Attempt to pull out the version pattern match
	if match := d.versionPattern.FindString(claim); match != "" {
		return strings.TrimSpace(match)
	}
	if match := d.rolePattern.FindString(claim); match != "" {
		return strings.TrimSpace(match)
	}
	// Fallback to the whole claim if no specific chunk is easily isolatable
	return claim
}

// ExtractVersionToken pulls out the version or year from the claim if matched.
func (d *EntityDetector) ExtractVersionToken(claim string) string {
	if match := d.versionPattern.FindString(claim); match != "" {
		parts := strings.Fields(match)
		if len(parts) > 0 {
			return parts[len(parts)-1]
		}
	}
	if match := d.yearPattern.FindString(claim); match != "" {
		return match
	}
	return ""
}
