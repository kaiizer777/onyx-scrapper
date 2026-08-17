package store

import (
	"path/filepath"
	"testing"
)

func TestStore(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_onyx.db")

	st, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	defer st.Close()

	// 1. Save Page
	url := "https://golang.org"
	rawHTML := "<html><body><h1>Go Programming Language</h1><p>Go is an open source programming language.</p></body></html>"
	cleanText := "Go Programming Language\nGo is an open source programming language."

	pageID, err := st.SavePage(url, rawHTML, cleanText, "test-source", "ok")
	if err != nil {
		t.Fatalf("SavePage failed: %v", err)
	}
	if pageID <= 0 {
		t.Fatalf("expected valid pageID, got %d", pageID)
	}

	// 2. Get Page By URL
	page, err := st.GetPageByURL(url)
	if err != nil {
		t.Fatalf("GetPageByURL failed: %v", err)
	}
	if page == nil || page.CleanText != cleanText {
		t.Fatalf("unexpected page content: %+v", page)
	}

	// 3. Save Extraction
	extID, err := st.SaveExtraction(pageID, "article", `{"title": "Go"}`)
	if err != nil {
		t.Fatalf("SaveExtraction failed: %v", err)
	}
	if extID <= 0 {
		t.Fatalf("expected valid extraction ID, got %d", extID)
	}

	// 4. Upsert Page (Update)
	updatedText := "Go Programming Language\nUpdated text for search index."
	updatedPageID, err := st.SavePage(url, rawHTML, updatedText, "test-source", "ok")
	if err != nil {
		t.Fatalf("SavePage upsert failed: %v", err)
	}
	if updatedPageID != pageID {
		t.Fatalf("expected same pageID on upsert, got %d vs %d", updatedPageID, pageID)
	}

	// 5. Search via FTS5
	results, err := st.SearchPages("search index")
	if err != nil {
		t.Fatalf("SearchPages failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 search result, got %d", len(results))
	}
	if results[0].URL != url {
		t.Fatalf("expected match URL %s, got %s", url, results[0].URL)
	}
}

func TestAgentStore(t *testing.T) {
	st, err := NewStore(":memory:")
	if err != nil {
		t.Fatalf("NewStore in memory failed: %v", err)
	}
	defer st.Close()

	goal := "Search news and extract head item"
	runID, err := st.CreateAgentRun(goal)
	if err != nil {
		t.Fatalf("CreateAgentRun failed: %v", err)
	}
	if runID <= 0 {
		t.Fatalf("expected valid runID, got %d", runID)
	}

	stepID, err := st.SaveAgentStep(runID, 1, "navigate", `{"url": "https://example.com"}`, "Navigated successfully", "")
	if err != nil {
		t.Fatalf("SaveAgentStep failed: %v", err)
	}
	if stepID <= 0 {
		t.Fatalf("expected valid stepID, got %d", stepID)
	}

	err = st.UpdateAgentRunStatus(runID, "completed", "Top story found")
	if err != nil {
		t.Fatalf("UpdateAgentRunStatus failed: %v", err)
	}

	run, err := st.GetAgentRun(runID)
	if err != nil {
		t.Fatalf("GetAgentRun failed: %v", err)
	}
	if run == nil || run.Status != "completed" || run.Result != "Top story found" {
		t.Fatalf("unexpected agent run state: %+v", run)
	}

	steps, err := st.GetAgentSteps(runID)
	if err != nil {
		t.Fatalf("GetAgentSteps failed: %v", err)
	}
	if len(steps) != 1 || steps[0].Action != "navigate" {
		t.Fatalf("unexpected agent steps: %+v", steps)
	}
}

func TestEntityCache_TypedStorageAndTTL(t *testing.T) {
	st, err := NewStore(":memory:")
	if err != nil {
		t.Fatalf("NewStore in memory failed: %v", err)
	}
	defer st.Close()

	// 1. Save and retrieve typed cache entry
	err = st.SaveEntityCache("Apple", "exec", "exec:apple", "CONFIRMED", "Tim Cook")
	if err != nil {
		t.Fatalf("SaveEntityCache failed: %v", err)
	}

	res, val, ok := st.GetEntityCache("Apple", "exec", "exec:apple", 24)
	if !ok || res != "CONFIRMED" || val != "Tim Cook" {
		t.Fatalf("expected (CONFIRMED, Tim Cook, true), got (%s, %s, %v)", res, val, ok)
	}

	// 2. Different entity type with same entity name does not collide
	res2, val2, ok2 := st.GetEntityCache("Apple", "price", "price:apple:2026-08-17", 24)
	if ok2 {
		t.Fatalf("expected miss for different entity_type, got (%s, %s, %v)", res2, val2, ok2)
	}

	// 3. Upsert on same entity + entity_type + token updates result and value
	err = st.SaveEntityCache("Apple", "exec", "exec:apple", "CONTRADICTED", "New CEO")
	if err != nil {
		t.Fatalf("SaveEntityCache upsert failed: %v", err)
	}
	res3, val3, ok3 := st.GetEntityCache("Apple", "exec", "exec:apple", 24)
	if !ok3 || res3 != "CONTRADICTED" || val3 != "New CEO" {
		t.Fatalf("expected updated (CONTRADICTED, New CEO, true), got (%s, %s, %v)", res3, val3, ok3)
	}
}

