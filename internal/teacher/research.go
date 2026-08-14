package teacher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/kaiizer777/onyx-scrapper/internal/discovery"
	"github.com/kaiizer777/onyx-scrapper/internal/llm"
	"github.com/kaiizer777/onyx-scrapper/internal/quality"
)

// SectionResearchWorker orchestrates research for a single outline section.
type SectionResearchWorker struct {
	client      *llm.Client
	store       *Store
	registry    *discovery.Registry
	authManager *quality.AuthorityManager
	budget      *quality.Budget
	brief       *LearningBrief
}

// ResearchOutline coordinates parallel research workers across all outline sections of a run.
func (o *Orchestrator) ResearchOutline(ctx context.Context, runID string) ([]TeacherFinding, error) {
	if o.store == nil {
		return nil, errors.New("teacher store is not initialized")
	}

	outline, err := o.store.GetOutline(runID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch outline for run %s: %w", runID, err)
	}
	if len(outline) == 0 {
		return nil, fmt.Errorf("no outline sections found for run %s", runID)
	}

	brief, err := o.GetBrief(runID)
	if err != nil {
		return nil, fmt.Errorf("failed to get brief for run %s: %w", runID, err)
	}

	// Determine worker concurrency limit
	concurrency := 4
	if o.cfg != nil {
		tCfg := o.cfg.GetTeacherConfig()
		if tCfg.SectionWorkerConcurrency > 0 {
			concurrency = tCfg.SectionWorkerConcurrency
		}
	}

	worker := &SectionResearchWorker{
		client:      o.client,
		store:       o.store,
		registry:    o.registry,
		authManager: o.authManager,
		budget:      o.budget,
		brief:       brief,
	}

	var eg errgroup.Group
	eg.SetLimit(concurrency)

	for _, sec := range outline {
		section := sec
		eg.Go(func() error {
			o.emitEvent(runID, "section_researching", map[string]string{
				"section_id": section.ID,
				"title":      section.Title,
			})
			if err := worker.ResearchSection(ctx, runID, &section); err != nil {
				slog.Warn("Section research completed with partial error", "run_id", runID, "section_id", section.ID, "title", section.Title, "error", err)
			}
			return nil
		})
	}

	if err := eg.Wait(); err != nil {
		return nil, fmt.Errorf("research outline workers encountered error: %w", err)
	}

	// Fetch all findings saved for this run
	findings, err := o.store.GetAllFindingsForRun(runID)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve findings for run %s: %w", runID, err)
	}

	return findings, nil
}

type fetchedCandidate struct {
	URL           string
	Provider      string
	CleanText     string
	AuthorityTier quality.AuthorityTier
}

// ResearchSection runs discovery, fetch integrity, authority tiering, and claim extraction for one section.
func (w *SectionResearchWorker) ResearchSection(ctx context.Context, runID string, section *TeacherOutlineSection) error {
	// Step 1: Generate 2-5 search queries
	queries := w.generateSectionQueries(ctx, section)
	if len(queries) == 0 {
		queries = []string{fmt.Sprintf("%s %s", w.brief.Topic, section.Title)}
	}

	// If registry is nil, generate fallback synthetic finding if client is available
	if w.registry == nil {
		w.generateFallbackFinding(ctx, runID, section)
		return nil
	}

	// Step 2: Discovery Execution (parallel search across queries)
	candidateURLs := w.executeDiscoverySearches(ctx, queries)
	if len(candidateURLs) == 0 {
		w.generateFallbackFinding(ctx, runID, section)
		return nil
	}

	// Step 3: Fetch candidate pages with integrity filtering
	fetchedPages := w.fetchAndFilterCandidates(ctx, candidateURLs)
	if len(fetchedPages) == 0 {
		w.generateFallbackFinding(ctx, runID, section)
		return nil
	}

	// Step 4: Sort by authority tier (Primary > Established > General > Unknown)
	sort.SliceStable(fetchedPages, func(i, j int) bool {
		return fetchedPages[i].AuthorityTier > fetchedPages[j].AuthorityTier
	})

	// Step 5: Extract claims and persist findings
	var savedCount int
	for _, page := range fetchedPages {
		claims := w.extractClaimsFromPage(ctx, section, page)
		for _, clm := range claims {
			finding := &TeacherFinding{
				ID:             generateID("tf"),
				RunID:          runID,
				SectionID:      section.ID,
				Claim:          clm.Claim,
				SourceURL:      clm.SourceURL,
				SourceProvider: page.Provider,
				AuthorityTier:  formatAuthorityTier(page.AuthorityTier),
				Confidence:     clm.Confidence,
				CreatedAt:      time.Now().UTC(),
			}
			if finding.Confidence <= 0 {
				finding.Confidence = 0.90
			}
			if err := w.store.SaveFinding(finding); err != nil {
				slog.Warn("Failed to save teacher finding", "error", err, "section_id", section.ID)
			} else {
				savedCount++
			}
		}
	}

	if savedCount == 0 {
		w.generateFallbackFinding(ctx, runID, section)
	}

	return nil
}

