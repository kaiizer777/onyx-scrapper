package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kaiizer777/onyx-scrapper/internal/config"
	"github.com/kaiizer777/onyx-scrapper/internal/discovery"
	"github.com/kaiizer777/onyx-scrapper/internal/llm"
	"github.com/kaiizer777/onyx-scrapper/internal/quality"
	"github.com/kaiizer777/onyx-scrapper/internal/store"
)

type integrationMockSearchProvider struct {
	results []discovery.SearchResult
}

func (m *integrationMockSearchProvider) Name() string {
	return "integration-search"
}

func (m *integrationMockSearchProvider) Search(ctx context.Context, query string, limit int) ([]discovery.SearchResult, error) {
	return m.results, nil
}

type integrationMockFetchProvider struct {
	content string
}

func (m *integrationMockFetchProvider) Name() string {
	return "integration-fetch"
}

func (m *integrationMockFetchProvider) Fetch(ctx context.Context, targetURL string, opts discovery.FetchOptions) (*discovery.PageContent, error) {
	text := "Corporate leadership updates: Tim Cook is CEO of Apple Inc. Sundar Pichai serves as CEO of Google and Alphabet Inc. as of current dates."
	if m.content != "" {
		text = m.content
	}
	return &discovery.PageContent{
		URL:       targetURL,
		CleanText: text,
		RawHTML:   "<html><body><p>" + text + "</p></body></html>",
		Provider:  "integration-fetch",
		FetchedAt: time.Now(),
	}, nil
}

