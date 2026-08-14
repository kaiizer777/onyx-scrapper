package research

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kaiizer777/onyx-scrapper/internal/config"
	"github.com/kaiizer777/onyx-scrapper/internal/llm"
	"github.com/kaiizer777/onyx-scrapper/internal/store"
)

func TestPlanner_Plan_Success(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]interface{}{
						"role": "assistant",
						"content": "```json\n" + `{
							"sub_questions": [
								"What is SQLite WAL mode?",
								"How does WAL mode improve concurrency?",
								"What are the trade-offs of WAL mode?"
							],
							"report_outline": [
								"Introduction to WAL",
								"Concurrency Architecture",
								"Trade-offs and Operational Guidelines"
							]
						}` + "\n```",
					},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mockServer.Close()

	provCfg := config.ProviderConfig{
		BaseURL: mockServer.URL,
		Model:   "mock-planner-model",
		APIKey:  "test-key",
	}
	client := llm.NewClient(provCfg)
	planner := NewPlanner(client, nil)

	plan, err := planner.Plan(context.Background(), "Explain SQLite WAL mode concurrency benefits")
	if err != nil {
		t.Fatalf("unexpected error planning: %v", err)
	}

	if len(plan.SubQuestions) != 3 {
		t.Errorf("expected 3 sub-questions, got %d", len(plan.SubQuestions))
	}
	if len(plan.ReportOutline) != 3 {
		t.Errorf("expected 3 outline sections, got %d", len(plan.ReportOutline))
	}
	if plan.SubQuestions[0].Question != "What is SQLite WAL mode?" {
		t.Errorf("unexpected first question: %q", plan.SubQuestions[0].Question)
	}
	if plan.ReportOutline[1] != "Concurrency Architecture" {
		t.Errorf("unexpected outline section: %q", plan.ReportOutline[1])
	}
}

func TestPlanner_Plan_InvalidJSON_ReturnsError(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]interface{}{
						"role":    "assistant",
						"content": "I am not returning JSON here",
					},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mockServer.Close()

	provCfg := config.ProviderConfig{
		BaseURL: mockServer.URL,
		Model:   "mock-planner-model",
		APIKey:  "test-key",
	}
	client := llm.NewClient(provCfg)
	planner := NewPlanner(client, nil)

	_, err := planner.Plan(context.Background(), "invalid goal")
	if err == nil {
		t.Fatal("expected error on invalid JSON response, got nil")
	}
}

func TestPlanner_ReflectAndReplan_WithNewQuestions(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]interface{}{
						"role": "assistant",
						"content": `{
							"new_questions": [
								"How does checkpointing work in WAL mode?"
							]
						}`,
					},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mockServer.Close()

	provCfg := config.ProviderConfig{
		BaseURL: mockServer.URL,
		Model:   "mock-planner-model",
		APIKey:  "test-key",
	}
	client := llm.NewClient(provCfg)
	planner := NewPlanner(client, nil)

	plan := ResearchPlan{
		Goal: "Explain SQLite WAL mode",
		SubQuestions: []SubQuestion{
			{Question: "What is WAL mode?"},
		},
	}
	findings := []store.Finding{
		{
			Claim:      "WAL allows concurrent readers and a single writer.",
			SourceURL:  "https://sqlite.org/wal.html",
			Confidence: 0.95,
		},
	}

	newQuestions, err := planner.ReflectAndReplan(context.Background(), plan, findings)
	if err != nil {
		t.Fatalf("unexpected error during reflection: %v", err)
	}

	if len(newQuestions) != 1 {
		t.Fatalf("expected 1 new question, got %d", len(newQuestions))
	}
	if newQuestions[0] != "How does checkpointing work in WAL mode?" {
		t.Errorf("unexpected question: %q", newQuestions[0])
	}
}

func TestPlanner_ReflectAndReplan_CoverageSufficient(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]interface{}{
						"role": "assistant",
						"content": `{
							"new_questions": []
						}`,
					},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mockServer.Close()

	provCfg := config.ProviderConfig{
		BaseURL: mockServer.URL,
		Model:   "mock-planner-model",
		APIKey:  "test-key",
	}
	client := llm.NewClient(provCfg)
	planner := NewPlanner(client, nil)

	plan := ResearchPlan{Goal: "Topic"}
	newQuestions, err := planner.ReflectAndReplan(context.Background(), plan, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(newQuestions) != 0 {
		t.Errorf("expected 0 new questions when coverage is sufficient, got %d", len(newQuestions))
	}
}
