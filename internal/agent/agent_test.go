package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kaiizer777/onyx-scrapper/internal/config"
	"github.com/kaiizer777/onyx-scrapper/internal/discovery"
	"github.com/kaiizer777/onyx-scrapper/internal/llm"
	"github.com/kaiizer777/onyx-scrapper/internal/quality"
	"github.com/kaiizer777/onyx-scrapper/internal/store"
)

func TestActionResponseParsing(t *testing.T) {
	rawJSON := `{
		"thought": "I will navigate to Hacker News",
		"action": {
			"name": "navigate",
			"args": {
				"url": "https://news.ycombinator.com"
			}
		}
	}`

	var resp ActionResponse
	err := json.Unmarshal([]byte(rawJSON), &resp)
	if err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if resp.Thought != "I will navigate to Hacker News" {
		t.Errorf("unexpected thought: %s", resp.Thought)
	}

	if resp.Action.Name != "navigate" {
		t.Errorf("unexpected action name: %s", resp.Action.Name)
	}

	var navArgs NavigateArgs
	err = json.Unmarshal(resp.Action.Args, &navArgs)
	if err != nil {
		t.Fatalf("Unmarshal args failed: %v", err)
	}

	if navArgs.URL != "https://news.ycombinator.com" {
		t.Errorf("unexpected url arg: %s", navArgs.URL)
	}
}

func TestAgentOptions(t *testing.T) {
	ag := NewAgent(nil, nil, WithMaxSteps(5), WithMinConfidence(0.7))
	if ag.maxSteps != 5 {
		t.Errorf("expected maxSteps 5, got %d", ag.maxSteps)
	}
	if ag.minConfidence != 0.7 {
		t.Errorf("expected minConfidence 0.7, got %f", ag.minConfidence)
	}
}

func TestWebSearchActionParsing(t *testing.T) {
	rawJSON := `{
		"thought": "I need to search for Go scraping libraries",
		"action": {
			"name": "web_search",
			"args": {
				"query": "go web scraping"
			}
		}
	}`

	var resp ActionResponse
	err := json.Unmarshal([]byte(rawJSON), &resp)
	if err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if resp.Action.Name != "web_search" {
		t.Errorf("expected action 'web_search', got %s", resp.Action.Name)
	}

	var searchArgs WebSearchArgs
	err = json.Unmarshal(resp.Action.Args, &searchArgs)
	if err != nil {
		t.Fatalf("Unmarshal search args failed: %v", err)
	}

	if searchArgs.Query != "go web scraping" {
		t.Errorf("expected query 'go web scraping', got %s", searchArgs.Query)
	}
}

func TestRecordFinding_URLNotInHistory_DroppedWithObservableError(t *testing.T) {
	st, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	defer st.Close()

	runID, err := st.CreateAgentRun("Test Goal")
	if err != nil {
		t.Fatalf("CreateAgentRun failed: %v", err)
	}

	ag := NewAgent(nil, st)
	ag.AddVisitedURL("https://legit-news.com/article1")

	// Claiming URL that was never visited
	args := RecordFindingArgs{
		Claim:      "Some factual claim",
		SourceURL:  "https://fabricated-site.com/fake",
		Confidence: 0.9,
	}

	_, err = ag.handleRecordFinding(context.Background(), runID, args, "")
	if err == nil {
		t.Fatalf("expected error for ungrounded URL, got nil")
	}
	if !errors.Is(err, ErrUngroundedFinding) {
		t.Errorf("expected ErrUngroundedFinding, got %v", err)
	}

	// Verify finding was never persisted in the store
	findings, err := st.GetFindingsByAgentRun(runID)
	if err != nil {
		t.Fatalf("GetFindingsByAgentRun failed: %v", err)
	}
	if len(findings) != 0 {
		t.Errorf("expected 0 persisted findings, got %d", len(findings))
	}
}

