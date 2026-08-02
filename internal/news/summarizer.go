package news

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/kaiizer777/onyx-scrapper/internal/discovery"
	"github.com/kaiizer777/onyx-scrapper/internal/llm"
	"github.com/kaiizer777/onyx-scrapper/internal/quality"
	"github.com/kaiizer777/onyx-scrapper/internal/store"
)

type Summarizer struct {
	llmClient   *llm.Client
	reranker    *discovery.JinaReranker
	authManager *quality.AuthorityManager
	budget      *quality.Budget
	st          *store.Store
}

func NewSummarizer(
	llmClient *llm.Client,
	reranker *discovery.JinaReranker,
	authManager *quality.AuthorityManager,
	budget *quality.Budget,
	st *store.Store,
) *Summarizer {
	return &Summarizer{
		llmClient:   llmClient,
		reranker:    reranker,
		authManager: authManager,
		budget:      budget,
		st:          st,
	}
}

// SummarizeField processes news items for a single profile field: reranking, confidence flagging, LLM takeaway generation, and LLM short-body replacement.
func (s *Summarizer) SummarizeField(ctx context.Context, field store.ProfileField, items []store.NewsItem) (FieldDigest, error) {
	fieldDigest := FieldDigest{
		FieldID:       field.ID,
		FieldName:     field.FieldName,
		PriorityOrder: field.PriorityOrder,
		Items:         []DigestItem{},
	}

	if len(items) == 0 {
		return fieldDigest, nil
	}

	// 1. Optional Jina Reranker Pass
	orderedItems := s.rerankItems(ctx, field, items)

	// 2. Cross-Source Corroboration & Confidence Flagging
	confidenceFlags := s.computeConfidenceFlags(orderedItems)

	// 3. Takeaway Generation Pass (MiMo V2.5 with Fallback)
	takeaways := s.generateTakeaways(ctx, orderedItems)

	// 4. LLM Short-Body Generation Pass — produces a 2-3 sentence
	// body for each item. This overwrites the raw full-text body
	// with the LLM-short version so all renderers show only the
	// compact summary, not a full article dump.
	shortBodies := s.GenerateShortBodies(ctx, orderedItems)

	// 4a. Persist short bodies to the DB so saved-run re-renders
	// can use the LLM body directly instead of extractProseBody.
	// Best-effort: failures are logged but don't abort the run.
	if s.st != nil {
		for i, item := range orderedItems {
			if item.ID > 0 && shortBodies[i] != "" {
				if err := s.st.UpdateNewsItemShortBody(item.ID, shortBodies[i]); err != nil {
					slog.Warn("failed to persist short_body", "item_id", item.ID, "error", err)
				}
			}
		}
	}

	// 5. Assemble Digest Items
	for i, item := range orderedItems {
		takeaway := takeaways[i]
		if takeaway == "" {
			takeaway = s.fallbackTakeaway(item.Summary)
		}

		digestItem := DigestItem{
			Headline:         item.Title,
			Takeaway:         takeaway,
			// Body is now the LLM-generated 2-3 sentence summary
			// (or a 2-sentence fallback from the raw text).
			// The full raw text is accessible via OriginalNewsItem.Summary.
			Body:             shortBodies[i],
			URL:              item.URL,
			Source:           item.Source,
			PublishedAt:      item.PublishedAt,
			ConfidenceFlag:   confidenceFlags[i],
			FetchIntegrity:   item.FetchIntegrity,
			OriginalNewsItem: item,
		}
		fieldDigest.Items = append(fieldDigest.Items, digestItem)
	}

	return fieldDigest, nil
}

