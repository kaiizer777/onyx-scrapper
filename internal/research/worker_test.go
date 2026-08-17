package research

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaiizer777/onyx-scrapper/internal/config"
	"github.com/kaiizer777/onyx-scrapper/internal/discovery"
	"github.com/kaiizer777/onyx-scrapper/internal/llm"
	"github.com/kaiizer777/onyx-scrapper/internal/store"
)

type mockSearchProvider struct {
	name    string
	results map[string][]discovery.SearchResult
}

func (m *mockSearchProvider) Name() string {
	return m.name
}

func (m *mockSearchProvider) Search(ctx context.Context, query string, limit int) ([]discovery.SearchResult, error) {
	for k, res := range m.results {
		if strings.Contains(strings.ToLower(query), strings.ToLower(k)) {
			return res, nil
		}
	}
	return []discovery.SearchResult{
		{URL: "https://example.com/doc1", Title: "Doc 1", Snippet: "First result snippet", Provider: m.name},
		{URL: "https://example.com/doc2", Title: "Doc 2", Snippet: "Second result snippet", Provider: m.name},
	}, nil
}

type mockFetchProvider struct {
	name      string
	responses map[string]*discovery.PageContent
	errors    map[string]error
	callCount int32
}

func (m *mockFetchProvider) Name() string {
	return m.name
}

func (m *mockFetchProvider) Fetch(ctx context.Context, targetURL string, opts discovery.FetchOptions) (*discovery.PageContent, error) {
	atomic.AddInt32(&m.callCount, 1)
	if err, ok := m.errors[targetURL]; ok {
		return nil, err
	}
	if resp, ok := m.responses[targetURL]; ok {
		return resp, nil
	}
	validText := "SQLite Write-Ahead Logging (WAL) mode provides significant concurrency improvements over traditional rollback journals. " +
		"In WAL mode, changes are appended to a separate WAL file rather than modifying the main database directly. " +
		"This architectural design enables concurrent reader transactions to proceed uninterrupted while a single writer appends transactions, eliminating reader-writer lock contention in read-heavy applications."
	return &discovery.PageContent{
		URL:       targetURL,
		CleanText: validText,
		RawHTML:   "<html><body><p>" + validText + "</p></body></html>",
		Provider:  m.name,
		FetchedAt: time.Now(),
	}, nil
}

type mockReranker struct {
	rankedDocs []discovery.RankedDoc
	err        error
}

func (m *mockReranker) Rerank(ctx context.Context, query string, docs []string) ([]discovery.RankedDoc, error) {
	if m.err != nil {
		return nil, m.err
	}
	if len(m.rankedDocs) > 0 {
		return m.rankedDocs, nil
	}
	var out []discovery.RankedDoc
	for i, d := range docs {
		out = append(out, discovery.RankedDoc{Index: i, Text: d, Score: 0.9})
	}
	return out, nil
}

func TestWorker_RunSubResearch_HappyPath(t *testing.T) {
	mockLLMServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]interface{}{
						"role": "assistant",
						"content": `{
							"claims": [
								{
									"claim": "WAL allows concurrent reading and writing simultaneously.",
									"source_url": "https://example.com/doc1",
									"confidence": 0.98
								}
							]
						}`,
					},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mockLLMServer.Close()

	dbPath := filepath.Join(t.TempDir(), "worker_test.db")
	st, err := store.NewStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer st.Close()

	runID, err := st.CreateResearchRun("Test Goal")
	if err != nil {
		t.Fatalf("failed to create run: %v", err)
	}
	sqID, err := st.CreateSubQuestion(runID, "How does WAL mode work?")
	if err != nil {
		t.Fatalf("failed to create subquestion: %v", err)
	}

	searcher := &mockSearchProvider{name: "searxng"}
	fetcher := &mockFetchProvider{name: "colly"}
	registry := discovery.NewRegistry([]discovery.SearchProvider{searcher}, map[string]discovery.FetchProvider{"colly": fetcher}, []string{"colly"}, nil)

	provCfg := config.ProviderConfig{
		BaseURL: mockLLMServer.URL,
		Model:   "mock-model",
		APIKey:  "test-key",
	}
	client := llm.NewClient(provCfg)

	worker := NewWorker(client, st, registry, false, nil, 24)

	err = worker.RunSubResearch(context.Background(), runID, sqID, "How does WAL mode work?")
	if err != nil {
		t.Fatalf("unexpected error running sub-research: %v", err)
	}

	findings, err := st.GetFindingsForSubQuestion(sqID)
	if err != nil {
		t.Fatalf("failed to get findings: %v", err)
	}
	if len(findings) == 0 {
		t.Fatal("expected at least 1 finding in DB, got 0")
	}

	sqs, err := st.GetSubQuestionsForRun(runID)
	if err != nil || len(sqs) == 0 {
		t.Fatalf("failed to get subquestions: %v", err)
	}
	if sqs[0].Status != "done" {
		t.Errorf("expected subquestion status 'done', got %q", sqs[0].Status)
	}
}