func TestRecordFinding_BlankURL_DefaultsToCurrentOrLastVisited(t *testing.T) {
	st, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	defer st.Close()

	runID, err := st.CreateAgentRun("Test Goal")
	if err != nil {
		t.Fatalf("CreateAgentRun failed: %v", err)
	}

	ag := NewAgent(nil, st)
	ag.AddVisitedURL("https://legit-news.com/article1")

	args := RecordFindingArgs{
		Claim:      "General fact about the world",
		SourceURL:  "", // blank
		Confidence: 0.9,
	}

	res, err := ag.handleRecordFinding(context.Background(), runID, args, "https://legit-news.com/article1")
	if err != nil {
		t.Fatalf("handleRecordFinding failed: %v", err)
	}
	if !strings.Contains(res, "Successfully recorded finding") {
		t.Errorf("unexpected step result: %s", res)
	}

	findings, err := st.GetFindingsByAgentRun(runID)
	if err != nil {
		t.Fatalf("GetFindingsByAgentRun failed: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}
	if findings[0].SourceURL != "https://legit-news.com/article1" {
		t.Errorf("expected grounded URL 'https://legit-news.com/article1', got %q", findings[0].SourceURL)
	}
}

func TestRecordFinding_LowConfidence_Dropped(t *testing.T) {
	st, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	defer st.Close()

	runID, err := st.CreateAgentRun("Test Goal")
	if err != nil {
		t.Fatalf("CreateAgentRun failed: %v", err)
	}

	ag := NewAgent(nil, st, WithMinConfidence(0.6))
	ag.AddVisitedURL("https://example.com/page")

	args := RecordFindingArgs{
		Claim:      "Low confidence speculation",
		SourceURL:  "https://example.com/page",
		Confidence: 0.3, // below 0.6 threshold
	}

	_, err = ag.handleRecordFinding(context.Background(), runID, args, "https://example.com/page")
	if err == nil {
		t.Fatalf("expected error for low confidence, got nil")
	}
	if !errors.Is(err, ErrLowConfidenceFinding) {
		t.Errorf("expected ErrLowConfidenceFinding, got %v", err)
	}

	findings, err := st.GetFindingsByAgentRun(runID)
	if err != nil {
		t.Fatalf("GetFindingsByAgentRun failed: %v", err)
	}
	if len(findings) != 0 {
		t.Errorf("expected 0 persisted findings, got %d", len(findings))
	}
}

type agentMockSearchProvider struct {
	name string
}

func (m *agentMockSearchProvider) Name() string { return m.name }
func (m *agentMockSearchProvider) Search(ctx context.Context, query string, limit int) ([]discovery.SearchResult, error) {
	return []discovery.SearchResult{
		{URL: "https://verified-source.com/news", Title: "Verified", Snippet: "New CEO appointed at Apple", Provider: m.name},
	}, nil
}

type agentMockFetchProvider struct {
	name string
}

func (m *agentMockFetchProvider) Name() string { return m.name }
func (m *agentMockFetchProvider) Fetch(ctx context.Context, targetURL string, opts discovery.FetchOptions) (*discovery.PageContent, error) {
	return &discovery.PageContent{
		URL:       targetURL,
		CleanText: "The new CEO of Apple is John Appleseed as of today.",
		RawHTML:   "<html><body>The new CEO of Apple is John Appleseed as of today.</body></html>",
		Provider:  m.name,
		FetchedAt: time.Now(),
	}, nil
}

func TestRecordFinding_FreshnessSensitiveClaim_TriggersSecondSourceVerification(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]interface{}{
						"role":    "assistant",
						"content": "RESULT: CONTRADICTED\nVALUE: John Appleseed",
					},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mockServer.Close()

	llmClient := llm.NewClient(config.ProviderConfig{
		BaseURL: mockServer.URL,
		APIKey:  "test-key",
		Model:   "test-model",
	})

	searcher := &agentMockSearchProvider{name: "test-search"}
	fetcher := &agentMockFetchProvider{name: "test-fetch"}
	registry := discovery.NewRegistry([]discovery.SearchProvider{searcher}, map[string]discovery.FetchProvider{"test-fetch": fetcher}, []string{"test-fetch"}, nil)

	st, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	defer st.Close()

	runID, err := st.CreateAgentRun("Test Leadership Goal")
	if err != nil {
		t.Fatalf("CreateAgentRun failed: %v", err)
	}

	verifier := quality.NewSecondSourceVerifier(llmClient, registry, st, nil, 24)
	ag := NewAgent(llmClient, st, WithRegistry(registry), WithSecondSourceVerifier(verifier))
	ag.AddVisitedURL("https://example.com/tech-history")

	args := RecordFindingArgs{
		Claim:      "CEO of Apple is Tim Cook",
		SourceURL:  "https://example.com/tech-history",
		Confidence: 0.9,
	}

	res, err := ag.handleRecordFinding(context.Background(), runID, args, "https://example.com/tech-history")
	if err != nil {
		t.Fatalf("handleRecordFinding failed: %v", err)
	}
	if !strings.Contains(res, "Successfully recorded finding") {
		t.Errorf("unexpected step result: %s", res)
	}

	findings, err := st.GetFindingsByAgentRun(runID)
	if err != nil {
		t.Fatalf("GetFindingsByAgentRun failed: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}
	if findings[0].Status != store.StatusContradicted {
		t.Errorf("expected status 'contradicted', got %s", findings[0].Status)
	}
	if findings[0].VerificationNote != "John Appleseed" {
		t.Errorf("expected note 'John Appleseed', got %q", findings[0].VerificationNote)
	}
	// Verify claim text is preserved without string mutations
	if findings[0].Claim != "CEO of Apple is Tim Cook" {
		t.Errorf("expected original claim preserved, got %q", findings[0].Claim)
	}
}

func TestRecordFinding_NonFreshnessSensitiveClaim_SkipsVerification(t *testing.T) {
	st, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	defer st.Close()

	runID, err := st.CreateAgentRun("Test Astronomy Goal")
	if err != nil {
		t.Fatalf("CreateAgentRun failed: %v", err)
	}

	ag := NewAgent(nil, st)
	ag.AddVisitedURL("https://science.org/astronomy")

	args := RecordFindingArgs{
		Claim:      "The Earth revolves around the Sun in an elliptical orbit",
		SourceURL:  "https://science.org/astronomy",
		Confidence: 0.95,
	}

	res, err := ag.handleRecordFinding(context.Background(), runID, args, "https://science.org/astronomy")
	if err != nil {
		t.Fatalf("handleRecordFinding failed: %v", err)
	}
	if !strings.Contains(res, "Successfully recorded finding") {
		t.Errorf("unexpected step result: %s", res)
	}

	findings, err := st.GetFindingsByAgentRun(runID)
	if err != nil {
		t.Fatalf("GetFindingsByAgentRun failed: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}
	if findings[0].Status != store.StatusActive {
		t.Errorf("expected status 'active', got %s", findings[0].Status)
	}
	if findings[0].VerificationNote != "" {
		t.Errorf("expected empty verification note, got %q", findings[0].VerificationNote)
	}
}

func TestRecordFinding_PersistsAuthorityTier(t *testing.T) {
	st, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	defer st.Close()

	runID, err := st.CreateAgentRun("Test Authority Goal")
	if err != nil {
		t.Fatalf("CreateAgentRun failed: %v", err)
	}

	yamlData := `primary:
  - ".gov"
  - "arxiv.org"
established:
  - "reuters.com"
general:
  - "reddit.com"
`
	tmpFile, err := os.CreateTemp("", "authority_test_*.yaml")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	if _, err := tmpFile.Write([]byte(yamlData)); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}
	tmpFile.Close()

	authManager := quality.NewAuthorityManager()
	if err := authManager.LoadTiers(tmpFile.Name()); err != nil {
		t.Fatalf("failed to load tiers: %v", err)
	}

	ag := NewAgent(nil, st, WithAuthorityManager(authManager))
	ag.AddVisitedURL("https://cdc.gov/flu/data")
	ag.AddVisitedURL("https://random-forum.com/thread")

	// Primary tier (.gov suffix)
	argsPrimary := RecordFindingArgs{
		Claim:      "Flu vaccination rates increased across all regions",
		SourceURL:  "https://cdc.gov/flu/data",
		Confidence: 0.9,
	}
	_, err = ag.handleRecordFinding(context.Background(), runID, argsPrimary, "https://cdc.gov/flu/data")
	if err != nil {
		t.Fatalf("handleRecordFinding primary failed: %v", err)
	}

	// General/Unknown tier
	argsGeneral := RecordFindingArgs{
		Claim:      "Some users report better sleep after tea",
		SourceURL:  "https://random-forum.com/thread",
		Confidence: 0.8,
	}
	_, err = ag.handleRecordFinding(context.Background(), runID, argsGeneral, "https://random-forum.com/thread")
	if err != nil {
		t.Fatalf("handleRecordFinding general failed: %v", err)
	}

	findings, err := st.GetFindingsByAgentRun(runID)
	if err != nil {
		t.Fatalf("GetFindingsByAgentRun failed: %v", err)
	}
	if len(findings) != 2 {
		t.Fatalf("expected 2 findings, got %d", len(findings))
	}

	if findings[0].AuthorityTier != int(quality.TierPrimary) {
		t.Errorf("expected primary tier (3), got %d", findings[0].AuthorityTier)
	}
	if findings[1].AuthorityTier != int(quality.TierUnknown) {
		t.Errorf("expected unknown tier (0), got %d", findings[1].AuthorityTier)
	}
}

func TestPostRunGroundingPass_StandaloneVsSubquestion(t *testing.T) {
	st, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	defer st.Close()

	// 1. Standalone run (subQuestionID == 0)
	runID, err := st.CreateAgentRun("Standalone Research Run")
	if err != nil {
		t.Fatalf("CreateAgentRun failed: %v", err)
	}

	agStandalone := NewAgent(nil, st) // subQuestionID == 0
	err = agStandalone.PostRunGroundingPass(context.Background(), runID)
	if err != nil {
		t.Fatalf("PostRunGroundingPass failed: %v", err)
	}

	run, err := st.GetAgentRun(runID)
	if err != nil {
		t.Fatalf("GetAgentRun failed: %v", err)
	}
	if !run.IsGrounded {
		t.Errorf("expected agent run to be marked grounded, got false")
	}
}

func TestPostRunGroundingPass_ReverifiesUnverifiedFreshnessSensitiveGroups(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]interface{}{
						"role":    "assistant",
						"content": "RESULT: CONTRADICTED\nVALUE: $105,000",
					},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mockServer.Close()

	llmClient := llm.NewClient(config.ProviderConfig{
		BaseURL: mockServer.URL,
		APIKey:  "test-key",
		Model:   "test-model",
	})

	searcher := &agentMockSearchProvider{name: "test-search"}
	fetcher := &agentMockFetchProvider{name: "test-fetch"}
	registry := discovery.NewRegistry([]discovery.SearchProvider{searcher}, map[string]discovery.FetchProvider{"test-fetch": fetcher}, []string{"test-fetch"}, nil)

	st, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	defer st.Close()

	runID, err := st.CreateAgentRun("Price Tracking")
	if err != nil {
		t.Fatalf("CreateAgentRun failed: %v", err)
	}

	// Insert unverified freshness-sensitive finding directly into store
	fID, err := st.InsertFinding(store.Finding{
		AgentRunID:       runID,
		Claim:            "Bitcoin current price is $95,000",
		SourceURL:        "https://example.com/btc",
		Confidence:       0.9,
		Status:           store.StatusActive,
		VerificationNote: "", // unverified
	})
	if err != nil {
		t.Fatalf("InsertFinding failed: %v", err)
	}

	verifier := quality.NewSecondSourceVerifier(llmClient, registry, st, nil, 24)
	ag := NewAgent(llmClient, st, WithRegistry(registry), WithSecondSourceVerifier(verifier))

	err = ag.PostRunGroundingPass(context.Background(), runID)
	if err != nil {
		t.Fatalf("PostRunGroundingPass failed: %v", err)
	}

	findings, err := st.GetFindingsByAgentRun(runID)
	if err != nil {
		t.Fatalf("GetFindingsByAgentRun failed: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}
	if findings[0].ID != fID {
		t.Fatalf("expected finding ID %d, got %d", fID, findings[0].ID)
	}
	if findings[0].Status != store.StatusContradicted {
		t.Errorf("expected status 'contradicted' after post-run pass, got %s", findings[0].Status)
	}
	if findings[0].VerificationNote != "$105,000" {
		t.Errorf("expected note '$105,000', got %q", findings[0].VerificationNote)
	}

	run, err := st.GetAgentRun(runID)
	if err != nil {
		t.Fatalf("GetAgentRun failed: %v", err)
	}
	if !run.IsGrounded {
		t.Errorf("expected agent run to be marked grounded")
	}
}

func TestRecordFinding_RespectsRateGovernor(t *testing.T) {
	callCount := 0
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]interface{}{
						"role":    "assistant",
						"content": "RESULT: CONFIRMED\nVALUE: ",
					},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mockServer.Close()

	llmClient := llm.NewClient(config.ProviderConfig{
		BaseURL: mockServer.URL,
		APIKey:  "test-key",
		Model:   "test-model",
	})

	searcher := &agentMockSearchProvider{name: "test-search"}
	fetcher := &agentMockFetchProvider{name: "test-fetch"}
	registry := discovery.NewRegistry([]discovery.SearchProvider{searcher}, map[string]discovery.FetchProvider{"test-fetch": fetcher}, []string{"test-fetch"}, nil)

	st, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	defer st.Close()

	runID, err := st.CreateAgentRun("Budget Capping Test")
	if err != nil {
		t.Fatalf("CreateAgentRun failed: %v", err)
	}

	budget := quality.NewBudget(2) // Allow at most 2 calls
	verifier := quality.NewSecondSourceVerifier(llmClient, registry, st, budget, 24)

	ag := NewAgent(llmClient, st, WithRegistry(registry), WithBudget(budget), WithSecondSourceVerifier(verifier))
	ag.AddVisitedURL("https://example.com/source")

	claims := []string{
		"CEO of Microsoft is Satya Nadella",
		"CEO of Google is Sundar Pichai",
		"CEO of Apple is Tim Cook",
		"CEO of Meta is Mark Zuckerberg",
		"CEO of Amazon is Andy Jassy",
	}

	for _, claim := range claims {
		args := RecordFindingArgs{
			Claim:      claim,
			SourceURL:  "https://example.com/source",
			Confidence: 0.9,
		}
		_, err := ag.handleRecordFinding(context.Background(), runID, args, "https://example.com/source")
		if err != nil {
			t.Fatalf("handleRecordFinding failed for %s: %v", claim, err)
		}
	}

	currCalls, maxCalls := budget.Stats()
	if currCalls > maxCalls {
		t.Errorf("budget exceeded: current %d > max %d", currCalls, maxCalls)
	}

	// Verify all 5 findings were persisted
	findings, err := st.GetFindingsByAgentRun(runID)
	if err != nil {
		t.Fatalf("GetFindingsByAgentRun failed: %v", err)
	}
	if len(findings) != 5 {
		t.Errorf("expected 5 persisted findings, got %d", len(findings))
	}
}


