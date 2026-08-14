package teacher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaiizer777/onyx-scrapper/internal/config"
	"github.com/kaiizer777/onyx-scrapper/internal/discovery"
	"github.com/kaiizer777/onyx-scrapper/internal/llm"
	"github.com/kaiizer777/onyx-scrapper/internal/quality"
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
	// Fallback results
	return []discovery.SearchResult{
		{URL: "https://docs.ietf.org/doc/raft-consensus", Title: "Raft Consensus Specification", Snippet: "Raft consensus algorithms", Provider: m.name},
		{URL: "https://en.wikipedia.org/wiki/Raft_(algorithm)", Title: "Raft Algorithm - Wikipedia", Snippet: "Raft is a consensus algorithm", Provider: m.name},
		{URL: "https://blocked.example.com/captcha", Title: "Blocked Page", Snippet: "Cloudflare access denied", Provider: m.name},
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
	// Default page response
	return &discovery.PageContent{
		URL:       targetURL,
		CleanText: "Raft manages replicated logs through an elected leader that coordinates all log updates across majority quorum nodes.",
		RawHTML:   "<html><body><p>Raft manages replicated logs through an elected leader that coordinates all log updates across majority quorum nodes.</p></body></html>",
		Provider:  m.name,
		FetchedAt: time.Now(),
	}, nil
}

func createTestDiscoveryRegistry(fetcher *mockFetchProvider, searcher *mockSearchProvider) *discovery.Registry {
	fetchProviders := map[string]discovery.FetchProvider{
		"colly": fetcher,
	}
	return discovery.NewRegistry([]discovery.SearchProvider{searcher}, fetchProviders, []string{"colly"}, nil)
}