func TestStoreMigration_FindingsStatusColumn_IdempotentOnExistingDB(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "migration_test.db")

	// 1. Initialize store (schema applied + migrations run)
	st, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	runID, err := st.CreateResearchRun("Test Migration Goal")
	if err != nil {
		t.Fatalf("CreateResearchRun failed: %v", err)
	}
	sqID, err := st.CreateSubQuestion(runID, "Test SQ")
	if err != nil {
		t.Fatalf("CreateSubQuestion failed: %v", err)
	}

	// 2. Insert active finding via SaveFinding (legacy 5-param helper)
	fID1, err := st.SaveFinding(sqID, "Active Claim", "https://example.com/1", "searxng", 0.9)
	if err != nil {
		t.Fatalf("SaveFinding failed: %v", err)
	}
	if fID1 <= 0 {
		t.Fatalf("expected valid finding ID, got %d", fID1)
	}

	// 3. Insert structured finding via InsertFinding with StatusContradicted
	fID2, err := st.InsertFinding(Finding{
		SubQuestionID:    sqID,
		Claim:            "Contradicted Claim",
		SourceURL:        "https://example.com/2",
		SourceProvider:   "searxng",
		Confidence:       0.8,
		Status:           StatusContradicted,
		VerificationNote: "Contradicted by authoritative source",
	})
	if err != nil {
		t.Fatalf("InsertFinding failed: %v", err)
	}
	if fID2 <= 0 {
		t.Fatalf("expected valid finding ID, got %d", fID2)
	}

	// 4. Insert structured finding with StatusUnclear
	_, err = st.InsertFinding(Finding{
		SubQuestionID:    sqID,
		Claim:            "Unclear Claim",
		SourceURL:        "https://example.com/3",
		SourceProvider:   "searxng",
		Confidence:       0.7,
		Status:           StatusUnclear,
		VerificationNote: "Second source inconclusive",
	})
	if err != nil {
		t.Fatalf("InsertFinding with StatusUnclear failed: %v", err)
	}

	// Close store connection
	if err := st.Close(); err != nil {
		t.Fatalf("st.Close failed: %v", err)
	}

	// 5. Simulate restart: Re-open the database with NewStore (idempotent migration)
	st2, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore re-open failed: %v", err)
	}
	defer st2.Close()

	// 6. Verify findings retained statuses and notes accurately without resetting to defaults
	findings, err := st2.GetFindingsForSubQuestion(sqID)
	if err != nil {
		t.Fatalf("GetFindingsForSubQuestion failed: %v", err)
	}
	if len(findings) != 3 {
		t.Fatalf("expected 3 findings, got %d", len(findings))
	}

	// Check finding 1
	if findings[0].Status != StatusActive {
		t.Errorf("finding 0: expected status 'active', got %q", findings[0].Status)
	}
	if findings[0].VerificationNote != "" {
		t.Errorf("finding 0: expected empty verification note, got %q", findings[0].VerificationNote)
	}

	// Check finding 2 (Contradicted)
	if findings[1].Status != StatusContradicted {
		t.Errorf("finding 1: expected status 'contradicted', got %q", findings[1].Status)
	}
	if findings[1].VerificationNote != "Contradicted by authoritative source" {
		t.Errorf("finding 1: expected note 'Contradicted by authoritative source', got %q", findings[1].VerificationNote)
	}
	if findings[1].Claim != "Contradicted Claim" {
		t.Errorf("finding 1: expected unmodified claim 'Contradicted Claim', got %q", findings[1].Claim)
	}

	// Check finding 3 (Unclear)
	if findings[2].Status != StatusUnclear {
		t.Errorf("finding 2: expected status 'unclear', got %q", findings[2].Status)
	}
	if findings[2].VerificationNote != "Second source inconclusive" {
		t.Errorf("finding 2: expected note 'Second source inconclusive', got %q", findings[2].VerificationNote)
	}

	// 7. Verify GetAllFindingsForRun also correctly scans all fields
	runFindings, err := st2.GetAllFindingsForRun(runID)
	if err != nil {
		t.Fatalf("GetAllFindingsForRun failed: %v", err)
	}
	if len(runFindings) != 3 {
		t.Fatalf("expected 3 run findings, got %d", len(runFindings))
	}
	if runFindings[1].Status != StatusContradicted || runFindings[1].VerificationNote != "Contradicted by authoritative source" {
		t.Errorf("run findings 1: unexpected status/note: %+v", runFindings[1])
	}
}



