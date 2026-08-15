package teacher

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/kaiizer777/onyx-scrapper/internal/store"
)

func TestTeacherStoreEndToEnd(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_teacher.db")

	rootStore, err := store.NewStore(dbPath)
	if err != nil {
		t.Fatalf("failed to initialize root store: %v", err)
	}
	defer rootStore.Close()

	ts := NewStoreFromAppStore(rootStore)
	if ts == nil {
		t.Fatalf("expected non-nil teacher store")
	}

	// 1. Create Teacher Run
	goal := "I want to understand transformer attention mechanisms"
	run, err := ts.CreateRun(goal)
	if err != nil {
		t.Fatalf("CreateRun failed: %v", err)
	}
	if run == nil || run.ID == "" || run.Status != RunStatusClarifying {
		t.Fatalf("unexpected run created: %+v", run)
	}

	// 2. Add Clarification Round
	clarification := &ClarificationRound{
		RunID: run.ID,
		Round: 1,
		Question: ClarificationQuestion{
			Text:      "What is your background level?",
			InputKind: InputKindSingleSelect,
			Options:   []string{"Beginner", "Intermediate", "Advanced"},
		},
		Answer:    "Intermediate",
		CreatedAt: time.Now().UTC(),
	}
	if err := ts.SaveClarification(clarification); err != nil {
		t.Fatalf("SaveClarification failed: %v", err)
	}

	rounds, err := ts.GetClarifications(run.ID)
	if err != nil {
		t.Fatalf("GetClarifications failed: %v", err)
	}
	if len(rounds) != 1 || rounds[0].Answer != "Intermediate" || len(rounds[0].Question.Options) != 3 {
		t.Fatalf("unexpected clarifications: %+v", rounds)
	}

	// 3. Update Brief
	wantsCode := true
	brief := &LearningBrief{
		Topic:                "Transformer Attention Mechanisms",
		Domain:               "Machine Learning",
		LearnerLevel:         "Intermediate",
		Motivation:           "Research understanding",
		Depth:                "working_understanding",
		KnownReferencePoints: []string{"Vectors", "Matrix Multiplication", "RNNs"},
		ExplicitScopeIn:      []string{"Scaled Dot-Product", "Multi-Head Attention"},
		ExplicitScopeOut:     []string{"Positional Encodings detail"},
		FormatPreferences: FormatPreferences{
			Length:            "medium",
			WantsCodeExamples: &wantsCode,
			WantsDiagrams:     true,
		},
		AssumptionsToAvoid: []string{"Assume PyTorch mastery"},
	}
	if err := brief.Validate(); err != nil {
		t.Fatalf("LearningBrief validation failed: %v", err)
	}

	if err := ts.UpdateRunBrief(run.ID, brief); err != nil {
		t.Fatalf("UpdateRunBrief failed: %v", err)
	}

	loadedRun, err := ts.GetRun(run.ID)
	if err != nil {
		t.Fatalf("GetRun failed: %v", err)
	}
	if loadedRun.Status != RunStatusBriefReady || loadedRun.LearningBrief == nil || loadedRun.LearningBrief.Topic != "Transformer Attention Mechanisms" {
		t.Fatalf("unexpected loaded run brief state: %+v", loadedRun)
	}

	// 4. Save Outline
	sections := []TeacherOutlineSection{
		{
			ID:                "sec_0",
			RunID:             run.ID,
			SectionOrder:      0,
			Title:             "Core Intuition",
			LearningObjective: "Understand why attention is needed over RNNs in one paragraph.",
			Status:            OutlineStatusPending,
		},
		{
			ID:                "sec_1",
			RunID:             run.ID,
			SectionOrder:      1,
			Title:             "Scaled Dot-Product Attention",
			LearningObjective: "Explain Q, K, V mathematical interaction and softmax scaling.",
			DependsOn:         "sec_0",
			Status:            OutlineStatusPending,
		},
	}
	if err := ts.SaveOutline(sections); err != nil {
		t.Fatalf("SaveOutline failed: %v", err)
	}

	outline, err := ts.GetOutline(run.ID)
	if err != nil {
		t.Fatalf("GetOutline failed: %v", err)
	}
	if len(outline) != 2 || outline[1].Title != "Scaled Dot-Product Attention" {
		t.Fatalf("unexpected outline: %+v", outline)
	}

	// 5. Save Findings
	finding := &TeacherFinding{
		RunID:          run.ID,
		SectionID:      "sec_1",
		Claim:          "Softmax scaling by sqrt(d_k) prevents vanishing gradients in dot product attention.",
		SourceURL:      "https://arxiv.org/abs/1706.03762",
		SourceProvider: "searxng",
		AuthorityTier:  "Primary",
		Confidence:     0.95,
	}
	if err := ts.SaveFinding(finding); err != nil {
		t.Fatalf("SaveFinding failed: %v", err)
	}

	secFindings, err := ts.GetFindingsForSection("sec_1")
	if err != nil {
		t.Fatalf("GetFindingsForSection failed: %v", err)
	}
	if len(secFindings) != 1 || secFindings[0].AuthorityTier != "Primary" {
		t.Fatalf("unexpected section findings: %+v", secFindings)
	}

	// 6. Section Draft & Critique
	teacherSec := &TeacherSection{
		ID:        "sec_1",
		RunID:     run.ID,
		OutlineID: "sec_1",
		DraftMD:   "Initial draft of Scaled Dot-Product Attention...",
	}
	if err := ts.SaveSectionDraft(teacherSec); err != nil {
		t.Fatalf("SaveSectionDraft failed: %v", err)
	}

	critiqueNotes := []CritiqueNote{
		{
			Issue:      "Analogy needs to reference Matrix Multiplication more explicitly.",
			Severity:   "minor",
			Suggestion: "Anchor Q and K to row and column lookups.",
		},
	}
	finalMD := "Approved final Markdown for Scaled Dot-Product Attention."
	if err := ts.UpdateSectionCritique(teacherSec.ID, critiqueNotes, finalMD, 1); err != nil {
		t.Fatalf("UpdateSectionCritique failed: %v", err)
	}

	loadedSec, err := ts.GetSection(teacherSec.ID)
	if err != nil {
		t.Fatalf("GetSection failed: %v", err)
	}
	if loadedSec == nil || loadedSec.FinalMD != finalMD || loadedSec.RevisionCount != 1 || len(loadedSec.CritiqueNotes) != 1 {
		t.Fatalf("unexpected loaded section: %+v", loadedSec)
	}

	// 7. Update Report & FTS Search
	finalReport := "# Understanding Transformer Attention\n\nFull deep dive report..."
	if err := ts.UpdateRunReport(run.ID, finalReport); err != nil {
		t.Fatalf("UpdateRunReport failed: %v", err)
	}

	if err := ts.IndexReportFTS(run.ID, "Scaled Dot-Product Attention", finalMD); err != nil {
		t.Fatalf("IndexReportFTS failed: %v", err)
	}

	searchResults, err := ts.SearchFTS("Scaled Dot-Product", 10)
	if err != nil {
		t.Fatalf("SearchFTS failed: %v", err)
	}
	if len(searchResults) != 1 || searchResults[0].RunID != run.ID {
		t.Fatalf("unexpected search results: %+v", searchResults)
	}

	// 8. List Runs
	runs, err := ts.ListRuns(10)
	if err != nil {
		t.Fatalf("ListRuns failed: %v", err)
	}
	if len(runs) != 1 || runs[0].Status != RunStatusDone {
		t.Fatalf("unexpected list runs result: %+v", runs)
	}
}

