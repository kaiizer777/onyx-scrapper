package research

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/kaiizer777/onyx-scrapper/internal/config"
	"github.com/kaiizer777/onyx-scrapper/internal/llm"
	"github.com/kaiizer777/onyx-scrapper/internal/store"
)

func TestSplitFindingsForSynthesis(t *testing.T) {
	findings := []store.Finding{
		{ID: 1, Claim: "Active claim 1", Status: store.StatusActive},
		{ID: 2, Claim: "Unspecified status defaults to active", Status: ""},
		{ID: 3, Claim: "Contradicted claim 1", Status: store.StatusContradicted, VerificationNote: "Contradicted by v2"},
		{ID: 4, Claim: "Unclear claim 1", Status: store.StatusUnclear, VerificationNote: "Second source inconclusive"},
		{ID: 5, Claim: "Active claim 2", Status: store.StatusActive},
	}

	active, excluded := SplitFindingsForSynthesis(findings)

	if len(active) != 3 {
		t.Fatalf("expected 3 active findings, got %d", len(active))
	}
	if active[0].Claim != "Active claim 1" || active[1].Claim != "Unspecified status defaults to active" || active[2].Claim != "Active claim 2" {
		t.Errorf("unexpected active findings: %+v", active)
	}

	if len(excluded) != 2 {
		t.Fatalf("expected 2 excluded findings, got %d", len(excluded))
	}
	if excluded[0].Claim != "Contradicted claim 1" || excluded[0].Status != store.StatusContradicted {
		t.Errorf("unexpected excluded finding 0: %+v", excluded[0])
	}
	if excluded[1].Claim != "Unclear claim 1" || excluded[1].Status != store.StatusUnclear {
		t.Errorf("unexpected excluded finding 1: %+v", excluded[1])
	}
}

func TestSynthesizer_ExcludesContradictedFindingsFromPrompt(t *testing.T) {
	var receivedPrompt string
	var mu sync.Mutex

	mockLLMServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)

		mu.Lock()
		if messages, ok := body["messages"].([]interface{}); ok {
			for _, m := range messages {
				if msgMap, ok := m.(map[string]interface{}); ok {
					if msgMap["role"] == "user" {
						receivedPrompt = msgMap["content"].(string)
					}
				}
			}
		}
		mu.Unlock()

		resp := map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]interface{}{
						"role":    "assistant",
						"content": "# Research Report\n\nActive findings synthesized successfully.",
					},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mockLLMServer.Close()

	client := llm.NewClient(config.ProviderConfig{
		BaseURL: mockLLMServer.URL,
		Model:   "mock-model",
		APIKey:  "test-key",
	})
	synth := NewSynthesizer(client, nil)

	plan := ResearchPlan{
		Goal:          "Investigate Kubernetes versions",
		ReportOutline: []string{"Introduction", "Details", "Conclusion"},
	}

	findings := []store.Finding{
		{
			Claim:      "Kubernetes latest version is 1.30",
			SourceURL:  "https://kubernetes.io/release-1-30",
			Confidence: 0.95,
			Status:     store.StatusActive,
		},
		{
			Claim:            "Kubernetes latest version is 1.20",
			SourceURL:        "https://outdated-blog.com/k8s",
			Confidence:       0.85,
			Status:           store.StatusContradicted,
			VerificationNote: "Contradicted: official site reports v1.30 as current",
		},
		{
			Claim:            "Kubernetes 2.0 release date announced",
			SourceURL:        "https://rumors.example.com",
			Confidence:       0.50,
			Status:           store.StatusUnclear,
			VerificationNote: "Inconclusive second source",
		},
	}

	_, err := synth.Synthesize(context.Background(), plan, findings)
	if err != nil {
		t.Fatalf("Synthesize failed: %v", err)
	}

	mu.Lock()
	prompt := receivedPrompt
	mu.Unlock()

	if strings.Contains(prompt, "Kubernetes latest version is 1.20") {
		t.Errorf("expected contradicted claim to be excluded from LLM prompt, but found it in prompt: %s", prompt)
	}
	if strings.Contains(prompt, "Kubernetes 2.0 release date announced") {
		t.Errorf("expected unclear claim to be excluded from LLM prompt, but found it in prompt: %s", prompt)
	}
	if strings.Contains(prompt, "https://outdated-blog.com/k8s") {
		t.Errorf("expected contradicted URL to be excluded from LLM prompt, but found it in prompt: %s", prompt)
	}
}

