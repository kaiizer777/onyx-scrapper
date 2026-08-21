package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
)

type RankedDoc struct {
	Index int     `json:"index"`
	Text  string  `json:"text"`
	Score float64 `json:"relevance_score"`
}

type JinaReranker struct {
	apiKey  string
	enabled bool
	client  *http.Client
}

func NewJinaReranker(apiKey string, enabled bool) *JinaReranker {
	return &JinaReranker{
		apiKey:  apiKey,
		enabled: enabled,
		client:  &http.Client{},
	}
}

type rerankRequest struct {
	Model string   `json:"model"`
	Query string   `json:"query"`
	Docs  []string `json:"documents"`
	TopN  int      `json:"top_n,omitempty"`
}

type rerankResponse struct {
	Results []struct {
		Index          int     `json:"index"`
		RelevanceScore float64 `json:"relevance_score"`
		Document       struct {
			Text string `json:"text"`
		} `json:"document"`
	} `json:"results"`
}

func (r *JinaReranker) Rerank(ctx context.Context, query string, docs []string) ([]RankedDoc, error) {
	if r == nil || !r.enabled || len(docs) == 0 {
		return r.fallbackRerank(docs), nil
	}

	reqBody := rerankRequest{
		Model: "jina-reranker-v2-base-multilingual",
		Query: query,
		Docs:  docs,
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		log.Printf("jina reranker error marshaling request: %v, falling back", err)
		return r.fallbackRerank(docs), nil
	}

	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.jina.ai/v1/rerank", bytes.NewReader(bodyBytes))
	if err != nil {
		log.Printf("jina reranker error creating request: %v, falling back", err)
		return r.fallbackRerank(docs), nil
	}
	req.Header.Set("Content-Type", "application/json")
	if r.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+r.apiKey)
	}

	resp, err := r.client.Do(req)
	if err != nil {
		log.Printf("jina reranker request failed: %v, falling back", err)
		return r.fallbackRerank(docs), nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("jina reranker returned status %d, falling back", resp.StatusCode)
		return r.fallbackRerank(docs), nil
	}

	var res rerankResponse
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		log.Printf("jina reranker error decoding response: %v, falling back", err)
		return r.fallbackRerank(docs), nil
	}

	var out []RankedDoc
	for _, rr := range res.Results {
		text := rr.Document.Text
		if text == "" {
			// fallback mapping if jina doesn't return it
			if rr.Index < len(docs) {
				text = docs[rr.Index]
			}
		}
		out = append(out, RankedDoc{
			Index: rr.Index,
			Text:  text,
			Score: rr.RelevanceScore,
		})
	}
	return out, nil
}

func (r *JinaReranker) fallbackRerank(docs []string) []RankedDoc {
	var out []RankedDoc
	for i, d := range docs {
		out = append(out, RankedDoc{Index: i, Text: d, Score: 1.0})
	}
	return out
}
