package research

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kaiizer777/onyx-scrapper/internal/config"
	"github.com/kaiizer777/onyx-scrapper/internal/discovery"
	"github.com/kaiizer777/onyx-scrapper/internal/llm"
	"github.com/kaiizer777/onyx-scrapper/internal/store"
)

func TestOrchestrator_Run_HappyPath(t *testing.T) {
	mockLLMServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		messages, _ := body["messages"].([]interface{})

		systemContent := ""
		for _, msg := range messages {
			if m, ok := msg.(map[string]interface{}); ok {
				if m["role"] == "system" {
					systemContent, _ = m["content"].(string)
				}
			}
		}

		var respContent string
		if strings.Contains(systemContent, "planning agent") {
			respContent = `{
				"sub_questions": [
					"What are Go channels?",
					"How do buffered channels differ from unbuffered channels?"
				],
				"report_outline": [
					"Channel Primitives",
					"Buffering Semantics"
				]
			}`
		} else if strings.Contains(systemContent, "extraction assistant") {
			respContent = `{
				"claims": [
					{
						"claim": "Channels in Go provide synchronized communication between goroutines.",
						"source_url": "https://example.com/go-channels",
						"confidence": 0.95
					}
				]
			}`
		} else if strings.Contains(systemContent, "reflection agent") {
			respContent = `{
				"new_questions": []
			}`
		} else if strings.Contains(systemContent, "synthesizer") {
			respContent = "# Go Channels Report\n\nChannels provide synchronized communication between goroutines [1](https://example.com/go-channels)."
		} else {
			respContent = "{}"
		}

		resp := map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]interface{}{
						"role":    "assistant",
						"content": respContent,
					},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mockLLMServer.Close()

	dbPath := filepath.Join(t.TempDir(), "orch_test.db")
	st, err := store.NewStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer st.Close()

	searcher := &mockSearchProvider{name: "searxng"}
	fetcher := &mockFetchProvider{name: "colly"}
	registry := discovery.NewRegistry([]discovery.SearchProvider{searcher}, map[string]discovery.FetchProvider{"colly": fetcher}, []string{"colly"}, nil)

	provCfg := config.ProviderConfig{
		BaseURL: mockLLMServer.URL,
		Model:   "mock-model",
		APIKey:  "test-key",
	}
	client := llm.NewClient(provCfg)

	cfg := &config.Config{}
	orch := NewOrchestrator(client, st, registry, cfg)

	run, err := orch.Run(context.Background(), "Explain Go Channels", Options{})
	if err != nil {
		t.Fatalf("orchestrator run failed: %v", err)
	}

	if run == nil {
		t.Fatal("expected non-nil research run")
	}
	if run.Status != "completed" {
		t.Errorf("expected run status 'completed', got %q", run.Status)
	}
	if !strings.Contains(run.ReportMD, "Go Channels") {
		t.Errorf("expected report to contain 'Go Channels', got %q", run.ReportMD)
	}
}

func TestOrchestrator_Run_ResumeWithZeroSubQuestions_TriggersPlanning(t *testing.T) {
	mockLLMServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		messages, _ := body["messages"].([]interface{})

		systemContent := ""
		for _, msg := range messages {
			if m, ok := msg.(map[string]interface{}); ok {
				if m["role"] == "system" {
					systemContent, _ = m["content"].(string)
				}
			}
		}

		var respContent string
		if strings.Contains(systemContent, "planning agent") {
			respContent = `{
				"sub_questions": [
					"Planned Question 1"
				],
				"report_outline": [
					"Overview"
				]
			}`
		} else if strings.Contains(systemContent, "extraction assistant") {
			respContent = `{
				"claims": [
					{
						"claim": "Verified factual claim.",
						"source_url": "https://example.com/doc1",
						"confidence": 0.90
					}
				]
			}`
		} else if strings.Contains(systemContent, "reflection agent") {
			respContent = `{"new_questions": []}`
		} else if strings.Contains(systemContent, "synthesizer") {
			respContent = "# Synthesis Complete"
		} else {
			respContent = "{}"
		}

		resp := map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]interface{}{
						"role":    "assistant",
						"content": respContent,
					},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mockLLMServer.Close()

	dbPath := filepath.Join(t.TempDir(), "orch_resume_zero_test.db")
	st, err := store.NewStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer st.Close()

	// Pre-create run with 0 subquestions (matching API handler behavior)
	preRunID, err := st.CreateResearchRun("Precreated Goal")
	if err != nil {
		t.Fatalf("failed to create run: %v", err)
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

	cfg := &config.Config{}
	orch := NewOrchestrator(client, st, registry, cfg)

	run, err := orch.Run(context.Background(), "Precreated Goal", Options{ResumeRunID: preRunID})
	if err != nil {
		t.Fatalf("orchestrator run failed: %v", err)
	}

	if run.ID != preRunID {
		t.Errorf("expected run ID %d, got %d", preRunID, run.ID)
	}
	if run.Status != "completed" {
		t.Errorf("expected status 'completed', got %q", run.Status)
	}

	// Verify subquestions were planned and created
	sqs, err := st.GetSubQuestionsForRun(preRunID)
	if err != nil || len(sqs) == 0 {
		t.Fatalf("expected subquestions to be planned for precreated run with 0 subquestions, got %d", len(sqs))
	}
}