func TestSynthesizer_IncludesActiveFindingsInPrompt(t *testing.T) {
	var receivedPrompt string
	var mu sync.Mutex

	mockLLMServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)

		mu.Lock()
		if messages, ok := body["messages"].([]interface{}); ok {
			for _, m := range messages {
				if msgMap, ok := m.(map[string]interface{}); ok {
					if msgMap["role"] == "user" {
						receivedPrompt = msgMap["content"].(string)
					}
				}
			}
		}
		mu.Unlock()

		resp := map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]interface{}{
						"role":    "assistant",
						"content": "# Research Report\n\nVerified active claims included.",
					},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mockLLMServer.Close()

	client := llm.NewClient(config.ProviderConfig{
		BaseURL: mockLLMServer.URL,
		Model:   "mock-model",
		APIKey:  "test-key",
	})
	synth := NewSynthesizer(client, nil)

	plan := ResearchPlan{
		Goal:          "Investigate SQLite concurrency",
		ReportOutline: []string{"WAL Mode", "Performance"},
	}

	activeClaim := "SQLite WAL mode allows multiple readers and one writer concurrently."
	activeURL := "https://sqlite.org/wal.html"

	findings := []store.Finding{
		{
			Claim:      activeClaim,
			SourceURL:  activeURL,
			Confidence: 0.98,
			Status:     store.StatusActive,
		},
	}

	_, err := synth.Synthesize(context.Background(), plan, findings)
	if err != nil {
		t.Fatalf("Synthesize failed: %v", err)
	}

	mu.Lock()
	prompt := receivedPrompt
	mu.Unlock()

	if !strings.Contains(prompt, activeClaim) {
		t.Errorf("expected active claim in prompt, but not found: %s", prompt)
	}
	if !strings.Contains(prompt, activeURL) {
		t.Errorf("expected active URL in prompt, but not found: %s", prompt)
	}
}

func TestSynthesizer_ExcludedFindingsAppearInReportMetadataNotNarrative(t *testing.T) {
	mockLLMServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]interface{}{
						"role":    "assistant",
						"content": "# Executive Summary\n\nThis is the pure LLM narrative based solely on active facts.",
					},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mockLLMServer.Close()

	client := llm.NewClient(config.ProviderConfig{
		BaseURL: mockLLMServer.URL,
		Model:   "mock-model",
		APIKey:  "test-key",
	})
	synth := NewSynthesizer(client, nil)

	plan := ResearchPlan{
		Goal:          "Company Leadership",
		ReportOutline: []string{"Leadership Overview"},
	}

	contradictedClaim := "Old CEO is Alice"
	contradictedNote := "Alice retired in 2024; Bob is current CEO"

	findings := []store.Finding{
		{
			Claim:      "Current CEO is Bob",
			SourceURL:  "https://company.com/press",
			Confidence: 0.95,
			Status:     store.StatusActive,
		},
		{
			Claim:            contradictedClaim,
			SourceURL:        "https://old-article.com",
			Confidence:       0.80,
			Status:           store.StatusContradicted,
			VerificationNote: contradictedNote,
		},
	}

	report, err := synth.Synthesize(context.Background(), plan, findings)
	if err != nil {
		t.Fatalf("Synthesize failed: %v", err)
	}

	// 1. Narrative portion (before the metadata heading) should not contain contradicted claim
	narrativeEnd := strings.Index(report, "### Excluded Findings")
	if narrativeEnd == -1 {
		t.Fatalf("expected Excluded Findings section in report metadata, but not found: %s", report)
	}

	narrative := report[:narrativeEnd]
	if strings.Contains(narrative, contradictedClaim) {
		t.Errorf("contradicted claim should not appear in main narrative: %s", narrative)
	}

	// 2. Metadata portion (after the heading) should surface the contradicted claim and note
	metadata := report[narrativeEnd:]
	if !strings.Contains(metadata, contradictedClaim) {
		t.Errorf("expected metadata section to surface contradicted claim, got: %s", metadata)
	}
	if !strings.Contains(metadata, contradictedNote) {
		t.Errorf("expected metadata section to surface verification note, got: %s", metadata)
	}
	if !strings.Contains(metadata, "[CONTRADICTED]") {
		t.Errorf("expected metadata section to label status as [CONTRADICTED], got: %s", metadata)
	}
}