func TestWorker_RunSubResearch_RerankFailureFallback(t *testing.T) {
	mockLLMServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]interface{}{
						"role": "assistant",
						"content": `{
							"claims": [
								{
									"claim": "Fallback finding extracted without reranker.",
									"source_url": "https://example.com/doc1",
									"confidence": 0.90
								}
							]
						}`,
					},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mockLLMServer.Close()

	dbPath := filepath.Join(t.TempDir(), "worker_fallback_test.db")
	st, err := store.NewStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer st.Close()

	runID, _ := st.CreateResearchRun("Test Goal")
	sqID, _ := st.CreateSubQuestion(runID, "Test Rerank Fallback")

	searcher := &mockSearchProvider{name: "searxng"}
	fetcher := &mockFetchProvider{name: "colly"}
	failingReranker := &mockReranker{err: fmt.Errorf("simulated rerank 500 error")}

	registry := discovery.NewRegistry([]discovery.SearchProvider{searcher}, map[string]discovery.FetchProvider{"colly": fetcher}, []string{"colly"}, failingReranker)

	provCfg := config.ProviderConfig{
		BaseURL: mockLLMServer.URL,
		Model:   "mock-model",
		APIKey:  "test-key",
	}
	client := llm.NewClient(provCfg)

	worker := NewWorker(client, st, registry, false, nil, 24)

	err = worker.RunSubResearch(context.Background(), runID, sqID, "Test Rerank Fallback")
	if err != nil {
		t.Fatalf("expected rerank failure to fall through unranked, got error: %v", err)
	}

	findings, err := st.GetFindingsForSubQuestion(sqID)
	if err != nil || len(findings) == 0 {
		t.Fatal("expected findings to be saved despite reranker failure")
	}
}

func TestWorker_RunSubResearch_RerankOutOfBoundsIndex_NoPanic(t *testing.T) {
	mockLLMServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]interface{}{
						"role": "assistant",
						"content": `{
							"claims": [
								{
									"claim": "Claim with valid index.",
									"source_url": "https://example.com/doc1",
									"confidence": 0.85
								}
							]
						}`,
					},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mockLLMServer.Close()

	dbPath := filepath.Join(t.TempDir(), "worker_bounds_test.db")
	st, err := store.NewStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer st.Close()

	runID, _ := st.CreateResearchRun("Test Goal")
	sqID, _ := st.CreateSubQuestion(runID, "Test Bounds")

	searcher := &mockSearchProvider{name: "searxng"}
	fetcher := &mockFetchProvider{name: "colly"}
	corruptedReranker := &mockReranker{
		rankedDocs: []discovery.RankedDoc{
			{Index: 999, Text: "Out of bounds text", Score: 0.99},
			{Index: -5, Text: "Negative index text", Score: 0.95},
			{Index: 0, Text: "Valid index text", Score: 0.90},
		},
	}

	registry := discovery.NewRegistry([]discovery.SearchProvider{searcher}, map[string]discovery.FetchProvider{"colly": fetcher}, []string{"colly"}, corruptedReranker)

	provCfg := config.ProviderConfig{
		BaseURL: mockLLMServer.URL,
		Model:   "mock-model",
		APIKey:  "test-key",
	}
	client := llm.NewClient(provCfg)

	worker := NewWorker(client, st, registry, false, nil, 24)

	err = worker.RunSubResearch(context.Background(), runID, sqID, "Test Bounds")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	findings, _ := st.GetFindingsForSubQuestion(sqID)
	if len(findings) == 0 {
		t.Fatal("expected valid index finding to be processed and saved")
	}
}

