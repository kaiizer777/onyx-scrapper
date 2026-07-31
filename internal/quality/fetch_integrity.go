package quality

import (
	"strings"
)

type FetchIntegrity string

const (
	FetchOK                FetchIntegrity = "ok"
	FetchBlocked           FetchIntegrity = "blocked"            // challenge page, CAPTCHA, 403
	FetchEmpty             FetchIntegrity = "empty"              // below min_content_chars
	FetchTimeout           FetchIntegrity = "timeout"
	FetchFallbackRecovered FetchIntegrity = "fallback_recovered" // priority chain rescued it
	FetchPartial           FetchIntegrity = "partial"            // content present but truncated/malformed
)

const MinContentChars = 250

func AnalyzeFetchIntegrity(rawHTML string, cleanText string, provider string, fetchErr error) FetchIntegrity {
	if fetchErr != nil {
		errStr := strings.ToLower(fetchErr.Error())
		if strings.Contains(errStr, "timeout") || strings.Contains(errStr, "deadline") || strings.Contains(errStr, "context canceled") {
			return FetchTimeout
		}
		// Consider other errors as blocked, especially if we have no raw HTML
		if rawHTML == "" {
			return FetchBlocked
		}
	}

	if len(strings.TrimSpace(cleanText)) < MinContentChars {
		lowerHTML := strings.ToLower(rawHTML)
		if strings.Contains(lowerHTML, "cloudflare") || strings.Contains(lowerHTML, "challenge") || strings.Contains(lowerHTML, "captcha") || strings.Contains(lowerHTML, "verify you are human") {
			return FetchBlocked
		}
		return FetchEmpty
	}

	// FetchPartial heuristics
	lowerHTML := strings.ToLower(rawHTML)
	
	// Check for missing </html> tag (unless it's JSON or not HTML, but we assume we fetch HTML mostly)
	if strings.Contains(lowerHTML, "<html") && !strings.Contains(lowerHTML, "</html>") {
		return FetchPartial
	}
	
	if strings.Contains(lowerHTML, "enable javascript to continue") || 
	   strings.Contains(lowerHTML, "please enable javascript") || 
	   strings.Contains(lowerHTML, "javascript is required") ||
	   strings.Contains(lowerHTML, "turn on javascript") {
		return FetchPartial
	}

	// If fetchErr was present but we somehow had enough content (e.g. partial read error)
	if fetchErr != nil {
		return FetchPartial
	}

	if provider == "scraperapi" || provider == "tinyfish" || provider == "jina" {
		return FetchFallbackRecovered
	}

	return FetchOK
}
