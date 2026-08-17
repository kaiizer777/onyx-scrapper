package quality_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaiizer777/onyx-scrapper/internal/config"
	"github.com/kaiizer777/onyx-scrapper/internal/discovery"
	"github.com/kaiizer777/onyx-scrapper/internal/llm"
	"github.com/kaiizer777/onyx-scrapper/internal/quality"
	"github.com/kaiizer777/onyx-scrapper/internal/research"
	"github.com/kaiizer777/onyx-scrapper/internal/store"
	"github.com/kaiizer777/onyx-scrapper/test/fixtures"
)

type mockSearchRegistryProvider struct {
	results []discovery.SearchResult
}

func (m *mockSearchRegistryProvider) Name() string {
	return "mock-search"
}

func (m *mockSearchRegistryProvider) Search(ctx context.Context, query string, limit int) ([]discovery.SearchResult, error) {
	return m.results, nil
}

type mockFetchRegistryProvider struct {
	page *discovery.PageContent
}

func (m *mockFetchRegistryProvider) Name() string {
	return "mock-fetch"
}

func (m *mockFetchRegistryProvider) Fetch(ctx context.Context, targetURL string, opts discovery.FetchOptions) (*discovery.PageContent, error) {
	if m.page != nil {
		return m.page, nil
	}
	return &discovery.PageContent{
		URL:       targetURL,
		CleanText: "Tim Cook is the current Chief Executive Officer (CEO) of Apple Inc. as of 2026.",
		RawHTML:   "<html><body><p>Tim Cook is the current Chief Executive Officer (CEO) of Apple Inc.</p></body></html>",
		Provider:  "mock-fetch",
		FetchedAt: time.Now(),
	}, nil
}

func TestIntegration_AllEightBugScenarios(t *testing.T) {
	detector := quality.NewEntityDetector()
	authMgr := quality.NewAuthorityManager()
	engine := quality.NewCorroborationEngine(authMgr)

	for _, sc := range fixtures.Scenarios {
		sc := sc
		t.Run(sc.Name, func(t *testing.T) {
			switch sc.BugID {
			case 1: // query_template_mismatch
				entity := detector.Detect(sc.Claim)
				if entity.Type != sc.ExpectedEntityType {
					t.Fatalf("expected entity type %v, got %v for claim %q", sc.ExpectedEntityType, entity.Type, sc.Claim)
				}
				if sc.ExpectedSubject != "" && !strings.Contains(strings.ToLower(entity.Subject), strings.ToLower(sc.ExpectedSubject)) {
					t.Errorf("expected subject containing %q, got %q", sc.ExpectedSubject, entity.Subject)
				}
				query := quality.BuildVerificationQuery(entity)
				for _, expected := range sc.ExpectedQueryContains {
					if !strings.Contains(strings.ToLower(query), strings.ToLower(expected)) {
						t.Errorf("query %q missing expected substring %q", query, expected)
					}
				}
				for _, notExpected := range sc.ExpectedQueryNotContains {
					if strings.Contains(strings.ToLower(query), strings.ToLower(notExpected)) {
						t.Errorf("query %q contains forbidden substring %q", query, notExpected)
					}
				}

			case 2: // bracketed_llm_result
				res, val, ok := quality.ParseVerificationResult(sc.MockLLMResponse)
				if ok != sc.ExpectedResultOk {
					t.Fatalf("expected ok=%v, got ok=%v", sc.ExpectedResultOk, ok)
				}
				if res != sc.ExpectedResult {
					t.Errorf("expected result %v, got %v", sc.ExpectedResult, res)
				}
				if sc.ExpectedValue != "" && !strings.Contains(val, sc.ExpectedValue) {
					t.Errorf("expected value containing %q, got %q", sc.ExpectedValue, val)
				}

			case 3: // cache_key_collision
				tokens := make(map[string]bool)
				for _, claim := range sc.Claims {
					entity := detector.Detect(claim)
					tok := quality.CacheToken(entity, claim)
					if tok == "" {
						t.Fatalf("CacheToken returned empty string for claim %q", claim)
					}
					if sc.ExpectDistinctCacheTokens && tokens[tok] {
						t.Errorf("duplicate cache token %q generated for claim %q", tok, claim)
					}
					tokens[tok] = true
				}

			case 4: // paraphrase_corroboration
				labeled := engine.GroupAndLabelFindings(sc.Findings)
				if len(labeled) != sc.ExpectedClusterCount {
					t.Fatalf("expected %d cluster, got %d (%v)", sc.ExpectedClusterCount, len(labeled), labeled)
				}
				if sc.ExpectedLabelContains != "" && !strings.Contains(labeled[0], sc.ExpectedLabelContains) {
					t.Errorf("expected cluster label containing %q, got %q", sc.ExpectedLabelContains, labeled[0])
				}

			case 5: // same_domain_false_multi
				labeled := engine.GroupAndLabelFindings(sc.Findings)
				if len(labeled) != sc.ExpectedClusterCount {
					t.Fatalf("expected %d cluster, got %d (%v)", sc.ExpectedClusterCount, len(labeled), labeled)
				}
				for _, forbidden := range sc.ExpectedLabelNotContains {
					if strings.Contains(strings.ToLower(labeled[0]), strings.ToLower(forbidden)) {
						t.Errorf("label %q unexpectedly contains forbidden multi-source tag %q", labeled[0], forbidden)
					}
				}

			case 6: // low_confidence_leak
				if sc.Confidence < sc.MinConfidence && sc.ExpectPersisted {
					t.Errorf("confidence %.2f < threshold %.2f but marked as expect persisted", sc.Confidence, sc.MinConfidence)
				}

			case 7: // contradicted_reaches_synthesis
				active, excluded := research.SplitFindingsForSynthesis([]store.Finding{
					{Claim: sc.Claim, Status: sc.FindingStatus},
				})
				if sc.ExpectInSynthesisPrompt && len(active) == 0 {
					t.Errorf("expected finding to be active in synthesis prompt, but got excluded")
				}
				if !sc.ExpectInSynthesisPrompt && len(active) > 0 {
					t.Errorf("expected contradicted finding to be excluded from synthesis prompt, but was active")
				}
				if sc.ExpectInExcludedSection && len(excluded) == 0 {
					t.Errorf("expected finding to be in excluded section, but got 0 excluded")
				}

			case 8: // agent_bypasses_pipeline
				if !sc.ExpectPipelineSuccess {
					t.Errorf("expected pipeline success for scenario %s", sc.Name)
				}
			}
		})
	}
}

