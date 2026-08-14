package research

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/kaiizer777/onyx-scrapper/internal/discovery"
	"github.com/kaiizer777/onyx-scrapper/internal/llm"
	"github.com/kaiizer777/onyx-scrapper/internal/quality"
	"github.com/kaiizer777/onyx-scrapper/internal/store"
	"github.com/kaiizer777/onyx-scrapper/internal/timecontext"
)

type Worker struct {
	client             *llm.Client
	store              *store.Store
	registry           *discovery.Registry
	verifier           *quality.SecondSourceVerifier
	entityCheckEnabled bool
}

func NewWorker(client *llm.Client, st *store.Store, registry *discovery.Registry, entityCheckEnabled bool, budget *quality.Budget, ttlHours int) *Worker {
	return &Worker{
		client:             client,
		store:              st,
		registry:           registry,
		verifier:           quality.NewSecondSourceVerifier(client, registry, st, budget, ttlHours),
		entityCheckEnabled: entityCheckEnabled,
	}
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
	prompt := fmt.Sprintf("The search query \"%s\" yielded no usable results (pages were blocked or empty). Provide a single, simpler or broader alternative search query to find the same information.\n\nToday's date is %s. Use this as the ground truth for what is current — do not rely on your own training knowledge to guess the date or the current state of fast-changing facts. When building a search query about current/latest/recent information, use the actual current year given above, not a year from memory.\n\nReturn ONLY the query text, nothing else.", question, currentDateStr)
	messages := []llm.Message{
		{Role: "system", Content: "You are a search query reformulation assistant."},
		{Role: "user", Content: prompt},
	}
	resp, err := w.client.Chat(ctx, messages)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(resp), nil
}

func (w *Worker) RunSubResearch(ctx context.Context, runID int64, sqID int64, question string) error {
	success, err := w.attemptSearchAndExtract(ctx, runID, sqID, question)
	if !success {
		// Reformulate
		reformulated, reformErr := w.reformulateQuery(ctx, question)
		if reformErr == nil && reformulated != "" && reformulated != question {
			slog.Info("Reformulating query due to fetch failures", "original", question, "reformulated", reformulated)
			success, err = w.attemptSearchAndExtract(ctx, runID, sqID, reformulated)
		}
		
		if !success {
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

func (w *Worker) attemptSearchAndExtract(ctx context.Context, runID int64, sqID int64, question string) (bool, error) {
	// 1. Search
	results := w.registry.Search(ctx, question)
	if len(results) == 0 {
		return false, fmt.Errorf("no search results found for question: %s", question)
	}

	// 2. Fetch top N
	var chunks = make([]string, 5)
	var urls = make([]string, 5)
	var providers = make([]string, 5)
	
	var eg errgroup.Group
	
	for i, res := range results {
		if i >= 5 {
			break
		}
		idx := i
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

			if pc != nil && (integrity == quality.FetchOK || integrity == quality.FetchFallbackRecovered) {
				chunks[idx] = cleanText
				urls[idx] = u
				providers[idx] = provider
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
	
	var filteredChunks []string
	var filteredURLs []string
	var filteredProviders []string
	for i := 0; i < 5; i++ {
		if chunks[i] != "" {
			filteredChunks = append(filteredChunks, chunks[i])
			filteredURLs = append(filteredURLs, urls[i])
			filteredProviders = append(filteredProviders, providers[i])
		}
	}
	
	if len(filteredChunks) == 0 {
		return false, fmt.Errorf("all fetches failed for question: %s", question)
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
				claimEg.Go(func() error {
					claimText := clm.Claim

					if w.entityCheckEnabled {
						res, val, err := w.verifier.VerifyClaim(ctx, claimText)
						if err != nil {
							slog.Warn("Entity verification failed", "error", err)
						} else {
							if res == quality.ResultContradicted {
								claimText = fmt.Sprintf("%s. Note: a second source suggests this may have changed — %s.", claimText, val)
								slog.Info("Claim contradicted by second source", "original", clm.Claim, "new_value", val)
							} else if res == quality.ResultConfirmed {
								slog.Debug("Claim confirmed by second source", "claim", clm.Claim)
							}
						}
					}

					storeMu.Lock()
					_, err := w.store.SaveFinding(sqID, claimText, clm.SourceURL, selectedProviders[idx], clm.Confidence)
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

	return true, nil
}