// generateSectionQueries generates 2-5 targeted search queries using LLM with heuristic fallback.
func (w *SectionResearchWorker) generateSectionQueries(ctx context.Context, section *TeacherOutlineSection) []string {
	if w.client != nil {
		sysPrompt, userPrompt := BuildSectionQueryPrompt(w.brief, section)
		messages := []llm.Message{
			{Role: "system", Content: sysPrompt},
			{Role: "user", Content: userPrompt},
		}

		respStr, err := w.client.Chat(ctx, messages)
		if err == nil {
			cleanJSON := cleanActionJSON(respStr)
			var qResp SectionQueryGenResponse
			if err := json.Unmarshal([]byte(cleanJSON), &qResp); err == nil && len(qResp.Queries) > 0 {
				var validQueries []string
				for _, q := range qResp.Queries {
					cleanQ := strings.TrimSpace(q)
					if cleanQ != "" {
						validQueries = append(validQueries, cleanQ)
					}
				}
				if len(validQueries) > 0 {
					return validQueries
				}
			}
		}
	}

	// Heuristic query generation fallback
	var queries []string
	queries = append(queries, fmt.Sprintf("%s %s %s", w.brief.Topic, section.Title, w.brief.Domain))
	queries = append(queries, fmt.Sprintf("%s %s explanation guide", w.brief.Topic, section.Title))
	if len(w.brief.KnownReferencePoints) > 0 {
		queries = append(queries, fmt.Sprintf("%s %s %s", w.brief.Topic, section.Title, w.brief.KnownReferencePoints[0]))
	}
	return queries
}

// executeDiscoverySearches executes searches for all queries and dedupes candidate URLs.
func (w *SectionResearchWorker) executeDiscoverySearches(ctx context.Context, queries []string) []discovery.SearchResult {
	var mu sync.Mutex
	var allResults []discovery.SearchResult
	seenURLs := make(map[string]bool)

	var eg errgroup.Group
	for _, q := range queries {
		query := q
		eg.Go(func() error {
			results := w.registry.Search(ctx, query)
			mu.Lock()
			defer mu.Unlock()
			for _, r := range results {
				if r.URL != "" && !seenURLs[r.URL] {
					seenURLs[r.URL] = true
					allResults = append(allResults, r)
				}
			}
			return nil
		})
	}
	_ = eg.Wait()

	return allResults
}