func TestWorker_RunSubResearch_QueryReformulationOnAllFetchesFailed(t *testing.T) {
	mockLLMServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		messages, _ := body["messages"].([]interface{})
		systemRoleContent := ""
		for _, msg := range messages {
			if m, ok := msg.(map[string]interface{}); ok {
				if m["role"] == "system" {
					systemRoleContent, _ = m["content"].(string)
				}
			}
		}

		if strings.Contains(systemRoleContent, "reformulation") {
			resp := map[string]interface{}{
				"choices": []map[string]interface{}{
					{
						"message": map[string]interface{}{
							"role":    "assistant",
							"content": "sqlite concurrency benefits",
						},
					},
				},
			}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		resp := map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]interface{}{
						"role": "assistant",
						"content": `{
							"claims": [
								{
									"claim": "Extracted finding after query reformulation.",
									"source_url": "https://example.com/recovered",
									"confidence": 0.92
								}
							]
						}`,
					},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mockLLMServer.Close()

	dbPath := filepath.Join(t.TempDir(), "worker_reform_test.db")
	st, err := store.NewStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer st.Close()

	runID, _ := st.CreateResearchRun("Test Goal")
	sqID, _ := st.CreateSubQuestion(runID, "Blocked Question")

	searcher := &mockSearchProvider{
		name: "searxng",
		results: map[string][]discovery.SearchResult{
			"blocked": {
				{URL: "https://blocked.com/403", Title: "Blocked", Snippet: "Forbidden", Provider: "searxng"},
			},
			"sqlite concurrency benefits": {
				{URL: "https://example.com/recovered", Title: "Recovered", Snippet: "Success", Provider: "searxng"},
			},
		},
	}

	validRecoveredText := "SQLite WAL mode improves write performance and enables concurrent readers without locking the entire database in high concurrency multi-threaded architectures. Readers do not block writers and writers do not block readers, providing excellent read throughput."

	fetcher := &mockFetchProvider{
		name: "colly",
		errors: map[string]error{
			"https://blocked.com/403": fmt.Errorf("HTTP 403 Forbidden"),
		},
		responses: map[string]*discovery.PageContent{
			"https://example.com/recovered": {
				URL:       "https://example.com/recovered",
				CleanText: validRecoveredText,
				RawHTML:   "<html><body><p>" + validRecoveredText + "</p></body></html>",
				Provider:  "colly",
			},
		},
	}

	registry := discovery.NewRegistry([]discovery.SearchProvider{searcher}, map[string]discovery.FetchProvider{"colly": fetcher}, []string{"colly"}, nil)

	provCfg := config.ProviderConfig{
		BaseURL: mockLLMServer.URL,
		Model:   "mock-model",
		APIKey:  "test-key",
	}
	client := llm.NewClient(provCfg)

	worker := NewWorker(client, st, registry, false, nil, 24)

	err = worker.RunSubResearch(context.Background(), runID, sqID, "Blocked Question")
	if err != nil {
		t.Fatalf("unexpected error with query reformulation: %v", err)
	}

	findings, _ := st.GetFindingsForSubQuestion(sqID)
	if len(findings) == 0 {
		t.Fatal("expected findings extracted via reformulated query")
	}
}

func TestExtractClaims_LowConfidenceDropped(t *testing.T) {
	mockLLMServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]interface{}{
						"role": "assistant",
						"content": `{
							"claims": [
								{
									"claim": "Low confidence claim that must be dropped.",
									"source_url": "https://example.com/doc1",
									"confidence": 0.1
								},
								{
									"claim": "High confidence claim that must be retained.",
									"source_url": "https://example.com/doc1",
									"confidence": 0.95
								}
							]
						}`,
					},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mockLLMServer.Close()

	dbPath := filepath.Join(t.TempDir(), "worker_low_conf_test.db")
	st, err := store.NewStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer st.Close()

	runID, _ := st.CreateResearchRun("Test Goal")
	sqID, _ := st.CreateSubQuestion(runID, "Test Low Confidence Gating")

	searcher := &mockSearchProvider{
		name: "searxng",
		results: map[string][]discovery.SearchResult{
			"test low confidence gating": {
				{URL: "https://example.com/doc1", Title: "Doc 1", Snippet: "Doc 1 snippet", Provider: "searxng"},
			},
		},
	}
	fetcher := &mockFetchProvider{name: "colly"}
	registry := discovery.NewRegistry([]discovery.SearchProvider{searcher}, map[string]discovery.FetchProvider{"colly": fetcher}, []string{"colly"}, nil)

	provCfg := config.ProviderConfig{
		BaseURL: mockLLMServer.URL,
		Model:   "mock-model",
		APIKey:  "test-key",
	}
	client := llm.NewClient(provCfg)
	worker := NewWorker(client, st, registry, false, nil, 24)

	err = worker.RunSubResearch(context.Background(), runID, sqID, "Test Low Confidence Gating")
	if err != nil {
		t.Fatalf("unexpected error running sub-research: %v", err)
	}

	findings, err := st.GetFindingsForSubQuestion(sqID)
	if err != nil {
		t.Fatalf("failed to get findings: %v", err)
	}

	if len(findings) != 1 {
		t.Fatalf("expected exactly 1 finding (high confidence only), got %d findings", len(findings))
	}
	if findings[0].Claim != "High confidence claim that must be retained." {
		t.Errorf("unexpected finding claim: %q", findings[0].Claim)
	}
	if findings[0].Confidence != 0.95 {
		t.Errorf("expected confidence 0.95, got %f", findings[0].Confidence)
	}
}

