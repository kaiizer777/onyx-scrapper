package research

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/kaiizer777/onyx-scrapper/internal/discovery"
	"github.com/kaiizer777/onyx-scrapper/internal/llm"
	"github.com/kaiizer777/onyx-scrapper/internal/quality"
	"github.com/kaiizer777/onyx-scrapper/internal/store"
	"github.com/kaiizer777/onyx-scrapper/internal/timecontext"
)

const DefaultMinConfidence = 0.4

type Worker struct {
	client             *llm.Client
	store              *store.Store
	registry           *discovery.Registry
	verifier           *quality.SecondSourceVerifier
	entityCheckEnabled bool
	minConfidence      float64
}

func NewWorker(client *llm.Client, st *store.Store, registry *discovery.Registry, entityCheckEnabled bool, budget *quality.Budget, ttlHours int) *Worker {
	return &Worker{
		client:             client,
		store:              st,
		registry:           registry,
		verifier:           quality.NewSecondSourceVerifier(client, registry, st, budget, ttlHours),
		entityCheckEnabled: entityCheckEnabled,
		minConfidence:      DefaultMinConfidence,
	}
}

// SetMinConfidence sets the minimum confidence threshold for claim extraction.
func (w *Worker) SetMinConfidence(min float64) {
	w.minConfidence = min
}

type extractedClaims struct {
	Claims []struct {
		Claim      string  `json:"claim"`
		SourceURL  string  `json:"source_url"`
		Confidence float64 `json:"confidence"`
	} `json:"claims"`
}

func (w *Worker) reformulateQuery(ctx context.Context, question string) (string, error) {
	currentDateStr := timecontext.Now().Format("January 2, 2006")
	currentYear := timecontext.Now().Year()
	prompt := fmt.Sprintf(`The search query "%s" yielded no usable results or extracted claims.
Provide a concise, keyword-dense search query (3 to 6 keywords) to find the latest factual information on this topic.
Focus on core entities, tools, benchmarks, and releases.

Today's date is %s (Year: %d).
Return ONLY the reformulated search query text, nothing else.`, question, currentDateStr, currentYear)
	messages := []llm.Message{
		{Role: "system", Content: "You are a search query reformulation assistant. Return only the optimized query text."},
		{Role: "user", Content: prompt},
	}
	resp, err := w.client.Chat(ctx, messages)
	if err != nil {
		return "", err
	}
	return strings.Trim(strings.TrimSpace(resp), `"'`+"`"), nil
}

func (w *Worker) RunSubResearch(ctx context.Context, runID int64, sqID int64, question string) error {
	savedCount, err := w.attemptSearchAndExtract(ctx, runID, sqID, question)
	if savedCount == 0 {
		// Reformulate query
		reformulated, reformErr := w.reformulateQuery(ctx, question)
		if reformErr == nil && reformulated != "" && reformulated != question {
			slog.Info("Reformulating query due to 0 findings or fetch failures", "original", question, "reformulated", reformulated)
			savedCount, err = w.attemptSearchAndExtract(ctx, runID, sqID, reformulated)
		}
		
		if savedCount == 0 {
			_ = w.store.UpdateSubQuestionStatus(sqID, "insufficient_data")
			if err != nil {
				return err
			}
			return fmt.Errorf("insufficient_data for question: %s", question)
		}
	}
	_ = w.store.UpdateSubQuestionStatus(sqID, "done")
	return nil
}