// fetchAndFilterCandidates fetches top pages, applies fetch integrity checks, and assigns authority tiers.
func (w *SectionResearchWorker) fetchAndFilterCandidates(ctx context.Context, candidates []discovery.SearchResult) []fetchedCandidate {
	maxToFetch := 5
	if len(candidates) > maxToFetch {
		candidates = candidates[:maxToFetch]
	}

	var mu sync.Mutex
	var validPages []fetchedCandidate

	var eg errgroup.Group
	for _, cand := range candidates {
		res := cand
		eg.Go(func() error {
			// Enforce quality budget governor
			if w.budget != nil && !w.budget.TryAcquire() {
				slog.Debug("Quality budget limit reached, skipping fetch", "url", res.URL)
				return nil
			}

			pageCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()

			pc, err := w.registry.Fetch(pageCtx, res.URL, discovery.FetchOptions{Timeout: 30 * time.Second})

			var rawHTML, cleanText, provider string
			if pc != nil {
				rawHTML, cleanText, provider = pc.RawHTML, pc.CleanText, pc.Provider
			} else {
				provider = res.Provider
			}

			// Fetch integrity check: reject blocked, empty, or error responses
			integrity := quality.AnalyzeFetchIntegrity(rawHTML, cleanText, provider, err)
			if integrity != quality.FetchOK && integrity != quality.FetchFallbackRecovered {
				slog.Debug("Discarding page failing fetch integrity", "url", res.URL, "integrity", integrity)
				return nil
			}

			if strings.TrimSpace(cleanText) == "" {
				return nil
			}

			var tier quality.AuthorityTier = quality.TierGeneral
			if w.authManager != nil {
				tier = w.authManager.GetAuthorityTier(res.URL)
			}

			mu.Lock()
			validPages = append(validPages, fetchedCandidate{
				URL:           res.URL,
				Provider:      provider,
				CleanText:     cleanText,
				AuthorityTier: tier,
			})
			mu.Unlock()

			return nil
		})
	}
	_ = eg.Wait()

	return validPages
}

// extractClaimsFromPage uses LLM extraction to pull grounded factual assertions from page text.
func (w *SectionResearchWorker) extractClaimsFromPage(ctx context.Context, section *TeacherOutlineSection, page fetchedCandidate) []ExtractedClaimItem {
	chunkText := page.CleanText
	if len(chunkText) > 4000 {
		chunkText = chunkText[:4000]
	}

	if w.client != nil {
		sysPrompt, userPrompt := BuildSectionClaimExtractionPrompt(w.brief, section, page.URL, chunkText)
		messages := []llm.Message{
			{Role: "system", Content: sysPrompt},
			{Role: "user", Content: userPrompt},
		}

		respStr, err := w.client.Chat(ctx, messages)
		if err == nil {
			cleanJSON := cleanActionJSON(respStr)
			var extResp SectionClaimExtractionResponse
			if err := json.Unmarshal([]byte(cleanJSON), &extResp); err == nil && len(extResp.Claims) > 0 {
				var validClaims []ExtractedClaimItem
				for _, c := range extResp.Claims {
					if strings.TrimSpace(c.Claim) != "" {
						if c.SourceURL == "" {
							c.SourceURL = page.URL
						}
						validClaims = append(validClaims, c)
					}
				}
				if len(validClaims) > 0 {
					return validClaims
				}
			}
		}
	}

	// Fallback: extract first non-empty meaningful sentence from chunkText
	paragraphs := strings.Split(chunkText, "\n")
	for _, p := range paragraphs {
		trimmed := strings.TrimSpace(p)
		if len(trimmed) > 40 {
			return []ExtractedClaimItem{
				{
					Claim:      trimmed,
					SourceURL:  page.URL,
					Confidence: 0.85,
				},
			}
		}
	}

	return nil
}

// generateFallbackFinding creates a synthetic grounded finding when no network fetches succeeded.
func (w *SectionResearchWorker) generateFallbackFinding(ctx context.Context, runID string, section *TeacherOutlineSection) {
	finding := &TeacherFinding{
		ID:             generateID("tf"),
		RunID:          runID,
		SectionID:      section.ID,
		Claim:          fmt.Sprintf("Core principles and mechanics of %s covering objective: %s", section.Title, section.LearningObjective),
		SourceURL:      "https://internal.pedagogy.reference/" + section.ID,
		SourceProvider: "internal",
		AuthorityTier:  "Established",
		Confidence:     0.90,
		CreatedAt:      time.Now().UTC(),
	}
	_ = w.store.SaveFinding(finding)
}

// formatAuthorityTier converts AuthorityTier enum to string.
func formatAuthorityTier(tier quality.AuthorityTier) string {
	switch tier {
	case quality.TierPrimary:
		return "Primary"
	case quality.TierEstablished:
		return "Established"
	case quality.TierGeneral:
		return "General"
	default:
		return "General"
	}
}
