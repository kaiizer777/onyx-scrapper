package quality

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/kaiizer777/onyx-scrapper/internal/timecontext"
)

type EntityType int

const (
	EntityUnknown EntityType = iota
	EntityVersion
	EntityYear
	EntityExecutive
	EntityPrice
)

func (t EntityType) String() string {
	switch t {
	case EntityVersion:
		return "version"
	case EntityYear:
		return "year"
	case EntityExecutive:
		return "exec"
	case EntityPrice:
		return "price"
	default:
		return "unknown"
	}
}

type DetectedEntity struct {
	Type     EntityType
	Subject  string // e.g. "Apple", "Kubernetes", "Bitcoin"
	RawMatch string // e.g. "CEO of Apple", "v2.3.1", "$95,000"
}

// EntityDetector flags claims that are "freshness-sensitive" because they
// name a specific versioned entity, role holder, or current valuation.
type EntityDetector struct {
	versionPattern  *regexp.Regexp
	yearPattern     *regexp.Regexp
	rolePattern     *regexp.Regexp
	rolePatternSubj *regexp.Regexp
	pricePattern    *regexp.Regexp
	priceRawPattern *regexp.Regexp
}

func NewEntityDetector() *EntityDetector {
	currYear := timecontext.Now().Year()
	yearPatternStr := fmt.Sprintf(`\b(%d|%d|%d)\b`, currYear-1, currYear, currYear+1)

	return &EntityDetector{
		// Matches model identifiers (GPT-5, RTX 5090), version tokens (v2.3.1, 3.5), or proper noun recency indicators
		versionPattern: regexp.MustCompile(`\b([A-Z]{2,}-?\d{1,4})\b|\b([A-Z][a-zA-Z0-9-]*\s+)+(?:v?\d+(\.\d+)+|v\d+|\d{1,4}|[A-Z]+-?\d{1,4}|latest|newest|current|next-gen)\b|\b(?:latest|newest|current|next-gen)\s+([A-Z][a-zA-Z0-9-]*)`),

		// Matches the current year and adjacent years
		yearPattern: regexp.MustCompile(yearPatternStr),

		// Matches CEO of X or X's CEO with exact case matching for the proper noun subject
		rolePattern:     regexp.MustCompile(`\b(?i:CEO|CTO|CFO|COO|President|Prime Minister|Chair(?:person|man|woman)?|Director)\s+of\s+([A-Z][a-zA-Z0-9&.\-]+(?:\s+[A-Z][a-zA-Z0-9&.\-]+)*)`),
		rolePatternSubj: regexp.MustCompile(`\b([A-Z][a-zA-Z0-9&.\-]+(?:\s+[A-Z][a-zA-Z0-9&.\-]+)*)'s\s+(?i:CEO|CTO|CFO|COO|President|Prime Minister|Chair(?:person|man|woman)?|Director)\b`),

		// Matches currency adjacent to numbers or price keywords
		pricePattern:    regexp.MustCompile(`(?i)(?:current|now|as of|latest|today).*?[$€£¥]\s*\d+([,.]\d+)?([kKmMbBtT]|billion|million|trillion)?|[$€£¥]\s*\d+([,.]\d+)?([kKmMbBtT]|billion|million|trillion)?.*?(?:current|now|as of|latest|today)|[$€£¥]\s*\d+(?:,\d{3})*(?:\.\d+)?\s*(?:[kKmMbBtT]|billion|million|trillion)?|(?i)\b(?:current price|current valuation)\b`),
		priceRawPattern: regexp.MustCompile(`[$€£¥]\s*\d+(?:,\d{3})*(?:\.\d+)?\s*(?:[kKmMbBtT]|billion|million|trillion)?`),
	}
}

var priceOfSubjectPattern = regexp.MustCompile(`(?i)\b(?:price|valuation)\s+(?:of|for)\s+([A-Z][a-zA-Z0-9&.\-]+(?:\s+[A-Z][a-zA-Z0-9&.\-]+)*)`)
var priceLeadingSubjectPattern = regexp.MustCompile(`(?i)^([A-Z][a-zA-Z0-9&.\-]+(?:\s+[A-Z][a-zA-Z0-9&.\-]+)*)\s+(?:price|current\s+price|valuation|is|reached|costs|hit)`)

func extractPriceSubject(claim string) string {
	if m := priceOfSubjectPattern.FindStringSubmatch(claim); len(m) >= 2 {
		return strings.TrimSpace(m[1])
	}
	if m := priceLeadingSubjectPattern.FindStringSubmatch(claim); len(m) >= 2 {
		return strings.TrimSpace(m[1])
	}
	// Fallback to first capitalized proper noun before the currency symbol
	parts := strings.Fields(claim)
	for _, p := range parts {
		clean := strings.Trim(p, ",.!'\"()[]")
		if len(clean) > 0 && clean[0] >= 'A' && clean[0] <= 'Z' && !strings.EqualFold(clean, "The") && !strings.EqualFold(clean, "As") {
			return clean
		}
	}
	return "price"
}

func extractVersionSubject(claim string, match string) string {
	fields := strings.Fields(match)
	if len(fields) > 1 {
		return strings.Join(fields[:len(fields)-1], " ")
	}
	if len(fields) == 1 {
		return fields[0]
	}
	return strings.TrimSpace(match)
}

func extractYearSubject(claim string, match string) string {
	parts := strings.Fields(claim)
	for _, p := range parts {
		clean := strings.Trim(p, ",.!'\"()[]")
		if len(clean) > 0 && clean[0] >= 'A' && clean[0] <= 'Z' && !strings.EqualFold(clean, "As") && !strings.EqualFold(clean, "The") && !strings.EqualFold(clean, "In") {
			return clean
		}
	}
	return "status"
}