func (w *Worker) attemptSearchAndExtract(ctx context.Context, runID int64, sqID int64, question string) (int, error) {
	// 1. Search
	results := w.registry.Search(ctx, question)
	if len(results) == 0 {
		return 0, fmt.Errorf("no search results found for question: %s", question)
	}

	// 2. Fetch top candidates concurrently (try up to 10 search results to get clean chunks)
	type fetchItem struct {
		chunk    string
		url      string
		provider string
	}
	var fetchMu sync.Mutex
	var validFetches []fetchItem

	var eg errgroup.Group
	maxCandidates := len(results)
	if maxCandidates > 10 {
		maxCandidates = 10
	}

	for i := 0; i < maxCandidates; i++ {
		res := results[i]
		u := res.URL
		prov := res.Provider

		eg.Go(func() error {
			pc, err := w.registry.Fetch(ctx, u, discovery.FetchOptions{Timeout: 30 * time.Second})

			var rawHTML, cleanText, provider string
			if pc != nil {
				rawHTML, cleanText, provider = pc.RawHTML, pc.CleanText, pc.Provider
			} else {
				provider = prov
			}

			integrity := quality.AnalyzeFetchIntegrity(rawHTML, cleanText, provider, err)
			if err != nil {
				slog.Warn("Failed to fetch", "url", u, "error", err, "integrity", integrity)
			}

			if pc != nil && (integrity == quality.FetchOK || integrity == quality.FetchFallbackRecovered) && strings.TrimSpace(cleanText) != "" {
				fetchMu.Lock()
				validFetches = append(validFetches, fetchItem{
					chunk:    cleanText,
					url:      u,
					provider: provider,
				})
				fetchMu.Unlock()
			}

			// Persist page
			if pc != nil {
				_, _ = w.store.SavePage(pc.URL, rawHTML, cleanText, provider, string(integrity))
				_ = w.store.AddPageToRun(runID, pc.URL)
			} else {
				_, _ = w.store.SavePage(u, "", "", provider, string(integrity))
				_ = w.store.AddPageToRun(runID, u)
			}
			return nil
		})
	}
	_ = eg.Wait()

	if len(validFetches) == 0 {
		return 0, fmt.Errorf("all fetches failed for question: %s", question)
	}

	var filteredChunks []string
	var filteredURLs []string
	var filteredProviders []string
	for _, item := range validFetches {
		filteredChunks = append(filteredChunks, item.chunk)
		filteredURLs = append(filteredURLs, item.url)
		filteredProviders = append(filteredProviders, item.provider)
	}

	// 3. Rerank
	ranked, err := w.registry.Rerank(ctx, question, filteredChunks)
	if err != nil || len(ranked) == 0 {
		if err != nil {
			slog.Warn("Rerank failed or disabled, falling through unranked", "error", err)
		}
		ranked = make([]discovery.RankedDoc, len(filteredChunks))
		for i, chunk := range filteredChunks {
			ranked[i] = discovery.RankedDoc{Index: i, Text: chunk, Score: 1.0}
		}
	}

	// 4. Keep top K (default 8)
	topK := 8
	var selectedChunks []string
	var selectedURLs []string
	var selectedProviders []string
	for i, r := range ranked {
		if i >= topK {
			break
		}
		if r.Index < 0 || r.Index >= len(filteredURLs) || r.Index >= len(filteredProviders) {
			continue
		}
		selectedChunks = append(selectedChunks, r.Text)
		selectedURLs = append(selectedURLs, filteredURLs[r.Index])
		selectedProviders = append(selectedProviders, filteredProviders[r.Index])
	}

	// 5. Extract claims with MiMo
	var extractEg errgroup.Group
	var storeMu sync.Mutex
	var totalSaved int64

	for i, chunk := range selectedChunks {
		idx := i
		chk := chunk
		extractEg.Go(func() error {
			currentDateStr := timecontext.Now().Format("January 2, 2006")
			prompt := fmt.Sprintf(`Extract factual claims from the following text that answer the question: "%s".
Today's date is %s. Use this as the ground truth for what is current.
Return a JSON object with a "claims" array containing objects with "claim" (the statement), "source_url" (should be exactly "%s"), and "confidence" (0.0 to 1.0).
Text:
%s`, question, currentDateStr, selectedURLs[idx], chk)

			messages := []llm.Message{
				{Role: "system", Content: "You are an extraction assistant. Respond strictly with JSON."},
				{Role: "user", Content: prompt},
			}

			respStr, err := w.client.Chat(ctx, messages)
			if err != nil {
				slog.Warn("MiMo extraction failed for chunk", "error", err)
				return nil
			}

			if len(respStr) > 7 && respStr[:7] == "```json" {
				respStr = strings.TrimPrefix(respStr, "```json")
				respStr = strings.TrimSuffix(respStr, "```")
			} else if len(respStr) > 3 && respStr[:3] == "```" {
				respStr = strings.TrimPrefix(respStr, "```")
				respStr = strings.TrimSuffix(respStr, "```")
			}

			var extracted extractedClaims
			if err := json.Unmarshal([]byte(respStr), &extracted); err != nil {
				slog.Warn("Failed to parse extracted claims JSON", "error", err, "raw", respStr)
				return nil
			}

			// 6. Save findings (and verify concurrently with each chunk)
			var claimEg errgroup.Group
			for _, c := range extracted.Claims {
				clm := c
				if clm.Confidence < w.minConfidence {
					slog.Debug("dropping low-confidence claim", "confidence", clm.Confidence, "claim", clm.Claim, "min_confidence", w.minConfidence)
					continue
				}

				verifiedURL := selectedURLs[idx]
				if clm.SourceURL != "" && !urlsRoughlyMatch(clm.SourceURL, verifiedURL) {
					slog.Warn("LLM-reported source URL mismatch, clamping to verified URL",
						"llm_url", clm.SourceURL, "verified_url", verifiedURL)
				}
				clm.SourceURL = verifiedURL

				claimEg.Go(func() error {
					finding := store.Finding{
						SubQuestionID:  sqID,
						Claim:          clm.Claim,
						SourceURL:      clm.SourceURL,
						SourceProvider: selectedProviders[idx],
						Confidence:     clm.Confidence,
						Status:         store.StatusActive,
					}

					if w.entityCheckEnabled {
						res, val, err := w.verifier.VerifyClaim(ctx, clm.Claim)
						if err != nil {
							slog.Warn("Entity verification failed", "error", err)
						} else {
							switch res {
							case quality.ResultContradicted:
								finding.Status = store.StatusContradicted
								finding.VerificationNote = val
								slog.Info("Claim contradicted by second source", "original", clm.Claim, "new_value", val)
							case quality.ResultUnclear:
								finding.Status = store.StatusUnclear
								finding.VerificationNote = val
								slog.Info("Claim verification unclear", "claim", clm.Claim, "note", val)
							case quality.ResultConfirmed:
								finding.Status = store.StatusActive
								slog.Debug("Claim confirmed by second source", "claim", clm.Claim)
							default:
								finding.Status = store.StatusActive
							}
						}
					}

					storeMu.Lock()
					fID, err := w.store.InsertFinding(finding)
					if err == nil && fID > 0 {
						atomic.AddInt64(&totalSaved, 1)
					}
					storeMu.Unlock()
					if err != nil {
						slog.Warn("Failed to save finding", "error", err)
					}
					return nil
				})
			}
			_ = claimEg.Wait()
			return nil
		})
	}
	_ = extractEg.Wait()

	return int(atomic.LoadInt64(&totalSaved)), nil
}

// urlsRoughlyMatch checks whether two URLs have the same host and path (ignoring www. and trailing slashes).
func urlsRoughlyMatch(u1, u2 string) bool {
	p1, err1 := url.Parse(strings.TrimSpace(u1))
	p2, err2 := url.Parse(strings.TrimSpace(u2))
	if err1 != nil || err2 != nil {
		return strings.EqualFold(strings.TrimRight(u1, "/"), strings.TrimRight(u2, "/"))
	}
	h1 := strings.TrimPrefix(strings.ToLower(p1.Hostname()), "www.")
	h2 := strings.TrimPrefix(strings.ToLower(p2.Hostname()), "www.")
	if h1 != h2 {
		return false
	}
	path1 := strings.TrimRight(p1.Path, "/")
	path2 := strings.TrimRight(p2.Path, "/")
	return strings.EqualFold(path1, path2)
}