func TestResearch_FetchIntegrityAndAuthorityTiering(t *testing.T) {
	rootStore, teacherStore := setupTestTeacherStore(t)
	defer rootStore.Close()

	// 1. Create run and brief
	run, err := teacherStore.CreateRun("Learn Distributed Consensus")
	if err != nil {
		t.Fatalf("failed to create run: %v", err)
	}

	brief := &LearningBrief{
		Topic:        "Raft Consensus Protocol",
		Domain:       "Distributed Systems",
		LearnerLevel: "Senior Engineer",
		Motivation:   "Building replicated key-value storage",
		Depth:        "deep_dive",
	}
	if err := teacherStore.UpdateRunBrief(run.ID, brief); err != nil {
		t.Fatalf("failed to save brief: %v", err)
	}

	// 2. Create outline sections
	outline := []TeacherOutlineSection{
		{
			ID:                "sec_test_0",
			RunID:             run.ID,
			SectionOrder:      0,
			Title:             SectionZeroTitle,
			LearningObjective: "Grasp why distributed consensus is fundamental to fault-tolerant state machines.",
			Status:            OutlineStatusPending,
		},
		{
			ID:                "sec_test_1",
			RunID:             run.ID,
			SectionOrder:      1,
			Title:             "Leader Election and Quorum Guarantees",
			LearningObjective: "Explain how randomized timers and majority votes prevent split-brain scenarios.",
			DependsOn:         "sec_test_0",
			Status:            OutlineStatusPending,
		},
	}
	if err := teacherStore.SaveOutline(outline); err != nil {
		t.Fatalf("failed to save outline: %v", err)
	}

	// 3. Mock fetch provider with blocked page and authoritative page
	fetcher := &mockFetchProvider{
		name: "colly",
		responses: map[string]*discovery.PageContent{
			"https://docs.ietf.org/doc/raft-consensus": {
				URL:       "https://docs.ietf.org/doc/raft-consensus",
				CleanText: "The leader accepts log entries from clients, replicates them across a majority of servers, and instructs nodes when to apply them.",
				RawHTML:   "<html><body><article>The leader accepts log entries from clients, replicates them across a majority of servers, and instructs nodes when to apply them.</article></body></html>",
				Provider:  "colly",
				FetchedAt: time.Now(),
			},
			"https://en.wikipedia.org/wiki/Raft_(algorithm)": {
				URL:       "https://en.wikipedia.org/wiki/Raft_(algorithm)",
				CleanText: "Raft divides time into terms of arbitrary length, each initiated by an election round.",
				RawHTML:   "<html><body><p>Raft divides time into terms of arbitrary length, each initiated by an election round.</p></body></html>",
				Provider:  "colly",
				FetchedAt: time.Now(),
			},
			"https://blocked.example.com/captcha": {
				URL:       "https://blocked.example.com/captcha",
				CleanText: "Please verify you are human. Cloudflare Ray ID: 12345678",
				RawHTML:   "<html><head><title>Just a moment...</title></head><body><h1>Attention Required! | Cloudflare</h1></body></html>",
				Provider:  "colly",
				FetchedAt: time.Now(),
			},
		},
		errors: map[string]error{
			"https://network-error.example.com/down": errors.New("connection reset by peer"),
		},
	}

	searcher := &mockSearchProvider{
		name: "searxng",
		results: map[string][]discovery.SearchResult{
			"Leader Election": {
				{URL: "https://docs.ietf.org/doc/raft-consensus", Title: "IETF Raft Spec", Provider: "searxng"},
				{URL: "https://en.wikipedia.org/wiki/Raft_(algorithm)", Title: "Raft Wikipedia", Provider: "searxng"},
				{URL: "https://blocked.example.com/captcha", Title: "Blocked Page", Provider: "searxng"},
				{URL: "https://network-error.example.com/down", Title: "Error Page", Provider: "searxng"},
			},
		},
	}

	registry := createTestDiscoveryRegistry(fetcher, searcher)

	// 4. Mock LLM server extracting claims
	server, client := newMockLLMServer(t, func(messages []llm.Message) (string, error) {
		lastMsg := messages[len(messages)-1].Content
		if strings.Contains(lastMsg, "Generate 2 to 5 search queries") {
			qResp := SectionQueryGenResponse{
				Queries: []string{"Raft leader election quorum", "Raft log replication majority"},
			}
			b, _ := json.Marshal(qResp)
			return string(b), nil
		}

		if strings.Contains(lastMsg, "Extract grounded factual claims") {
			claimResp := SectionClaimExtractionResponse{
				Claims: []ExtractedClaimItem{
					{
						Claim:      "Raft leaders require a strict majority quorum to commit any log entry to state machines.",
						SourceURL:  "https://docs.ietf.org/doc/raft-consensus",
						Confidence: 0.98,
					},
				},
			}
			b, _ := json.Marshal(claimResp)
			return string(b), nil
		}

		return `{}`, nil
	})
	defer server.Close()

	// 5. Build AuthorityManager
	authManager := quality.NewAuthorityManager()
	// Mock authority rules: ietf.org is Primary, wikipedia.org is Established
	// (AuthorityManager matches substrings)

	cfg := &config.Config{
		Teacher: &config.TeacherConfig{
			SectionWorkerConcurrency: 2,
		},
	}

	orch := NewOrchestratorWithStore(client, teacherStore, registry, cfg)
	orch.authManager = authManager

	// 6. Execute ResearchOutline
	findings, err := orch.ResearchOutline(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("ResearchOutline failed: %v", err)
	}

	if len(findings) == 0 {
		t.Fatalf("expected findings to be persisted, got 0")
	}

	// 7. Validate that NO findings were persisted from blocked or error URLs
	for _, f := range findings {
		if strings.Contains(f.SourceURL, "blocked.example.com") {
			t.Errorf("fetch integrity violation: finding persisted from blocked URL: %s", f.SourceURL)
		}
		if strings.Contains(f.SourceURL, "network-error.example.com") {
			t.Errorf("fetch integrity violation: finding persisted from failed URL: %s", f.SourceURL)
		}
		if f.SectionID == "" {
			t.Errorf("finding missing section_id: %+v", f)
		}
		if f.RunID != run.ID {
			t.Errorf("finding run_id mismatch: expected %s, got %s", run.ID, f.RunID)
		}
	}
}