func TestIntegration_SecondSourceVerifier_EndToEnd(t *testing.T) {
	var llmCallCount int32
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&llmCallCount, 1)
		w.Header().Set("Content-Type", "application/json")
		_ = r.Body.Close()
		_, _ = w.Write([]byte(`{
			"choices": [
				{
					"message": {
						"role": "assistant",
						"content": "RESULT: [CONFIRMED]\nVALUE: []"
					}
				}
			]
		}`))
	}))
	defer mockServer.Close()

	provCfg := config.ProviderConfig{
		BaseURL: mockServer.URL,
		Model:   "mock-model",
		APIKey:  "test-key",
	}
	llmClient := llm.NewClient(provCfg)

	searcher := &mockSearchRegistryProvider{
		results: []discovery.SearchResult{
			{URL: "https://apple.com/leadership", Title: "Apple Leadership", Snippet: "Tim Cook is CEO", Provider: "mock-search"},
		},
	}
	fetcher := &mockFetchRegistryProvider{}
	reg := discovery.NewRegistry([]discovery.SearchProvider{searcher}, map[string]discovery.FetchProvider{"mock-fetch": fetcher}, []string{"mock-fetch"}, nil)

	st, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	defer st.Close()

	budget := quality.NewBudget(10)
	verifier := quality.NewSecondSourceVerifier(llmClient, reg, st, budget, 24)

	claim := "Tim Cook is the CEO of Apple"
	res, val, err := verifier.VerifyClaim(context.Background(), claim)
	if err != nil {
		t.Fatalf("VerifyClaim failed: %v", err)
	}
	if res != quality.ResultConfirmed {
		t.Errorf("expected ResultConfirmed, got %v", res)
	}
	if val != "" {
		t.Errorf("expected empty val, got %q", val)
	}
	if atomic.LoadInt32(&llmCallCount) != 1 {
		t.Errorf("expected 1 LLM call, got %d", atomic.LoadInt32(&llmCallCount))
	}

	// Second identical call must hit the SQLite entity_cache and avoid LLM call
	res2, val2, err2 := verifier.VerifyClaim(context.Background(), claim)
	if err2 != nil {
		t.Fatalf("second VerifyClaim failed: %v", err2)
	}
	if res2 != quality.ResultConfirmed || val2 != "" {
		t.Errorf("cached result mismatch: %v, %q", res2, val2)
	}
	if atomic.LoadInt32(&llmCallCount) != 1 {
		t.Errorf("expected LLM call count to stay 1 (cache hit), got %d", atomic.LoadInt32(&llmCallCount))
	}
}

func TestIntegration_CorroborationEngine_NumericConflict(t *testing.T) {
	authMgr := quality.NewAuthorityManager()
	engine := quality.NewCorroborationEngine(authMgr)

	findings := []store.Finding{
		{
			Claim:     "Company reported Q4 revenue of $50B",
			SourceURL: "https://reuters.com/article1",
		},
		{
			Claim:     "Company reported Q4 revenue of $30B",
			SourceURL: "https://bloomberg.com/article2",
		},
	}

	labeled := engine.GroupAndLabelFindings(findings)
	if len(labeled) != 2 {
		t.Fatalf("expected 2 separate clusters for disagreeing numbers, got %d", len(labeled))
	}

	for _, l := range labeled {
		if !strings.Contains(l, "[conflicting-values-detected]") {
			t.Errorf("expected cluster output to contain '[conflicting-values-detected]', got %q", l)
		}
	}
}
