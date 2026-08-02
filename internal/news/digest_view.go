package news

import (
	"strings"
	"time"

	"github.com/kaiizer777/onyx-scrapper/internal/config"
)

// DigestView is the render-ready, surface-independent view of a news
// digest. It is the single source of truth that CLI, Telegram, and the
// Web UI renderers consume. Each surface adapts the SAME view to its own
// styling — field separation, per-field window, and item grouping are
// guaranteed by the view itself, not by each renderer re-deriving them.
//
// Why this exists: Phase 9 requires every field section to always show
// (field name, resolved window, item count) and to never merge two
// fields' items into a shared list. Before DigestView, each renderer
// derived its own section header from the raw NewsDigest, and they
// drifted (the Web UI showed "N articles", the CLI showed "N items",
// neither stamped the resolved window into the per-field header). This
// type centralizes the contract.
type DigestView struct {
	// RunID is the news run primary key.
	RunID int64
	// ProfileID is the profile that produced the digest.
	ProfileID int64
	// Window is the resolved recency phrase the run used, e.g.
	// "past 3 days" or "24h". Always non-empty; falls back to the
	// configured default if the trigger had no parseable duration.
	Window string
	// GeneratedAt is when the digest object was built.
	GeneratedAt time.Time
	// Fields is the ordered list of field sections, in profile
	// priority order. Never merged — every entry is a self-contained
	// section that must be rendered as its own block.
	Fields []FieldView
}

// FieldView is the render-ready view of a single profile field's news
// section. Each field is self-contained: it carries its own resolved
// window, item count, and items so renderers do not have to reach back
// into the parent digest to render a section header.
type FieldView struct {
	// FieldID is the profile_field row id.
	FieldID int64
	// FieldName is the display label set in the profile, e.g.
	// "AI/ML" or "Gaming". Surface renderers may uppercase it.
	FieldName string
	// PriorityOrder matches profile_fields.priority_order. Lower
	// numbers come first in the digest.
	PriorityOrder int
	// Window is the same resolved window as the parent digest, but
	// stamped on the field so the per-field section header can show
	// it without an extra parameter.
	Window string
	// ItemCount is len(Items), precomputed for the section header.
	ItemCount int
	// Items is the list of news items for this field, ordered
	// most-relevant first. May be empty — renderers must handle
	// the "no items" state for the field.
	Items []ItemView
}

// ItemView is the render-ready view of a single news article.
type ItemView struct {
	// Headline is the article title, ready to display.
	Headline string
	// Takeaway is the one-line summary or LLM-generated takeaway
	// for the article. May be empty.
	Takeaway string
	// Body is the full cleaned article text, suitable for
	// in-place rendering so the reader does not have to click
	// through to the source URL. May be empty if neither the
	// RSS snippet nor a full-text fetch was available.
	Body string
	// URL is the article's source URL. Renderers must escape this
	// for both attribute and text contexts.
	URL string
	// Source is the publisher name, e.g. "Tech Daily" or
	// "SearXNG News". May be empty.
	Source string
	// PublishedAt is the article's publication timestamp, if known.
	// May be nil — renderers should fall back gracefully.
	PublishedAt *time.Time
	// RelativeTime is the precomputed "N hours ago" / "N days ago"
	// string derived from PublishedAt at view-build time. Empty
	// when PublishedAt is nil or zero. Pre-computing here keeps
	// the three renderers identical and avoids a time.Now() race
	// between the per-field headers.
	RelativeTime string
	// FetchIntegrity is the quality layer's verdict string, e.g.
	// "ok" / "high" / "partial". May be empty for older runs.
	FetchIntegrity string
	// ConfidenceFlag is the cross-source corroboration verdict
	// (e.g. "single-source", "unverified"). May be empty.
	ConfidenceFlag string
}