func TestTeacherStore_RegenerationResetAndClearFTS(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_teacher_regen.db")

	rootStore, err := store.NewStore(dbPath)
	if err != nil {
		t.Fatalf("failed to initialize root store: %v", err)
	}
	defer rootStore.Close()

	ts := NewStoreFromAppStore(rootStore)

	run, err := ts.CreateRun("Learn Distributed Consensus")
	if err != nil {
		t.Fatalf("CreateRun failed: %v", err)
	}

	outlineSec := TeacherOutlineSection{
		ID:                "outline_0",
		RunID:             run.ID,
		SectionOrder:      0,
		Title:             "Consensus Intro",
		LearningObjective: "Intro to Paxos and Raft",
		Status:            OutlineStatusPending,
	}
	if err := ts.SaveOutline([]TeacherOutlineSection{outlineSec}); err != nil {
		t.Fatalf("SaveOutline failed: %v", err)
	}

	sec := &TeacherSection{
		ID:        "sec_regen",
		RunID:     run.ID,
		OutlineID: "outline_0",
		DraftMD:   "Draft 1",
	}
	if err := ts.SaveSectionDraft(sec); err != nil {
		t.Fatalf("initial SaveSectionDraft failed: %v", err)
	}

	// Update critique to simulate revision 2
	notes := []CritiqueNote{{Issue: "Needs work", Severity: "major"}}
	if err := ts.UpdateSectionCritique(sec.ID, notes, "Final MD 1", 2); err != nil {
		t.Fatalf("UpdateSectionCritique failed: %v", err)
	}

	loaded, _ := ts.GetSection(sec.ID)
	if loaded.RevisionCount != 2 || loaded.FinalMD != "Final MD 1" {
		t.Fatalf("expected revision_count=2, got %d", loaded.RevisionCount)
	}

	// Now simulate regeneration: SaveSectionDraft on the same ID
	sec.DraftMD = "New regenerated draft 2"
	sec.FinalMD = ""
	sec.RevisionCount = 0
	if err := ts.SaveSectionDraft(sec); err != nil {
		t.Fatalf("regenerated SaveSectionDraft failed: %v", err)
	}

	regenLoaded, err := ts.GetSection(sec.ID)
	if err != nil {
		t.Fatalf("GetSection after regen failed: %v", err)
	}
	if regenLoaded.DraftMD != "New regenerated draft 2" {
		t.Fatalf("expected updated draft_md, got %q", regenLoaded.DraftMD)
	}
	if regenLoaded.RevisionCount != 0 {
		t.Fatalf("expected revision_count to be reset to 0, got %d", regenLoaded.RevisionCount)
	}
	if regenLoaded.FinalMD != "" {
		t.Fatalf("expected final_md to be reset to empty, got %q", regenLoaded.FinalMD)
	}
	if len(regenLoaded.CritiqueNotes) != 0 {
		t.Fatalf("expected critique_notes to be reset to nil, got %v", regenLoaded.CritiqueNotes)
	}

	// Test FTS indexing and ClearReportFTS
	if err := ts.IndexReportFTS(run.ID, "Consensus Section", "Paxos and Raft consensus algorithms"); err != nil {
		t.Fatalf("IndexReportFTS failed: %v", err)
	}
	res, _ := ts.SearchFTS("Paxos", 10)
	if len(res) != 1 {
		t.Fatalf("expected 1 FTS result, got %d", len(res))
	}

	if err := ts.ClearReportFTS(run.ID); err != nil {
		t.Fatalf("ClearReportFTS failed: %v", err)
	}

	resAfterClear, _ := ts.SearchFTS("Paxos", 10)
	if len(resAfterClear) != 0 {
		t.Fatalf("expected 0 FTS results after ClearReportFTS, got %d", len(resAfterClear))
	}
}
