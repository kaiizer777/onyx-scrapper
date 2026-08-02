package news

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DefaultWindow is the fallback recency duration string when none is supplied or recognized.
const DefaultWindow = "24h"

// Window represents a parsed recency window for news fetching.
type Window struct {
	RawPhrase      string        `json:"raw_phrase"`
	Duration       time.Duration `json:"duration"`
	GoogleNewsWhen string        `json:"google_news_when"`
	Since          time.Time     `json:"since"`
}

var (
	// Number + unit regex (e.g., "last 24 hours", "3 days", "past 2 weeks", "48h")
	numUnitRegex = regexp.MustCompile(`(?i)\b(?:in\s+the\s+|over\s+the\s+|the\s+)?(?:last|past)?\s*(\d+)\s*(hours?|hrs?|h|days?|d|weeks?|wks?|w|months?|m|years?|yrs?|y)\b`)
	// Standalone unit without number (e.g., "past hour", "last day", "past week", "this month", "last year")
	unitOnlyRegex = regexp.MustCompile(`(?i)\b(?:in\s+the\s+|over\s+the\s+|the\s+)?(?:last|past|this)\s+(hour|hr|day|week|wk|month|year|yr)\b`)
	// Shorthand string regex (e.g., "24h", "7d", "1m", "1y")
	shorthandRegex = regexp.MustCompile(`^(?i)\s*(\d+)\s*([hdwy]|hr|day|wk|m|yr)s?\s*$`)
	// googleNewsOperatorRegex matches any Google News RSS operator
	// token. The recency parser MUST strip these before regex
	// matching, otherwise a hostile input like
	//   "last 24h when:1d"
	// would have the trailing "1d" matched as a shorthand duration
	// and emitted as the resolved window, rather than ignored.
	//
	// Phase 11 hardening: the parser must never let a Google News
	// operator literal (including but not limited to `when:`) be
	// interpreted as a duration. Operator strips are done in
	// sanitizeTriggerText.
	googleNewsOperatorRegex = regexp.MustCompile(`(?i)\b(when|location|topic|source|site|allinurl|inurl|intitle|sortby)\s*:\s*[^\s,]+`)
	// shellMetaRegex matches shell metacharacters that have no
	// business in a recency phrase. Used to strip noise so a
	// payload like `last 24h; rm -rf /` doesn't accidentally
	// produce a parse error AND so the parser doesn't have to
	// know about every possible metachar.
	shellMetaRegex = regexp.MustCompile(`[<>|&;$\x00-\x1f]`)
)

// sanitizeTriggerText strips noise and Google News operator
// tokens from a trigger message BEFORE recency-parsing it. This
// is the Phase 11 injection guard: any operator literal (`when:`,
// `location:`, `site:`, etc.) the user typed is removed before
// the duration regex runs, so a hostile string like
//   `last 24h when:1d`
// never has its `1d` accepted as a duration candidate.
//
// The function is a defense-in-depth measure; the primary
// guarantee is that BuildGoogleNewsQuery never sees the trigger
// text (it only sees KeywordsCSV from the profile + the
// parser-resolved GoogleNewsWhen). This function prevents
// *ambiguity*, not injection: if a user types
//   `past week when:1d`
// the parser must NOT silently switch to 1d — it should keep
// `past week` and ignore the trailing operator.
func sanitizeTriggerText(input string) string {
	s := googleNewsOperatorRegex.ReplaceAllString(input, " ")
	s = shellMetaRegex.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// ParseRecencyWindow extracts a duration window from free-text input relative to time.Now().
func ParseRecencyWindow(input string, defaultWindowStr string) Window {
	return ParseRecencyWindowAt(input, defaultWindowStr, time.Now())
}

// ParseRecencyWindowAt extracts a duration window relative to a given timestamp.
func ParseRecencyWindowAt(input string, defaultWindowStr string, now time.Time) Window {
	cleanInput := strings.TrimSpace(input)
	if cleanInput != "" {
		if win, ok := tryParseInput(cleanInput, now); ok {
			win.RawPhrase = cleanInput
			return win
		}
	}

	cleanDefault := strings.TrimSpace(defaultWindowStr)
	if cleanDefault != "" && cleanDefault != cleanInput {
		if win, ok := tryParseInput(cleanDefault, now); ok {
			win.RawPhrase = cleanInput
			if win.RawPhrase == "" {
				win.RawPhrase = cleanDefault
			}
			return win
		}
	}

	win, _ := buildWindow(24*time.Hour, "1d", now)
	win.RawPhrase = cleanInput
	if win.RawPhrase == "" {
		win.RawPhrase = DefaultWindow
	}
	return win
}

func tryParseInput(input string, now time.Time) (Window, bool) {
	// Phase 11: strip Google News operator literals and shell
	// metacharacters from the trigger text BEFORE regex matching.
	// See sanitizeTriggerText for the rationale.
	lower := strings.ToLower(sanitizeTriggerText(input))

	switch {
	case strings.Contains(lower, "today"):
		return buildWindow(24*time.Hour, "1d", now)
	case strings.Contains(lower, "yesterday"):
		return buildWindow(48*time.Hour, "2d", now)
	}

	if matches := numUnitRegex.FindStringSubmatch(lower); len(matches) >= 3 {
		n, err := strconv.Atoi(matches[1])
		if err == nil && n > 0 {
			unit := matches[2]
			if d, when, ok := parseUnitAndQuantity(n, unit); ok {
				return buildWindow(d, when, now)
			}
		}
	}

	if matches := unitOnlyRegex.FindStringSubmatch(lower); len(matches) >= 2 {
		unit := matches[1]
		if d, when, ok := parseUnitAndQuantity(1, unit); ok {
			return buildWindow(d, when, now)
		}
	}

	if matches := shorthandRegex.FindStringSubmatch(lower); len(matches) >= 3 {
		n, err := strconv.Atoi(matches[1])
		if err == nil && n > 0 {
			unit := matches[2]
			if d, when, ok := parseUnitAndQuantity(n, unit); ok {
				return buildWindow(d, when, now)
			}
		}
	}

	return Window{}, false
}

func parseUnitAndQuantity(n int, unit string) (time.Duration, string, bool) {
	unit = strings.ToLower(unit)
	switch unit {
	case "h", "hr", "hour", "hours":
		d := time.Duration(n) * time.Hour
		if n%24 == 0 && n >= 24 {
			return d, fmt.Sprintf("%dd", n/24), true
		}
		return d, fmt.Sprintf("%dh", n), true
	case "d", "day", "days":
		d := time.Duration(n) * 24 * time.Hour
		return d, fmt.Sprintf("%dd", n), true
	case "w", "wk", "week", "weeks":
		days := n * 7
		d := time.Duration(days) * 24 * time.Hour
		return d, fmt.Sprintf("%dd", days), true
	case "m", "month", "months":
		days := n * 30
		d := time.Duration(days) * 24 * time.Hour
		return d, fmt.Sprintf("%dm", n), true
	case "y", "yr", "year", "years":
		days := n * 365
		d := time.Duration(days) * 24 * time.Hour
		return d, fmt.Sprintf("%dy", n), true
	}
	return 0, "", false
}

func buildWindow(d time.Duration, whenTag string, now time.Time) (Window, bool) {
	when := whenTag
	if !strings.HasPrefix(when, "when:") {
		when = "when:" + whenTag
	}
	return Window{
		Duration:       d,
		GoogleNewsWhen: when,
		Since:          now.Add(-d),
	}, true
}