func TestOrchestrator_Run_ResumeWithExistingSubQuestions_SkipsPlanning(t *testing.T) {
	planningCalled := false
	mockLLMServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		messages, _ := body["messages"].([]interface{})

		systemContent := ""
		for _, msg := range messages {
			if m, ok := msg.(map[string]interface{}); ok {
				if m["role"] == "system" {
					systemContent, _ = m["content"].(string)
				}
			}
		}

		var respContent string
		if strings.Contains(systemContent, "planning agent") {
			planningCalled = true
			respContent = `{"sub_questions": ["Should not be called"], "report_outline": ["None"]}`
		} else if strings.Contains(systemContent, "extraction assistant") {
			respContent = `{
				"claims": [
					{
						"claim": "Resumed subquestion claim.",
						"source_url": "https://example.com/doc1",
						"confidence": 0.90
					}
				]
			}`
		} else if strings.Contains(systemContent, "reflection agent") {
			respContent = `{"new_questions": []}`
		} else if strings.Contains(systemContent, "synthesizer") {
			respContent = "# Resumed Report"
		} else {
			respContent = "{}"
		}

		resp := map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]interface{}{
						"role":    "assistant",
						"content": respContent,
					},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mockLLMServer.Close()

	dbPath := filepath.Join(t.TempDir(), "orch_resume_existing_test.db")
	st, err := store.NewStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer st.Close()

	// Pre-create run and add existing subquestion
	preRunID, _ := st.CreateResearchRun("Existing Run Goal")
	_, _ = st.CreateSubQuestion(preRunID, "Existing Question 1")

	searcher := &mockSearchProvider{name: "searxng"}
	fetcher := &mockFetchProvider{name: "colly"}
	registry := discovery.NewRegistry([]discovery.SearchProvider{searcher}, map[string]discovery.FetchProvider{"colly": fetcher}, []string{"colly"}, nil)

	provCfg := config.ProviderConfig{
		BaseURL: mockLLMServer.URL,
		Model:   "mock-model",
		APIKey:  "test-key",
	}
	client := llm.NewClient(provCfg)

	cfg := &config.Config{}
	orch := NewOrchestrator(client, st, registry, cfg)

	run, err := orch.Run(context.Background(), "", Options{ResumeRunID: preRunID})
	if err != nil {
		t.Fatalf("orchestrator run failed: %v", err)
	}

	if planningCalled {
		t.Error("expected planner NOT to be called when resuming run with existing subquestions")
	}
	if run.Status != "completed" {
		t.Errorf("expected status 'completed', got %q", run.Status)
	}
}

func TestOrchestrator_Run_ContextCancellation(t *testing.T) {
	mockLLMServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]interface{}{
						"role":    "assistant",
						"content": `{"sub_questions": ["Q1"], "report_outline": ["O1"]}`,
					},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mockLLMServer.Close()

	dbPath := filepath.Join(t.TempDir(), "orch_cancel_test.db")
	st, _ := store.NewStore(dbPath)
	defer st.Close()

	searcher := &mockSearchProvider{name: "searxng"}
	fetcher := &mockFetchProvider{name: "colly"}
	registry := discovery.NewRegistry([]discovery.SearchProvider{searcher}, map[string]discovery.FetchProvider{"colly": fetcher}, []string{"colly"}, nil)

	provCfg := config.ProviderConfig{
		BaseURL: mockLLMServer.URL,
		Model:   "mock-model",
		APIKey:  "test-key",
	}
	client := llm.NewClient(provCfg)
	orch := NewOrchestrator(client, st, registry, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := orch.Run(ctx, "Will be cancelled", Options{})
	if err == nil {
		t.Fatal("expected cancellation error, got nil")
	}
}