func TestIntegration_AgentEndToEnd_FindingPassesFullPipeline(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var req struct {
			Messages []llm.Message `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)

		lastMsg := ""
		if len(req.Messages) > 0 {
			lastMsg = req.Messages[len(req.Messages)-1].Content
		}

		var respContent string
		if strings.Contains(lastMsg, `Larry Page is currently the CEO of Google`) {
			respContent = "RESULT: [CONTRADICTED]\nVALUE: Sundar Pichai is the CEO of Google"
		} else if strings.Contains(lastMsg, `Tim Cook is the current CEO of Apple`) {
			respContent = "RESULT: [CONFIRMED]\nVALUE: []"
		} else {
			respContent = "RESULT: [CONFIRMED]\nVALUE: []"
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
	defer mockServer.Close()

	llmClient := llm.NewClient(config.ProviderConfig{
		BaseURL: mockServer.URL,
		APIKey:  "test-key",
		Model:   "test-model",
	})

	dbPath := filepath.Join(t.TempDir(), "agent_integration.db")
	st, err := store.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	defer st.Close()

	runID, err := st.CreateAgentRun("Investigate Tech Leadership")
	if err != nil {
		t.Fatalf("CreateAgentRun failed: %v", err)
	}

	searcher := &integrationMockSearchProvider{
		results: []discovery.SearchResult{
			{URL: "https://apple.com/leadership", Title: "Apple Leadership", Snippet: "Tim Cook is CEO", Provider: "integration-search"},
			{URL: "https://google.com/about", Title: "Google Leadership", Snippet: "Sundar Pichai is CEO", Provider: "integration-search"},
		},
	}
	fetcher := &integrationMockFetchProvider{}
	registry := discovery.NewRegistry(
		[]discovery.SearchProvider{searcher},
		map[string]discovery.FetchProvider{"integration-fetch": fetcher},
		[]string{"integration-fetch"},
		nil,
	)

	// Configure Authority Manager with tiers
	tierCfgFile := filepath.Join(t.TempDir(), "tiers.yaml")
	tierYaml := `primary:
  - "apple.com"
  - "google.com"
  - ".gov"
established:
  - "reuters.com"
general:
  - "techblog.org"
`
	if err := os.WriteFile(tierCfgFile, []byte(tierYaml), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	authMgr := quality.NewAuthorityManager()
	if err := authMgr.LoadTiers(tierCfgFile); err != nil {
		t.Fatalf("LoadTiers failed: %v", err)
	}

	verifier := quality.NewSecondSourceVerifier(llmClient, registry, st, quality.NewBudget(10), 24)
	corroborator := quality.NewCorroborationEngine(authMgr)

	ag := NewAgent(
		llmClient,
		st,
		WithRegistry(registry),
		WithAuthorityManager(authMgr),
		WithSecondSourceVerifier(verifier),
		WithCorroborationEngine(corroborator),
		WithMinConfidence(0.4),
	)

	// 1. Add visited URLs to navigation history
	visitedAppleURL := "https://apple.com/leadership/tim-cook"
	visitedGoogleURL := "https://google.com/about/leadership"
	ag.AddVisitedURL(visitedAppleURL)
	ag.AddVisitedURL(visitedGoogleURL)

	// 2. Record a valid, freshness-sensitive confirmed claim
	args1 := RecordFindingArgs{
		Claim:      "Tim Cook is the current CEO of Apple",
		SourceURL:  visitedAppleURL,
		Confidence: 0.92,
	}

	res1, err := ag.handleRecordFinding(context.Background(), runID, args1, visitedAppleURL)
	if err != nil {
		t.Fatalf("handleRecordFinding failed: %v", err)
	}
	if !strings.Contains(res1, "Successfully recorded finding") {
		t.Errorf("unexpected execution result: %s", res1)
	}

	// 3. Record a freshness-sensitive contradicted claim
	args2 := RecordFindingArgs{
		Claim:      "Larry Page is currently the CEO of Google",
		SourceURL:  visitedGoogleURL,
		Confidence: 0.88,
	}

	res2, err := ag.handleRecordFinding(context.Background(), runID, args2, visitedGoogleURL)
	if err != nil {
		t.Fatalf("handleRecordFinding 2 failed: %v", err)
	}
	if !strings.Contains(res2, "Successfully recorded finding") {
		t.Errorf("unexpected execution result 2: %s", res2)
	}

	// 4. Inspect persisted SQLite findings
	findings, err := st.GetFindingsByAgentRun(runID)
	if err != nil {
		t.Fatalf("GetFindingsByAgentRun failed: %v", err)
	}
	if len(findings) != 2 {
		t.Fatalf("expected 2 findings in store, got %d", len(findings))
	}

	f1 := findings[0]
	if f1.Status != store.StatusActive {
		t.Errorf("expected finding 1 status 'active', got %s", f1.Status)
	}
	if f1.AuthorityTier != int(quality.TierPrimary) {
		t.Errorf("expected finding 1 authority tier Primary (%d), got %d", quality.TierPrimary, f1.AuthorityTier)
	}
	if f1.SourceURL != visitedAppleURL {
		t.Errorf("expected source URL %q, got %q", visitedAppleURL, f1.SourceURL)
	}

	f2 := findings[1]
	if f2.Status != store.StatusContradicted {
		t.Errorf("expected finding 2 status 'contradicted', got %s", f2.Status)
	}
	if f2.AuthorityTier != int(quality.TierPrimary) {
		t.Errorf("expected finding 2 authority tier Primary (%d), got %d", quality.TierPrimary, f2.AuthorityTier)
	}
	if f2.VerificationNote != "Sundar Pichai is the CEO of Google" {
		t.Errorf("expected verification note 'Sundar Pichai is the CEO of Google', got %q", f2.VerificationNote)
	}
	if f2.Claim != "Larry Page is currently the CEO of Google" {
		t.Errorf("claim text was unexpectedly mutated: %q", f2.Claim)
	}
}

func TestIntegration_Agent_UngroundedURLAndLowConfidenceRejection(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "agent_rejections.db")
	st, err := store.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	defer st.Close()

	runID, err := st.CreateAgentRun("Rejection Test Goal")
	if err != nil {
		t.Fatalf("CreateAgentRun failed: %v", err)
	}

	ag := NewAgent(nil, st, WithMinConfidence(0.5))
	ag.AddVisitedURL("https://legit-source.com/page1")

	// Case 1: Ungrounded URL (never visited)
	ungroundedArgs := RecordFindingArgs{
		Claim:      "Factual statement",
		SourceURL:  "https://unvisited-hallucinated.com/fake",
		Confidence: 0.9,
	}

	_, err = ag.handleRecordFinding(context.Background(), runID, ungroundedArgs, "https://legit-source.com/page1")
	if err == nil {
		t.Fatalf("expected error for ungrounded URL, got nil")
	}
	if !errors.Is(err, ErrUngroundedFinding) {
		t.Errorf("expected ErrUngroundedFinding, got %v", err)
	}

	// Case 2: Low confidence (below 0.5 threshold)
	lowConfArgs := RecordFindingArgs{
		Claim:      "Low confidence rumor",
		SourceURL:  "https://legit-source.com/page1",
		Confidence: 0.25,
	}

	_, err = ag.handleRecordFinding(context.Background(), runID, lowConfArgs, "https://legit-source.com/page1")
	if err == nil {
		t.Fatalf("expected error for low confidence claim, got nil")
	}
	if !errors.Is(err, ErrLowConfidenceFinding) {
		t.Errorf("expected ErrLowConfidenceFinding, got %v", err)
	}

	// Ensure no findings were written to SQLite
	findings, err := st.GetFindingsByAgentRun(runID)
	if err != nil {
		t.Fatalf("GetFindingsByAgentRun failed: %v", err)
	}
	if len(findings) != 0 {
		t.Errorf("expected 0 persisted findings after rejections, got %d", len(findings))
	}
}

func TestIntegration_Agent_PostRunGroundingPass_StandaloneMode(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]interface{}{
						"role":    "assistant",
						"content": "RESULT: [CONFIRMED]\nVALUE: []",
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

	dbPath := filepath.Join(t.TempDir(), "agent_grounding_pass.db")
	st, err := store.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	defer st.Close()

	runID, err := st.CreateAgentRun("Standalone Grounding Pass Goal")
	if err != nil {
		t.Fatalf("CreateAgentRun failed: %v", err)
	}

	searcher := &integrationMockSearchProvider{
		results: []discovery.SearchResult{
			{URL: "https://apple.com/leadership", Title: "Apple Leadership", Snippet: "Tim Cook is CEO", Provider: "integration-search"},
		},
	}
	fetcher := &integrationMockFetchProvider{}
	registry := discovery.NewRegistry(
		[]discovery.SearchProvider{searcher},
		map[string]discovery.FetchProvider{"integration-fetch": fetcher},
		[]string{"integration-fetch"},
		nil,
	)

	verifier := quality.NewSecondSourceVerifier(llmClient, registry, st, quality.NewBudget(10), 24)
	authMgr := quality.NewAuthorityManager()
	corroborator := quality.NewCorroborationEngine(authMgr)

	ag := NewAgent(
		llmClient,
		st,
		WithRegistry(registry),
		WithAuthorityManager(authMgr),
		WithSecondSourceVerifier(verifier),
		WithCorroborationEngine(corroborator),
	)

	// Pre-insert an unverified finding into the database for this agent run
	_, err = st.InsertFinding(store.Finding{
		AgentRunID:     runID,
		Claim:          "Tim Cook is the current CEO of Apple",
		SourceURL:      "https://apple.com/leadership",
		SourceProvider: "agent",
		Confidence:     0.9,
		Status:         store.StatusActive,
	})
	if err != nil {
		t.Fatalf("InsertFinding failed: %v", err)
	}

	// Verify run is not yet grounded
	runBefore, err := st.GetAgentRun(runID)
	if err != nil {
		t.Fatalf("GetAgentRun failed: %v", err)
	}
	if runBefore.IsGrounded {
		t.Errorf("expected initial run IsGrounded to be false")
	}

	// Execute PostRunGroundingPass
	err = ag.PostRunGroundingPass(context.Background(), runID)
	if err != nil {
		t.Fatalf("PostRunGroundingPass failed: %v", err)
	}

	// Verify run is marked grounded
	runAfter, err := st.GetAgentRun(runID)
	if err != nil {
		t.Fatalf("GetAgentRun after pass failed: %v", err)
	}
	if !runAfter.IsGrounded {
		t.Errorf("expected run IsGrounded to be true after PostRunGroundingPass")
	}
}
