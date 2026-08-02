package news

import (
	"regexp"
	"strings"
)

// looksLikeMarkdownLinkOnly reports whether the given summary is
// effectively just a single markdown link with an optional trailing
// source tag (the Google News RSS <description> shape), with no
// real prose content. The full-text pull should always be
// attempted for such items because the snippet carries no
// readable body, only the encoded tracking URL.
func looksLikeMarkdownLinkOnly(summary string) bool {
	s := strings.TrimSpace(summary)
	if s == "" {
		return true
	}
	// Strip an optional leading "[title](url)" markdown link.
	linkRE := regexp.MustCompile(`^\[[^\]]*\]\([^\)]*\)\s*`)
	stripped := linkRE.ReplaceAllString(s, "")
	// What remains is usually the source name and a small tag,
	// e.g. "Hartford Courant" or "Some publisher".
	remainder := strings.TrimSpace(stripped)
	if len(remainder) > 80 {
		return false
	}
	return true
}
