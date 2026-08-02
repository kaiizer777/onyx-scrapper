package news

import (
	"sort"

	"github.com/kaiizer777/onyx-scrapper/internal/config"
	"github.com/kaiizer777/onyx-scrapper/internal/store"
)

// FieldLookup returns the metadata for a profile field by id. The
// server-side HTML handler uses this to enrich per-field sections
// without re-implementing the field-name resolution.
type FieldLookup func(fieldID int64) (name string, priority int, ok bool)

// BuildDigestFromStoreItems reconstructs a NewsDigest from persisted
// store.NewsItem rows for a given run. This is used by the HTML
// endpoint (GET /news/{id}/html) to re-render the digest after the run
// has completed and the in-memory NewsDigest has been discarded.
//
// The output is a faithful equivalent of the live digest: same
// per-field grouping, same ordering, same per-item summary/takeaway
// (using the same fallback-takeaway rule the live summarizer uses),
// same field-name resolution. The LLM-generated takeaway is not
// persisted in the database today, so for completed runs the takeaway
// is the best-effort summary-derived one. The CLI and Telegram paths
// still see the LLM takeway at run time; this path is intentionally
// for the post-run read-only HTML view.
//
// itemsPerField caps the rendered list per field. Pass 0 for the
// default (config.DefaultNewsItemsPerField). Values above
// config.HardNewsItemsPerField are clamped to the hard cap.
func BuildDigestFromStoreItems(
	run *store.NewsRun,
	items []store.NewsItem,
	lookup FieldLookup,
	itemsPerField int,
) *NewsDigest {
	if run == nil {
		return nil
	}

	type acc struct {
		fieldID    int64
		fieldName  string
		priority   int
		items      []store.NewsItem
	}

	bucket := map[int64]*acc{}
	order := []int64{}

	for _, it := range items {
		a, seen := bucket[it.FieldID]
		if !seen {
			name, prio, ok := lookup(it.FieldID)
			if !ok {
				name = ""
				prio = 0
			}
			a = &acc{
				fieldID:   it.FieldID,
				fieldName: name,
				priority:  prio,
			}
			bucket[it.FieldID] = a
			order = append(order, it.FieldID)
		}
		a.items = append(a.items, it)
	}

	// Stable order: by priority asc, then by first-seen field id asc.
	// We avoid sort.SliceStable to keep the import list lean.
	ordered := make([]int64, 0, len(order))
	seen := map[int64]bool{}
	for _, fid := range order {
		if !seen[fid] {
			ordered = append(ordered, fid)
			seen[fid] = true
		}
	}
	sort.Slice(ordered, func(i, j int) bool {
		pi := bucket[ordered[i]].priority
		pj := bucket[ordered[j]].priority
		if pi != pj {
			return pi < pj
		}
		return ordered[i] < ordered[j]
	})

	digest := &NewsDigest{
		RunID:     run.ID,
		ProfileID: run.ProfileID,
		Window:    run.Window,
	}
	if run.CompletedAt != nil {
		digest.GeneratedAt = *run.CompletedAt
	} else {
		digest.GeneratedAt = run.StartedAt
	}

	for _, fid := range ordered {
		a := bucket[fid]
		fd := FieldDigest{
			FieldID:       a.fieldID,
			FieldName:     a.fieldName,
			PriorityOrder: a.priority,
		}
		for _, it := range a.items {
			// Prefer the LLM-generated short body that was persisted
			// to the DB at summarization time. Fall back to
			// extractProseBody (heuristic) only for pre-migration runs
			// (where short_body = "") or items where LLM generation
			// failed.
			body := it.ShortBody
			if body == "" {
				body = shortBodyFromSummary(it.Summary)
			}
			fd.Items = append(fd.Items, DigestItem{
				Headline:       it.Title,
				Takeaway:       fallbackTakeawayFromSummary(it.Summary),
				Body:           body,
				URL:            it.URL,
				Source:         it.Source,
				PublishedAt:    it.PublishedAt,
				FetchIntegrity: it.FetchIntegrity,
			})
		}
		// Apply the same per-field display cap the live view path
		// applies. The orchestrator may have fetched more to give
		// the LLM a richer ranking pool; this is the final word on
		// what the user sees, so a saved run re-rendered later
		// (with a different config.items_per_field) still honors
		// the configured cap.
		cap := itemsPerField
		if cap <= 0 {
			cap = config.DefaultNewsItemsPerField
		}
		if cap > config.HardNewsItemsPerField {
			cap = config.HardNewsItemsPerField
		}
		if len(fd.Items) > cap {
			fd.Items = fd.Items[:cap]
		}
		digest.Fields = append(digest.Fields, fd)
	}

	return digest
}

// shortBodyFromSummary produces a clean 2-sentence prose excerpt from a
// raw crawled summary for saved-run re-renders. Delegates to
// extractProseBody which is significantly more aggressive than
// cleanBodyForMarkdownReport at stripping nav/markdown cruft.
func shortBodyFromSummary(summary string) string {
	body := extractProseBody(summary)
	if body == "" {
		// extractProseBody found no qualifying lines (e.g. the whole
		// article was very short). Return empty — the view layer renders
		// nothing rather than noise.
		return ""
	}
	return body
}