func TestExtractClaims_ConfidenceAtThresholdBoundaryIncluded(t *testing.T) {
	mockLLMServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]interface{}{
						"role": "assistant",
						"content": `{
							"claims": [
								{
									"claim": "Boundary confidence claim exactly at 0.4.",
									"source_url": "https://example.com/doc1",
									"confidence": 0.4
								}
							]
						}`,
					},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mockLLMServer.Close()

	dbPath := filepath.Join(t.TempDir(), "worker_boundary_test.db")
	st, err := store.NewStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer st.Close()

	runID, _ := st.CreateResearchRun("Test Goal")
	sqID, _ := st.CreateSubQuestion(runID, "Test Boundary")

	searcher := &mockSearchProvider{
		name: "searxng",
		results: map[string][]discovery.SearchResult{
			"test boundary": {
				{URL: "https://example.com/doc1", Title: "Doc 1", Snippet: "Doc 1 snippet", Provider: "searxng"},
			},
		},
	}
	fetcher := &mockFetchProvider{name: "colly"}
	registry := discovery.NewRegistry([]discovery.SearchProvider{searcher}, map[string]discovery.FetchProvider{"colly": fetcher}, []string{"colly"}, nil)

	provCfg := config.ProviderConfig{
		BaseURL: mockLLMServer.URL,
		Model:   "mock-model",
		APIKey:  "test-key",
	}
	client := llm.NewClient(provCfg)
	worker := NewWorker(client, st, registry, false, nil, 24)

	err = worker.RunSubResearch(context.Background(), runID, sqID, "Test Boundary")
	if err != nil {
		t.Fatalf("unexpected error running sub-research: %v", err)
	}

	findings, err := st.GetFindingsForSubQuestion(sqID)
	if err != nil {
		t.Fatalf("failed to get findings: %v", err)
	}

	if len(findings) != 1 {
		t.Fatalf("expected boundary claim (confidence 0.4) to be included, got %d findings", len(findings))
	}
	if findings[0].Claim != "Boundary confidence claim exactly at 0.4." {
		t.Errorf("unexpected finding claim: %q", findings[0].Claim)
	}
}

func TestExtractClaims_SourceURLAlwaysClampedToVerifiedURL(t *testing.T) {
	mockLLMServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]interface{}{
						"role": "assistant",
						"content": `{
							"claims": [
								{
									"claim": "Claim with hallucinated source URL.",
									"source_url": "https://hallucinated-fake-source.com/article",
									"confidence": 0.88
								}
							]
						}`,
					},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mockLLMServer.Close()

	dbPath := filepath.Join(t.TempDir(), "worker_clamp_test.db")
	st, err := store.NewStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer st.Close()

	runID, _ := st.CreateResearchRun("Test Goal")
	sqID, _ := st.CreateSubQuestion(runID, "Test URL Clamping")

	authoritativeURL := "https://example.com/doc1"
	searcher := &mockSearchProvider{
		name: "searxng",
		results: map[string][]discovery.SearchResult{
			"clamping": {
				{URL: authoritativeURL, Title: "Doc 1", Snippet: "Authoritative snippet", Provider: "searxng"},
			},
		},
	}
	fetcher := &mockFetchProvider{name: "colly"}
	registry := discovery.NewRegistry([]discovery.SearchProvider{searcher}, map[string]discovery.FetchProvider{"colly": fetcher}, []string{"colly"}, nil)

	provCfg := config.ProviderConfig{
		BaseURL: mockLLMServer.URL,
		Model:   "mock-model",
		APIKey:  "test-key",
	}
	client := llm.NewClient(provCfg)
	worker := NewWorker(client, st, registry, false, nil, 24)

	err = worker.RunSubResearch(context.Background(), runID, sqID, "clamping")
	if err != nil {
		t.Fatalf("unexpected error running sub-research: %v", err)
	}

	findings, err := st.GetFindingsForSubQuestion(sqID)
	if err != nil || len(findings) == 0 {
		t.Fatalf("expected findings to be saved, got: %v", err)
	}

	if findings[0].SourceURL != authoritativeURL {
		t.Errorf("expected source URL to be clamped to verified URL %q, got %q", authoritativeURL, findings[0].SourceURL)
	}
}

