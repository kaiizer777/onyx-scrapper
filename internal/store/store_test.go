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

	pageID, err := st.SavePage(url, rawHTML, cleanText, "test-source")
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
	updatedPageID, err := st.SavePage(url, rawHTML, updatedText, "test-source")
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