// Detect returns the first, highest-priority detected entity:
// Priority order: Price > Executive > Version > Year > EntityUnknown
func (d *EntityDetector) Detect(claim string) DetectedEntity {
	// 1. Price check (highest priority)
	if d.pricePattern.MatchString(claim) {
		raw := ""
		if m := d.priceRawPattern.FindString(claim); m != "" {
			raw = strings.TrimSpace(m)
		} else {
			raw = "price"
		}
		subject := extractPriceSubject(claim)
		return DetectedEntity{
			Type:     EntityPrice,
			Subject:  subject,
			RawMatch: raw,
		}
	}

	// 2. Executive check
	if m := d.rolePattern.FindStringSubmatch(claim); len(m) >= 2 {
		return DetectedEntity{
			Type:     EntityExecutive,
			Subject:  strings.TrimSpace(m[1]),
			RawMatch: strings.TrimSpace(m[0]),
		}
	}
	if m := d.rolePatternSubj.FindStringSubmatch(claim); len(m) >= 2 {
		return DetectedEntity{
			Type:     EntityExecutive,
			Subject:  strings.TrimSpace(m[1]),
			RawMatch: strings.TrimSpace(m[0]),
		}
	}

	// 3. Version check
	if m := d.versionPattern.FindString(claim); m != "" {
		subj := extractVersionSubject(claim, m)
		return DetectedEntity{
			Type:     EntityVersion,
			Subject:  subj,
			RawMatch: strings.TrimSpace(m),
		}
	}

	// 4. Year check
	if m := d.yearPattern.FindString(claim); m != "" {
		subj := extractYearSubject(claim, m)
		return DetectedEntity{
			Type:     EntityYear,
			Subject:  subj,
			RawMatch: strings.TrimSpace(m),
		}
	}

	// Fast-path fallback for plain text keywords
	lower := strings.ToLower(claim)
	if strings.Contains(lower, "current valuation") || strings.Contains(lower, "current price") {
		return DetectedEntity{
			Type:     EntityPrice,
			Subject:  extractPriceSubject(claim),
			RawMatch: "price",
		}
	}

	return DetectedEntity{
		Type:     EntityUnknown,
		Subject:  "",
		RawMatch: "",
	}
}

// IsFreshnessSensitive returns true if the claim text matches known patterns
// that are highly likely to go stale quickly (versions, leadership, current prices).
func (d *EntityDetector) IsFreshnessSensitive(claim string) bool {
	return d.Detect(claim).Type != EntityUnknown
}

var nonAlphanumericPattern = regexp.MustCompile(`[^a-zA-Z0-9]+`)

func normalizeToken(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	s = nonAlphanumericPattern.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

// CacheToken returns a deterministic token for the entity cache that never returns empty string.
func CacheToken(e DetectedEntity, claim string) string {
	switch e.Type {
	case EntityVersion, EntityYear:
		tok := normalizeToken(e.RawMatch)
		if tok != "" {
			return tok
		}
		tok = normalizeToken(e.Subject)
		if tok != "" {
			return tok
		}
		sum := sha1.Sum([]byte(normalizeToken(claim)))
		return "claim:" + hex.EncodeToString(sum[:8])
	case EntityExecutive:
		subj := normalizeToken(e.Subject)
		if subj == "" {
			subj = normalizeToken(e.RawMatch)
		}
		return "exec:" + subj
	case EntityPrice:
		subj := normalizeToken(e.Subject)
		if subj == "" {
			subj = "price"
		}
		return "price:" + subj + ":" + timecontext.Now().UTC().Format("2006-01-02")
	default:
		sum := sha1.Sum([]byte(normalizeToken(claim)))
		return "claim:" + hex.EncodeToString(sum[:8])
	}
}

// BuildVerificationQuery formats a targeted search query for second-source verification.
func BuildVerificationQuery(e DetectedEntity) string {
	switch e.Type {
	case EntityVersion:
		subj := e.Subject
		if subj == "" {
			subj = e.RawMatch
		}
		return fmt.Sprintf("%s current latest version", subj)
	case EntityYear:
		subj := e.Subject
		if subj == "" {
			subj = e.RawMatch
		}
		return fmt.Sprintf("%s current status %s", subj, e.RawMatch)
	case EntityExecutive:
		raw := e.RawMatch
		if raw == "" {
			raw = fmt.Sprintf("CEO of %s", e.Subject)
		}
		return fmt.Sprintf("who is the current %s", raw)
	case EntityPrice:
		subj := e.Subject
		if subj == "" || subj == "price" {
			subj = "current"
		}
		return fmt.Sprintf("%s current price today", subj)
	default:
		subj := e.Subject
		if subj == "" {
			subj = e.RawMatch
		}
		return fmt.Sprintf("%s latest news", subj)
	}
}

// ExtractEntity tries to pull out the main entity string for backwards compatibility.
func (d *EntityDetector) ExtractEntity(claim string) string {
	detected := d.Detect(claim)
	if detected.Subject != "" {
		return detected.Subject
	}
	if detected.RawMatch != "" {
		return detected.RawMatch
	}
	return claim
}

// ExtractVersionToken pulls out the version or cache token for backwards compatibility.
func (d *EntityDetector) ExtractVersionToken(claim string) string {
	detected := d.Detect(claim)
	return CacheToken(detected, claim)
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
				rewritten = strings.Replace(rewritten, match, strconv.Itoa(currentYear), 1)
				changed = true
			}
		}
	}
	return rewritten, changed
}