// BuildDigestView converts a NewsDigest (the data-layer shape produced
// by the orchestrator + summarizer) into a DigestView (the render-layer
// shape consumed by every surface formatter). It is the only place that
// stamps the resolved window onto every field, pre-computes relative
// times, and freezes the section structure.
//
// Thin convenience wrapper around BuildDigestViewWithCap that applies
// the default per-field cap (config.DefaultNewsItemsPerField).
// Kept for backwards compatibility with the existing call sites
// in formatter_*.go and the test suite.
//
// Returns nil if digest is nil.
func BuildDigestView(digest *NewsDigest) *DigestView {
	return BuildDigestViewWithCap(digest, 0)
}

// BuildDigestViewWithCap is the full control surface. itemsPerField
// caps the number of items rendered per field. Pass 0 to use the
// default (config.DefaultNewsItemsPerField). Values above
// config.HardNewsItemsPerField are clamped to the hard cap so a
// misconfigured caller can't blow past the Telegram 4096-char
// single-message limit on a single field.
//
// The cap is the final word on what the user sees — the orchestrator
// may have fetched more to give the LLM a richer ranking pool, and
// this pass truncates to the display count.
//
// Returns nil if digest is nil.
func BuildDigestViewWithCap(digest *NewsDigest, itemsPerField int) *DigestView {
	if digest == nil {
		return nil
	}
	if itemsPerField <= 0 {
		itemsPerField = config.DefaultNewsItemsPerField
	}
	if itemsPerField > config.HardNewsItemsPerField {
		itemsPerField = config.HardNewsItemsPerField
	}

	window := strings.TrimSpace(digest.Window)
	view := &DigestView{
		RunID:       digest.RunID,
		ProfileID:   digest.ProfileID,
		Window:      window,
		GeneratedAt: digest.GeneratedAt,
		Fields:      make([]FieldView, 0, len(digest.Fields)),
	}
	// Apply recency cutoff when WindowSince is set (non-zero). Items
	// with nil PublishedAt are always kept — RSS feeds that don't
	// ship dates still count. This is the second pass: the
	// orchestrator already filtered at fetch time, but this pass
	// re-applies the same cutoff when a saved run is re-rendered
	// from the DB with a different window config.
	windowSince := digest.WindowSince

	for _, f := range digest.Fields {
		fv := FieldView{
			FieldID:       f.FieldID,
			FieldName:     strings.TrimSpace(f.FieldName),
			PriorityOrder: f.PriorityOrder,
			Window:        window,
			Items:         make([]ItemView, 0, len(f.Items)),
		}
		for _, it := range f.Items {
			// Strict recency cutoff: drop dated items older than the window.
			if !windowSince.IsZero() && it.PublishedAt != nil && it.PublishedAt.Before(windowSince) {
				continue
			}
			iv := ItemView{
				Headline:       strings.TrimSpace(it.Headline),
				Takeaway:       strings.TrimSpace(it.Takeaway),
				Body:           strings.TrimSpace(it.Body),
				URL:            strings.TrimSpace(it.URL),
				Source:         strings.TrimSpace(it.Source),
				PublishedAt:    it.PublishedAt,
				FetchIntegrity: strings.TrimSpace(it.FetchIntegrity),
				ConfidenceFlag: strings.TrimSpace(it.ConfidenceFlag),
			}
			if iv.Headline == "" {
				iv.Headline = iv.URL
			}
			if it.PublishedAt != nil && !it.PublishedAt.IsZero() {
				iv.RelativeTime = formatRelativeTime(*it.PublishedAt)
			}
			fv.Items = append(fv.Items, iv)
		}
		// Apply the per-field display cap. The orchestrator may
		// have fetched more (giving the LLM a richer ranking
		// pool); this is the final word on what the user sees.
		if len(fv.Items) > itemsPerField {
			fv.Items = fv.Items[:itemsPerField]
		}
		fv.ItemCount = len(fv.Items)
		view.Fields = append(view.Fields, fv)
	}

	return view
}