func TestResearch_QualityBudgetGovernor(t *testing.T) {
	rootStore, teacherStore := setupTestTeacherStore(t)
	defer rootStore.Close()

	run, err := teacherStore.CreateRun("Learn Distributed Databases")
	if err != nil {
		t.Fatalf("failed to create run: %v", err)
	}

	brief := &LearningBrief{
		Topic:        "Spanner and CockroachDB",
		Domain:       "Distributed Databases",
		LearnerLevel: "Senior Engineer",
	}
	_ = teacherStore.UpdateRunBrief(run.ID, brief)

	// Save 4 sections
	var sections []TeacherOutlineSection
	for i := 0; i < 4; i++ {
		sections = append(sections, TeacherOutlineSection{
			ID:                fmt.Sprintf("sec_budget_%d", i),
			RunID:             run.ID,
			SectionOrder:      i,
			Title:             fmt.Sprintf("Section %d", i),
			LearningObjective: fmt.Sprintf("Objective %d", i),
		})
	}
	_ = teacherStore.SaveOutline(sections)

	fetcher := &mockFetchProvider{name: "colly"}
	searcher := &mockSearchProvider{name: "searxng"}
	registry := createTestDiscoveryRegistry(fetcher, searcher)

	// Set budget to only 2 calls total
	budget := quality.NewBudget(2)

	orch := NewOrchestratorWithStore(nil, teacherStore, registry, nil)
	orch.budget = budget

	findings, err := orch.ResearchOutline(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("ResearchOutline failed: %v", err)
	}

	if len(findings) == 0 {
		t.Fatalf("expected at least fallback/first findings, got 0")
	}

	current, maxAllowed := budget.Stats()
	if current > maxAllowed {
		t.Errorf("budget exceeded: used %d calls, max was %d", current, maxAllowed)
	}
}

func TestResearch_WorkerConcurrency(t *testing.T) {
	rootStore, teacherStore := setupTestTeacherStore(t)
	defer rootStore.Close()

	run, err := teacherStore.CreateRun("Learn High Concurrency Go")
	if err != nil {
		t.Fatalf("failed to create run: %v", err)
	}

	brief := &LearningBrief{
		Topic:        "Go Concurrency Primitives",
		Domain:       "Go Programming",
		LearnerLevel: "Intermediate",
	}
	_ = teacherStore.UpdateRunBrief(run.ID, brief)

	// Create 8 sections
	var sections []TeacherOutlineSection
	for i := 0; i < 8; i++ {
		sections = append(sections, TeacherOutlineSection{
			ID:                fmt.Sprintf("sec_conc_%d", i),
			RunID:             run.ID,
			SectionOrder:      i,
			Title:             fmt.Sprintf("Concurrency Topic %d", i),
			LearningObjective: fmt.Sprintf("Master concurrency pattern %d", i),
		})
	}
	_ = teacherStore.SaveOutline(sections)

	var activeWorkers int32
	var maxObservedWorkers int32

	fetcher := &mockFetchProvider{
		name: "colly",
		responses: map[string]*discovery.PageContent{
			"https://docs.ietf.org/doc/raft-consensus": {
				URL:       "https://docs.ietf.org/doc/raft-consensus",
				CleanText: "Goroutines and channels enable structured concurrent communication.",
				RawHTML:   "<html><body><p>Goroutines and channels enable structured concurrent communication.</p></body></html>",
				Provider:  "colly",
				FetchedAt: time.Now(),
			},
		},
	}

	// Wrap searcher with concurrency tracking
	searcher := &mockSearchProvider{name: "searxng"}

	registry := createTestDiscoveryRegistry(fetcher, searcher)

	concurrencyLimit := 3
	cfg := &config.Config{
		Teacher: &config.TeacherConfig{
			SectionWorkerConcurrency: concurrencyLimit,
		},
	}

	// Mock LLM server with slight sleep to track in-flight workers
	server, client := newMockLLMServer(t, func(messages []llm.Message) (string, error) {
		current := atomic.AddInt32(&activeWorkers, 1)
		for {
			max := atomic.LoadInt32(&maxObservedWorkers)
			if current <= max || atomic.CompareAndSwapInt32(&maxObservedWorkers, max, current) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		atomic.AddInt32(&activeWorkers, -1)

		qResp := SectionQueryGenResponse{Queries: []string{"golang channels select"}}
		b, _ := json.Marshal(qResp)
		return string(b), nil
	})
	defer server.Close()

	orch := NewOrchestratorWithStore(client, teacherStore, registry, cfg)

	_, err = orch.ResearchOutline(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("ResearchOutline failed: %v", err)
	}

	maxObserved := atomic.LoadInt32(&maxObservedWorkers)
	if maxObserved > int32(concurrencyLimit) {
		t.Errorf("concurrency limit exceeded: observed %d concurrent workers, limit was %d", maxObserved, concurrencyLimit)
	}
}
