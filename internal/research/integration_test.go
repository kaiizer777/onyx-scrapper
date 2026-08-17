package research_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/kaiizer777/onyx-scrapper/internal/config"
	"github.com/kaiizer777/onyx-scrapper/internal/discovery"
	"github.com/kaiizer777/onyx-scrapper/internal/llm"
	"github.com/kaiizer777/onyx-scrapper/internal/quality"
	"github.com/kaiizer777/onyx-scrapper/internal/research"
	"github.com/kaiizer777/onyx-scrapper/internal/store"
)

type mockSearchProvider struct {
	results []discovery.SearchResult
}

func (m *mockSearchProvider) Name() string {
	return "mock-search"
}

func (m *mockSearchProvider) Search(ctx context.Context, query string, limit int) ([]discovery.SearchResult, error) {
	return m.results, nil
}

type mockFetchProvider struct {
	pages map[string]string
}

func (m *mockFetchProvider) Name() string {
	return "mock-fetch"
}

func (m *mockFetchProvider) Fetch(ctx context.Context, targetURL string, opts discovery.FetchOptions) (*discovery.PageContent, error) {
	text := "PostgreSQL 16 was officially released featuring notable enhancements to query execution parallelism, improved developer experience with bidirectional logical replication, and streamlined monitoring features. This comprehensive release allows database administrators to optimize large-scale workloads and complex analytical queries across modern enterprise architectures efficiently."
	if custom, ok := m.pages[targetURL]; ok {
		text = custom
	}
	return &discovery.PageContent{
		URL:       targetURL,
		CleanText: text,
		RawHTML:   fmt.Sprintf("<html><body><p>%s</p></body></html>", text),
		Provider:  "mock-fetch",
		FetchedAt: time.Now(),
	}, nil
}