// GenerateShortBodies produces a 2-3 sentence (≤60 word) body for each
// item via a single batch LLM call. Falls back to the first 2 sentences
// of the existing item.Summary if the LLM is unavailable or parsing fails.
// NOTE: does NOT call budget.TryAcquire — the budget slot is shared with
// generateTakeaways which already acquired it in the same SummarizeField call.
func (s *Summarizer) GenerateShortBodies(ctx context.Context, items []store.NewsItem) []string {
	bodies := make([]string, len(items))
	// Pre-fill fallbacks so any early-return path is safe.
	for i, item := range items {
		bodies[i] = s.fallbackShortBody(item.Summary)
	}
	if len(items) == 0 {
		return bodies
	}
	if s.llmClient == nil {
		return bodies
	}

	type bodyRequest struct {
		I      int    `json:"i"`
		Source string `json:"source"`
		Title  string `json:"title"`
		Body   string `json:"body"`
	}
	type bodyResponse struct {
		I    int    `json:"i"`
		Body string `json:"body"`
	}

	reqs := make([]bodyRequest, len(items))
	for i, item := range items {
		reqs[i] = bodyRequest{
			I:      i,
			Source: item.Source,
			Title:  item.Title,
			Body:   item.Summary,
		}
	}

	reqJSON, err := json.Marshal(reqs)
	if err != nil {
		slog.Warn("GenerateShortBodies: failed to marshal request", "error", err)
		return bodies
	}

	prompt := `Rewrite each article into exactly 2-3 sentences (≤60 words) capturing the core news. ` +
		`Return a JSON array where each element has "i" (the original index) and "body" (the rewritten text). ` +
		`Do not include any other keys or markdown. Input: ` + string(reqJSON)

	messages := []llm.Message{
		{Role: "system", Content: "You are a senior news editor. Respond ONLY with a valid JSON array of objects with keys \"i\" and \"body\"."},
		{Role: "user", Content: prompt},
	}

	resp, err := s.llmClient.Chat(ctx, messages)
	if err != nil {
		slog.Warn("GenerateShortBodies: LLM call failed, using fallbacks", "error", err)
		return bodies
	}

	// Extract the JSON array from the response (strip markdown fences if present).
	cleanResp := strings.TrimSpace(resp)
	if idx := strings.Index(cleanResp, "["); idx != -1 {
		if endIdx := strings.LastIndex(cleanResp, "]"); endIdx != -1 && endIdx > idx {
			cleanResp = cleanResp[idx : endIdx+1]
		}
	}

	var parsed []bodyResponse
	if err := json.Unmarshal([]byte(cleanResp), &parsed); err != nil {
		slog.Warn("GenerateShortBodies: unmarshal failed, using fallbacks", "raw", resp, "error", err)
		return bodies
	}

	for _, r := range parsed {
		if r.I >= 0 && r.I < len(bodies) && strings.TrimSpace(r.Body) != "" {
			bodies[r.I] = strings.TrimSpace(r.Body)
		}
	}

	return bodies
}

// fallbackShortBody extracts a clean 2-sentence prose summary from a
// raw crawled body for use when the LLM is unavailable. Delegates to
// extractProseBody which strips markdown syntax, drops nav lines, and
// picks prose sentences >= 50 chars.
func (s *Summarizer) fallbackShortBody(summary string) string {
	body := extractProseBody(summary)
	if body == "" {
		return "No summary available for this article."
	}
	return body
}

// CompileDigest processes all fetched field news and returns a complete NewsDigest ordered by profile PriorityOrder.
func (s *Summarizer) CompileDigest(ctx context.Context, run *store.NewsRun, fetched []FetchedFieldNews) (*NewsDigest, error) {
	var fieldDigests []FieldDigest

	for _, f := range fetched {
		fd, err := s.SummarizeField(ctx, f.Field, f.Items)
		if err != nil {
			slog.Warn("Failed to summarize field news", "field", f.Field.FieldName, "error", err)
			fd = FieldDigest{
				FieldID:       f.Field.ID,
				FieldName:     f.Field.FieldName,
				PriorityOrder: f.Field.PriorityOrder,
				Items:         s.fallbackDigestItems(f.Items),
			}
		}
		fieldDigests = append(fieldDigests, fd)
	}

	// Sort fields by PriorityOrder ascending
	sort.Slice(fieldDigests, func(i, j int) bool {
		if fieldDigests[i].PriorityOrder == fieldDigests[j].PriorityOrder {
			return fieldDigests[i].FieldName < fieldDigests[j].FieldName
		}
		return fieldDigests[i].PriorityOrder < fieldDigests[j].PriorityOrder
	})

	digest := &NewsDigest{
		RunID:       run.ID,
		ProfileID:   run.ProfileID,
		Window:      run.Window,
		Fields:      fieldDigests,
		GeneratedAt: time.Now(),
	}

	return digest, nil
}

func (s *Summarizer) rerankItems(ctx context.Context, field store.ProfileField, items []store.NewsItem) []store.NewsItem {
	if s.reranker == nil || len(items) <= 1 {
		return items
	}

	docs := make([]string, len(items))
	for i, item := range items {
		docs[i] = fmt.Sprintf("%s: %s", item.Title, item.Summary)
	}

	query := fmt.Sprintf("Latest news on %s (%s)", field.FieldName, field.KeywordsCSV)
	ranked, err := s.reranker.Rerank(ctx, query, docs)
	if err != nil || len(ranked) == 0 {
		return items
	}

	// Sort items according to ranked order
	sort.SliceStable(ranked, func(i, j int) bool {
		return ranked[i].Score > ranked[j].Score
	})

	reordered := make([]store.NewsItem, 0, len(items))
	for _, r := range ranked {
		if r.Index >= 0 && r.Index < len(items) {
			reordered = append(reordered, items[r.Index])
		}
	}

	if len(reordered) == len(items) {
		return reordered
	}

	return items
}

