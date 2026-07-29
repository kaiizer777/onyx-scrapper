package research

import (
	"context"
	"fmt"

	"github.com/kaiizer777/onyx-scrapper/internal/discovery"
	"github.com/kaiizer777/onyx-scrapper/internal/llm"
	"github.com/kaiizer777/onyx-scrapper/internal/store"
	"log/slog"
	"time"
	"encoding/json"
	"strings"
)

type Worker struct {
	client   *llm.Client
	store    *store.Store
	registry *discovery.Registry
}

func NewWorker(client *llm.Client, st *store.Store, registry *discovery.Registry) *Worker {
	return &Worker{
		client:   client,
		store:    st,
		registry: registry,
	}
}

type extractedClaims struct {
	Claims []struct {
		Claim      string  `json:"claim"`
		SourceURL  string  `json:"source_url"`
		Confidence float64 `json:"confidence"`
	} `json:"claims"`
}

func (w *Worker) RunSubResearch(ctx context.Context, runID int64, sqID int64, question string) error {
	// 1. Search
	results := w.registry.Search(ctx, question)
	if len(results) == 0 {
		return fmt.Errorf("no search results found for question: %s", question)
	}

	// 2. Fetch top N
	var chunks []string
	var urls []string
	var providers []string
	
	// Fetch top 5 unique results
	for i, res := range results {
		if i >= 5 {
			break
		}
		pc, err := w.registry.Fetch(ctx, res.URL, discovery.FetchOptions{Timeout: 30 * time.Second})
		if err != nil {
			slog.Warn("Failed to fetch", "url", res.URL, "error", err)
			continue
		}
		chunks = append(chunks, pc.CleanText)
		urls = append(urls, res.URL)
		providers = append(providers, pc.Provider)

		// Persist page
		if _, err := w.store.SavePage(pc.URL, pc.RawHTML, pc.CleanText, pc.Provider); err != nil {
			slog.Warn("Failed to save page", "url", pc.URL, "error", err)
		}
	}

	if len(chunks) == 0 {
		return fmt.Errorf("all fetches failed for question: %s", question)
	}

	// 3. Rerank
	ranked, err := w.registry.Rerank(ctx, question, chunks)
	if err != nil {
		slog.Warn("Rerank failed or disabled, falling through unranked", "error", err)
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
		selectedChunks = append(selectedChunks, r.Text)
		selectedURLs = append(selectedURLs, urls[r.Index])
		selectedProviders = append(selectedProviders, providers[r.Index])
	}

	// 5. Extract claims with MiMo
	for i, chunk := range selectedChunks {
		prompt := fmt.Sprintf(`Extract factual claims from the following text that answer the question: "%s".
Return a JSON object with a "claims" array containing objects with "claim" (the statement), "source_url" (should be exactly "%s"), and "confidence" (0.0 to 1.0).
Text:
%s`, question, selectedURLs[i], chunk)
		
		messages := []llm.Message{
			{Role: "system", Content: "You are an extraction assistant. Respond strictly with JSON."},
			{Role: "user", Content: prompt},
		}

		respStr, err := w.client.Chat(ctx, messages)
		if err != nil {
			slog.Warn("MiMo extraction failed for chunk", "error", err)
			continue
		}

		// Clean up markdown block if present
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
			continue
		}

		// 6. Save findings
		for _, c := range extracted.Claims {
			_, err := w.store.SaveFinding(sqID, c.Claim, c.SourceURL, selectedProviders[i], c.Confidence)
			if err != nil {
				slog.Warn("Failed to save finding", "error", err)
			}
		}
	}

	return nil
}