func TestWorker_ContradictedClaim_SetsStatusContradicted_NotTextMutated(t *testing.T) {
	mockLLMServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		messages, _ := body["messages"].([]interface{})
		systemRoleContent := ""
		for _, msg := range messages {
			if m, ok := msg.(map[string]interface{}); ok {
				if m["role"] == "system" {
					systemRoleContent, _ = m["content"].(string)
				}
			}
		}

		// Second-source verification prompt handler
		if strings.Contains(systemRoleContent, "fact-checking assistant") {
			resp := map[string]interface{}{
				"choices": []map[string]interface{}{
					{
						"message": map[string]interface{}{
							"role":    "assistant",
							"content": "RESULT: [CONTRADICTED]\nVALUE: Tim Cook is the current CEO",
						},
					},
				},
			}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		// Claim extraction prompt handler
		resp := map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]interface{}{
						"role": "assistant",
						"content": `{
							"claims": [
								{
									"claim": "CEO of Apple is Steve Jobs",
									"source_url": "https://example.com/doc1",
									"confidence": 0.92
								}
							]
						}`,
					},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mockLLMServer.Close()

	dbPath := filepath.Join(t.TempDir(), "worker_contradicted_test.db")
	st, err := store.NewStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer st.Close()

	runID, _ := st.CreateResearchRun("Test Goal")
	sqID, _ := st.CreateSubQuestion(runID, "Who is the CEO of Apple?")

	searcher := &mockSearchProvider{
		name: "searxng",
		results: map[string][]discovery.SearchResult{
			"apple": {
				{URL: "https://example.com/doc1", Title: "Doc 1", Snippet: "Apple leadership article", Provider: "searxng"},
			},
		},
	}
	fetcher := &mockFetchProvider{name: "colly"}
	registry := discovery.NewRegistry([]discovery.SearchProvider{searcher}, map[string]discovery.FetchProvider{"colly": fetcher}, []string{"colly"}, nil)

	provCfg := config.ProviderConfig{
		BaseURL: mockLLMServer.URL,
		Model:   "mock-model",
		APIKey:  "test-key",
	}
	client := llm.NewClient(provCfg)

	// Initialize worker with entityCheckEnabled = true
	worker := NewWorker(client, st, registry, true, nil, 24)

	err = worker.RunSubResearch(context.Background(), runID, sqID, "Who is the CEO of Apple?")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	findings, err := st.GetFindingsForSubQuestion(sqID)
	if err != nil || len(findings) == 0 {
		t.Fatalf("expected finding to be saved in DB: %v", err)
	}

	finding := findings[0]

	// 1. Assert status is StatusContradicted
	if finding.Status != store.StatusContradicted {
		t.Errorf("expected StatusContradicted, got %q", finding.Status)
	}

	// 2. Assert claim text is byte-for-byte unchanged (not mutated with note appended)
	expectedClaim := "CEO of Apple is Steve Jobs"
	if finding.Claim != expectedClaim {
		t.Errorf("expected claim text to remain unmodified %q, got %q", expectedClaim, finding.Claim)
	}

	// 3. Assert structured verification note is stored
	expectedNote := "Tim Cook is the current CEO"
	if finding.VerificationNote != expectedNote {
		t.Errorf("expected verification note %q, got %q", expectedNote, finding.VerificationNote)
	}
}