func TestIntegration_DeepResearchEndToEnd_ContradictedClaimExcluded(t *testing.T) {
	var synthesisPromptCaptured string
	var extractionCallCount int32
	var verifyCallCount int32
	var synthesisCallCount int32

	mockLLMServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

		if strings.Contains(lastMsg, "Extract factual claims") {
			atomic.AddInt32(&extractionCallCount, 1)
			if strings.Contains(lastMsg, "postgres.org") {
				respContent = `{
					"claims": [
						{
							"claim": "PostgreSQL 16 was released with enhanced query execution parallelism.",
							"source_url": "https://postgres.org/news/pg16",
							"confidence": 0.95
						}
					]
				}`
			} else {
				respContent = `{
					"claims": [
						{
							"claim": "Tim Cook stepped down and is no longer CEO of Apple in 2026.",
							"source_url": "https://apple.com/leadership",
							"confidence": 0.85
						}
					]
				}`
			}
		} else if strings.Contains(lastMsg, "Does this second, independent source confirm, contradict") {
			atomic.AddInt32(&verifyCallCount, 1)
			if strings.Contains(lastMsg, "Tim Cook stepped down") {
				respContent = "RESULT: [CONTRADICTED]\nVALUE: Tim Cook remains the active CEO of Apple Inc."
			} else {
				respContent = "RESULT: [CONFIRMED]\nVALUE: []"
			}
		} else if strings.Contains(lastMsg, "lead synthesizer for a research report") {
			atomic.AddInt32(&synthesisCallCount, 1)
			synthesisPromptCaptured = lastMsg
			respContent = "# Technology & Leadership Report\n\nPostgreSQL 16 was released with enhanced parallelism [1](https://postgres.org/news/pg16)."
		} else {
			respContent = "OK"
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

	provCfg := config.ProviderConfig{
		BaseURL: mockLLMServer.URL,
		Model:   "mock-model",
		APIKey:  "test-key",
	}
	llmClient := llm.NewClient(provCfg)

	dbPath := filepath.Join(t.TempDir(), "deep_research_integration.db")
	st, err := store.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	defer st.Close()

	searcher := &mockSearchProvider{
		results: []discovery.SearchResult{
			{URL: "https://postgres.org/news/pg16", Title: "Postgres 16", Snippet: "PostgreSQL 16 release notes", Provider: "mock-search"},
			{URL: "https://apple.com/leadership", Title: "Apple Leadership", Snippet: "Tim Cook is CEO", Provider: "mock-search"},
		},
	}
	fetcher := &mockFetchProvider{
		pages: map[string]string{
			"https://postgres.org/news/pg16": "PostgreSQL 16 was officially released featuring notable enhancements to query execution parallelism, improved developer experience with bidirectional logical replication, and streamlined monitoring features. This comprehensive release allows database administrators to optimize large-scale workloads and complex analytical queries across modern enterprise architectures efficiently.",
			"https://apple.com/leadership":   "Tim Cook serves as the Chief Executive Officer of Apple Inc. and sits on its board of directors. Prior to being named CEO in August 2011, Tim was Apple's Chief Operating Officer and was responsible for all of the company's worldwide sales and operations, including end-to-end management of Apple's supply chain.",
		},
	}
	registry := discovery.NewRegistry(
		[]discovery.SearchProvider{searcher},
		map[string]discovery.FetchProvider{"mock-fetch": fetcher},
		[]string{"mock-fetch"},
		nil,
	)

	budget := quality.NewBudget(10)
	worker := research.NewWorker(llmClient, st, registry, true, budget, 24)

	runID, err := st.CreateResearchRun("Investigate Tech Releases and Leadership")
	if err != nil {
		t.Fatalf("CreateResearchRun failed: %v", err)
	}
	sqID, err := st.CreateSubQuestion(runID, "What are the latest Postgres and Apple leadership updates?")
	if err != nil {
		t.Fatalf("CreateSubQuestion failed: %v", err)
	}

	// 1. Execute SubResearch
	err = worker.RunSubResearch(context.Background(), runID, sqID, "What are the latest Postgres and Apple leadership updates?")
	if err != nil {
		t.Fatalf("RunSubResearch failed: %v", err)
	}

	// 2. Inspect SQLite persisted findings
	findings, err := st.GetFindingsForSubQuestion(sqID)
	if err != nil {
		t.Fatalf("GetFindingsForSubQuestion failed: %v", err)
	}
	if len(findings) != 2 {
		t.Fatalf("expected 2 findings persisted in store, got %d", len(findings))
	}

	var activeFinding, contradictedFinding *store.Finding
	for i := range findings {
		f := &findings[i]
		if f.Status == store.StatusActive {
			activeFinding = f
		} else if f.Status == store.StatusContradicted {
			contradictedFinding = f
		}
	}

	if activeFinding == nil {
		t.Fatalf("expected active finding for Postgres, none found")
	}
	if contradictedFinding == nil {
		t.Fatalf("expected contradicted finding for Apple CEO, none found")
	}

	// Contradicted claim must NOT have its claim text mutated with strings like "Note: a second source..."
	if strings.Contains(contradictedFinding.Claim, "Note:") {
		t.Errorf("contradicted finding claim text was unexpectedly mutated: %q", contradictedFinding.Claim)
	}
	if !strings.Contains(contradictedFinding.VerificationNote, "Tim Cook remains") {
		t.Errorf("expected structured verification note, got %q", contradictedFinding.VerificationNote)
	}

	// 3. Synthesize Report
	authMgr := quality.NewAuthorityManager()
	synth := research.NewSynthesizer(llmClient, authMgr)

	plan := research.ResearchPlan{
		Goal:          "Investigate Tech Releases and Leadership",
		ReportOutline: []string{"Executive Summary", "Database Releases", "Leadership Updates"},
	}

	reportMD, err := synth.Synthesize(context.Background(), plan, findings)
	if err != nil {
		t.Fatalf("Synthesize failed: %v", err)
	}

	// 4. Assert synthesis prompt excluded contradicted finding
	if strings.Contains(synthesisPromptCaptured, "Tim Cook stepped down") {
		t.Errorf("synthesis prompt unexpectedly contains contradicted claim text: %s", synthesisPromptCaptured)
	}
	if !strings.Contains(synthesisPromptCaptured, "PostgreSQL 16 was released") {
		t.Errorf("synthesis prompt missing active claim text: %s", synthesisPromptCaptured)
	}

	// 5. Assert report surfaces excluded section but excludes from narrative
	if !strings.Contains(reportMD, "### Excluded Findings (Verification & Fact Check)") {
		t.Errorf("report markdown missing Excluded Findings section: %s", reportMD)
	}
	if !strings.Contains(reportMD, "Tim Cook stepped down and is no longer CEO of Apple in 2026.") {
		t.Errorf("report markdown excluded section missing contradicted claim detail: %s", reportMD)
	}
}

func TestIntegration_FreshDatabaseMigration(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "fresh_database.db")

	st, err := store.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore failed on fresh database: %v", err)
	}
	defer st.Close()

	// Test findings table with new columns
	runID, err := st.CreateResearchRun("Fresh DB Test")
	if err != nil {
		t.Fatalf("CreateResearchRun failed: %v", err)
	}
	sqID, err := st.CreateSubQuestion(runID, "Sub question 1")
	if err != nil {
		t.Fatalf("CreateSubQuestion failed: %v", err)
	}

	findingID, err := st.InsertFinding(store.Finding{
		SubQuestionID:    sqID,
		Claim:            "Go 1.25 introduces new runtime features",
		SourceURL:        "https://go.dev/blog",
		SourceProvider:   "searxng",
		Confidence:       0.99,
		Status:           store.StatusContradicted,
		VerificationNote: "Contradicted by release notes",
		AuthorityTier:    int(quality.TierPrimary),
	})
	if err != nil {
		t.Fatalf("InsertFinding failed: %v", err)
	}
	if findingID <= 0 {
		t.Fatalf("expected positive finding ID, got %d", findingID)
	}

	findings, err := st.GetFindingsForSubQuestion(sqID)
	if err != nil {
		t.Fatalf("GetFindingsForSubQuestion failed: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}
	f := findings[0]
	if f.Status != store.StatusContradicted {
		t.Errorf("expected StatusContradicted, got %v", f.Status)
	}
	if f.VerificationNote != "Contradicted by release notes" {
		t.Errorf("expected verification note 'Contradicted by release notes', got %q", f.VerificationNote)
	}
	if f.AuthorityTier != int(quality.TierPrimary) {
		t.Errorf("expected AuthorityTier %d, got %d", quality.TierPrimary, f.AuthorityTier)
	}

	// Test entity_cache typed columns
	err = st.SaveEntityCache("Apple", "exec", "exec:apple", string(quality.ResultConfirmed), "Tim Cook")
	if err != nil {
		t.Fatalf("SaveEntityCache failed: %v", err)
	}
	res, val, ok := st.GetEntityCache("Apple", "exec", "exec:apple", 24)
	if !ok {
		t.Fatalf("GetEntityCache failed to find cached entry")
	}
	if res != string(quality.ResultConfirmed) || val != "Tim Cook" {
		t.Errorf("cached entry mismatch: res=%q, val=%q", res, val)
	}

	// Test agent_runs grounding flag
	agentRunID, err := st.CreateAgentRun("Agent Grounding Test")
	if err != nil {
		t.Fatalf("CreateAgentRun failed: %v", err)
	}
	run, err := st.GetAgentRun(agentRunID)
	if err != nil {
		t.Fatalf("GetAgentRun failed: %v", err)
	}
	if run.IsGrounded {
		t.Errorf("expected initial IsGrounded to be false")
	}

	err = st.MarkAgentRunGrounded(agentRunID)
	if err != nil {
		t.Fatalf("MarkAgentRunGrounded failed: %v", err)
	}

	runAfter, err := st.GetAgentRun(agentRunID)
	if err != nil {
		t.Fatalf("GetAgentRun after grounding failed: %v", err)
	}
	if !runAfter.IsGrounded {
		t.Errorf("expected IsGrounded to be true after MarkAgentRunGrounded")
	}
}

func TestIntegration_ExistingDatabaseMigrationIdempotent(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "existing_legacy.db")

	// 1. Manually create legacy schema (simulating older pre-migration database)
	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to open raw sqlite: %v", err)
	}

	legacySchema := `
		CREATE TABLE pages (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			url TEXT UNIQUE,
			fetched_at DATETIME,
			raw_html TEXT,
			clean_text TEXT
		);
		CREATE TABLE research_runs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			goal TEXT,
			status TEXT,
			started_at DATETIME,
			completed_at DATETIME,
			report_md TEXT
		);
		CREATE TABLE research_subquestions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			run_id INTEGER,
			question TEXT,
			status TEXT
		);
		CREATE TABLE findings (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			subquestion_id INTEGER,
			claim TEXT,
			source_url TEXT,
			confidence REAL,
			created_at DATETIME
		);
		CREATE TABLE entity_cache (
			entity TEXT,
			version_token TEXT,
			result TEXT,
			value TEXT,
			created_at DATETIME,
			UNIQUE(entity, version_token)
		);
		CREATE TABLE agent_runs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			goal TEXT,
			status TEXT,
			result TEXT,
			created_at DATETIME,
			updated_at DATETIME
		);
	`
	if _, err := rawDB.Exec(legacySchema); err != nil {
		_ = rawDB.Close()
		t.Fatalf("failed to create legacy schema: %v", err)
	}

	// Insert legacy pre-existing data
	now := time.Now().UTC()
	_, err = rawDB.Exec(`INSERT INTO research_runs (id, goal, status, started_at, report_md) VALUES (1, 'Legacy Goal', 'completed', ?, 'Legacy Report');`, now)
	if err != nil {
		_ = rawDB.Close()
		t.Fatalf("failed to insert legacy run: %v", err)
	}
	_, err = rawDB.Exec(`INSERT INTO research_subquestions (id, run_id, question, status) VALUES (1, 1, 'Legacy Question', 'done');`)
	if err != nil {
		_ = rawDB.Close()
		t.Fatalf("failed to insert legacy subquestion: %v", err)
	}
	_, err = rawDB.Exec(`INSERT INTO findings (id, subquestion_id, claim, source_url, confidence, created_at) VALUES (1, 1, 'Legacy Claim text', 'https://legacy.example.com', 0.9, ?);`, now)
	if err != nil {
		_ = rawDB.Close()
		t.Fatalf("failed to insert legacy finding: %v", err)
	}
	_, err = rawDB.Exec(`INSERT INTO entity_cache (entity, version_token, result, value, created_at) VALUES ('OldEntity', 'v1.0', 'CONFIRMED', 'v1.0.1', ?);`, now)
	if err != nil {
		_ = rawDB.Close()
		t.Fatalf("failed to insert legacy cache: %v", err)
	}
	_, err = rawDB.Exec(`INSERT INTO agent_runs (id, goal, status, result, created_at, updated_at) VALUES (1, 'Legacy Agent Goal', 'completed', 'Result', ?, ?);`, now, now)
	if err != nil {
		_ = rawDB.Close()
		t.Fatalf("failed to insert legacy agent run: %v", err)
	}
	_ = rawDB.Close()

	// 2. Open with NewStore to trigger auto-migrations
	st, err := store.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore failed on legacy DB migration: %v", err)
	}

	// Verify legacy data is preserved with valid default values
	findings, err := st.GetFindingsForSubQuestion(1)
	if err != nil {
		_ = st.Close()
		t.Fatalf("GetFindingsForSubQuestion failed: %v", err)
	}
	if len(findings) != 1 {
		_ = st.Close()
		t.Fatalf("expected 1 legacy finding, got %d", len(findings))
	}
	if findings[0].Claim != "Legacy Claim text" {
		t.Errorf("legacy claim corrupted: %q", findings[0].Claim)
	}
	if findings[0].Status != store.StatusActive {
		t.Errorf("expected default StatusActive for migrated legacy row, got %v", findings[0].Status)
	}
	if findings[0].VerificationNote != "" {
		t.Errorf("expected empty verification note default, got %q", findings[0].VerificationNote)
	}

	agentRun, err := st.GetAgentRun(1)
	if err != nil {
		_ = st.Close()
		t.Fatalf("GetAgentRun failed: %v", err)
	}
	if agentRun.IsGrounded {
		t.Errorf("expected is_grounded to default to false for legacy agent run")
	}

	// Test inserting new records using migrated columns
	newFindingID, err := st.InsertFinding(store.Finding{
		SubQuestionID:    1,
		Claim:            "New post-migration finding",
		SourceURL:        "https://new.example.com",
		Confidence:       0.95,
		Status:           store.StatusContradicted,
		VerificationNote: "Contradicted note",
		AuthorityTier:    int(quality.TierEstablished),
	})
	if err != nil {
		_ = st.Close()
		t.Fatalf("InsertFinding on migrated DB failed: %v", err)
	}
	if newFindingID <= 0 {
		t.Errorf("invalid new finding ID: %d", newFindingID)
	}

	if err := st.Close(); err != nil {
		t.Fatalf("failed to close store: %v", err)
	}

	// 3. Re-open NewStore again to verify migration idempotency (no crash on re-run)
	st2, err := store.NewStore(dbPath)
	if err != nil {
		t.Fatalf("re-opening NewStore on migrated DB failed: %v", err)
	}

	findings2, err := st2.GetFindingsForSubQuestion(1)
	if err != nil {
		_ = st2.Close()
		t.Fatalf("GetFindingsForSubQuestion on re-opened DB failed: %v", err)
	}
	if len(findings2) != 2 {
		t.Errorf("expected 2 findings after re-open, got %d", len(findings2))
	}
	_ = st2.Close()
}