func (s *Summarizer) computeConfidenceFlags(items []store.NewsItem) []string {
	flags := make([]string, len(items))
	if len(items) == 0 {
		return flags
	}

	domainCounts := make(map[string]int)
	domainsPerItem := make([]string, len(items))

	for i, item := range items {
		domain := extractDomain(item.URL)
		domainsPerItem[i] = domain
		if domain != "" {
			domainCounts[domain]++
		}
	}

	for i, item := range items {
		var tier quality.AuthorityTier = quality.TierUnknown
		if s.authManager != nil {
			tier = s.authManager.GetAuthorityTier(item.URL)
		}

		totalDomainCount := len(domainCounts)

		if totalDomainCount > 1 {
			if tier == quality.TierPrimary {
				flags[i] = "(corroborated — primary source)"
			} else {
				flags[i] = fmt.Sprintf("(corroborated: %d sources)", totalDomainCount)
			}
		} else {
			if tier == quality.TierPrimary {
				flags[i] = "(primary source)"
			} else {
				flags[i] = "(single source — unverified)"
			}
		}
	}

	return flags
}

func (s *Summarizer) generateTakeaways(ctx context.Context, items []store.NewsItem) []string {
	takeaways := make([]string, len(items))

	if s.llmClient == nil || len(items) == 0 {
		for i, item := range items {
			takeaways[i] = s.fallbackTakeaway(item.Summary)
		}
		return takeaways
	}

	if s.budget != nil && !s.budget.TryAcquire() {
		slog.Warn("Quality budget exhausted for news summarization, using fallback takeaways")
		for i, item := range items {
			takeaways[i] = s.fallbackTakeaway(item.Summary)
		}
		return takeaways
	}

	var sb strings.Builder
	sb.WriteString("Condense each of the following news articles into a short 1-2 sentence takeaway highlighting the key finding or development:\n\n")
	for i, item := range items {
		sb.WriteString(fmt.Sprintf("%d. Headline: %s\n   Source: %s\n   Summary: %s\n\n", i+1, item.Title, item.Source, item.Summary))
	}
	sb.WriteString(`Return a JSON array of string takeaways corresponding to the articles in exact order, format: ["takeaway 1", "takeaway 2", ...]`)

	messages := []llm.Message{
		{Role: "system", Content: "You are a senior news editor. Respond ONLY with a valid JSON array of strings containing concise 1-2 sentence takeaways."},
		{Role: "user", Content: sb.String()},
	}

	resp, err := s.llmClient.Chat(ctx, messages)
	if err != nil {
		slog.Warn("LLM takeaway generation failed, using fallback takeaways", "error", err)
		for i, item := range items {
			takeaways[i] = s.fallbackTakeaway(item.Summary)
		}
		return takeaways
	}

	var parsed []string
	cleanResp := strings.TrimSpace(resp)
	if idx := strings.Index(cleanResp, "["); idx != -1 {
		if endIdx := strings.LastIndex(cleanResp, "]"); endIdx != -1 && endIdx > idx {
			cleanResp = cleanResp[idx : endIdx+1]
		}
	}

	if err := json.Unmarshal([]byte(cleanResp), &parsed); err == nil && len(parsed) == len(items) {
		return parsed
	}

	slog.Warn("LLM response unmarshal mismatch or failure, applying individual fallbacks", "raw", resp)
	for i, item := range items {
		takeaways[i] = s.fallbackTakeaway(item.Summary)
	}

	return takeaways
}

func (s *Summarizer) fallbackTakeaway(summary string) string {
	cleaned := strings.TrimSpace(summary)
	if cleaned == "" {
		return "No summary snippet available for this news item."
	}

	// Split by sentences (. ! ?)
	sentences := strings.FieldsFunc(cleaned, func(r rune) bool {
		return r == '.' || r == '!' || r == '?'
	})

	if len(sentences) == 0 {
		return cleaned
	}

	if len(sentences) == 1 {
		return strings.TrimSpace(sentences[0]) + "."
	}

	takeaway := strings.TrimSpace(sentences[0]) + ". " + strings.TrimSpace(sentences[1]) + "."
	if len(takeaway) > 300 {
		takeaway = strings.TrimSpace(sentences[0]) + "."
	}
	return takeaway
}

func (s *Summarizer) fallbackDigestItems(items []store.NewsItem) []DigestItem {
	var digestItems []DigestItem
	for _, item := range items {
		digestItems = append(digestItems, DigestItem{
			Headline:         item.Title,
			Takeaway:         s.fallbackTakeaway(item.Summary),
			Body:             item.Summary,
			URL:              item.URL,
			Source:           item.Source,
			PublishedAt:      item.PublishedAt,
			ConfidenceFlag:   "(unverified)",
			FetchIntegrity:   item.FetchIntegrity,
			OriginalNewsItem: item,
		})
	}
	return digestItems
}

func extractDomain(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	host := strings.ToLower(u.Host)
	return strings.TrimPrefix(host, "www.")
}
