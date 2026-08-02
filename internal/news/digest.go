package news

import (
	"time"

	"github.com/kaiizer777/onyx-scrapper/internal/store"
)

// DigestItem represents a single news item formatted for digest display,
// including a concise takeaway and quality/corroboration flags.
type DigestItem struct {
	Headline string `json:"headline"`
	// Takeaway is the LLM-generated (or sentence-split fallback)
	// one- or two-sentence summary shown as the article "lede".
	Takeaway string `json:"takeaway"`
	// Body is the full cleaned article text pulled from the
	// source (or the RSS snippet when no full-text pull was
	// possible). The Web UI / Telegram / CLI renderers should
	// display this inline so the operator does not have to
	// click through to the source URL. May be empty if neither
	// the snippet nor a full-text fetch was available.
	Body             string     `json:"body,omitempty"`
	URL              string     `json:"url"`
	Source           string     `json:"source"`
	PublishedAt      *time.Time `json:"published_at,omitempty"`
	ConfidenceFlag   string     `json:"confidence_flag,omitempty"`
	FetchIntegrity   string     `json:"fetch_integrity"`
	OriginalNewsItem store.NewsItem `json:"original_news_item"`
}

// FieldDigest represents a section of the digest dedicated to a specific profile field.
type FieldDigest struct {
	FieldID       int64        `json:"field_id"`
	FieldName     string       `json:"field_name"`
	PriorityOrder int          `json:"priority_order"`
	Items         []DigestItem `json:"items"`
}

// NewsDigest represents the complete multi-field digest generated for a news run.
type NewsDigest struct {
	RunID       int64         `json:"run_id"`
	ProfileID   int64         `json:"profile_id"`
	Window      string        `json:"window"`
	// WindowSince is the resolved cutoff timestamp the run used
	// (now − window). BuildDigestView uses it to apply the strict
	// recency filter at view-build time. Zero value disables the
	// filter — keeps existing tests and saved-runs that don't
	// stamp it working unchanged.
	WindowSince  time.Time     `json:"window_since,omitempty"`
	Fields      []FieldDigest `json:"fields"`
	GeneratedAt time.Time     `json:"generated_at"`
}
