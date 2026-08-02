package quality

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/kaiizer777/onyx-scrapper/internal/timecontext"
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
	currYear := timecontext.Now().Year()
	yearPatternStr := fmt.Sprintf(`\b(%d|%d|%d)\b`, currYear-1, currYear, currYear+1)

	return &EntityDetector{
		// Matches Capitalized words followed optionally by version tokens
		// Simplified regex for identifying proper noun + version/model/ordinal
		versionPattern: regexp.MustCompile(`([A-Z][a-zA-Z0-9-]*\s*)+\b(v?\d+(\.\d+)*|[A-Z]+-?\d{1,4}|latest|newest|current|next-gen)\b`),
		
		// Matches the current year and adjacent years
		yearPattern: regexp.MustCompile(yearPatternStr),

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

var fourDigitYearPattern = regexp.MustCompile(`\b(20\d{2})\b`)
var recencyKeywordPattern = regexp.MustCompile(`(?i)\b(latest|current|recent|newest|this year|now)\b`)

// RewriteStaleYearQuery detects if a search query has a stale year along with recency keywords,
// and rewrites the stale year to the current year.
func (d *EntityDetector) RewriteStaleYearQuery(query string, currentYear int) (string, bool) {
	if !recencyKeywordPattern.MatchString(query) {
		return query, false
	}
	
	rewritten := query
	changed := false
	matches := fourDigitYearPattern.FindAllString(query, -1)
	for _, match := range matches {
		if year, err := strconv.Atoi(match); err == nil {
			if year < currentYear-1 {
				// Replace only the specific instance to avoid messing up other parts of the query
				rewritten = strings.Replace(rewritten, match, strconv.Itoa(currentYear), 1)
				changed = true
			}
		}
	}
	return rewritten, changed
}
