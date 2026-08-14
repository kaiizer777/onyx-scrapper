package teacher

import (
	"context"
	"strings"
	"testing"
)

func TestAssembler_FullMarkdownAssemblyAndFTS(t *testing.T) {
	rootStore, teacherStore := setupTestTeacherStore(t)
	defer rootStore.Close()

	run, err := teacherStore.CreateRun("Explain Raft Consensus")
	if err != nil {
		t.Fatalf("failed to create run: %v", err)
	}

	brief := &LearningBrief{
		Topic:                "Raft Consensus Protocol",
		Domain:               "Distributed Systems",
		LearnerLevel:         "Senior Infrastructure Engineer",
		Motivation:           "Building a high-availability key-value store",
		Depth:                "deep_dive",
		KnownReferencePoints: []string{"Paxos", "TCP/IP"},
		ExplicitScopeIn:      []string{"Leader Election", "Log Replication"},
		ExplicitScopeOut:     []string{"Byzantine Fault Tolerance", "Multi-Raft Sharding"},
	}
	_ = teacherStore.UpdateRunBrief(run.ID, brief)

	sec0 := TeacherOutlineSection{
		ID:                "sec_0",
		RunID:             run.ID,
		SectionOrder:      0,
		Title:             SectionZeroTitle,
		LearningObjective: "Grasp why replicated state machines need leader-driven consensus.",
		Status:            OutlineStatusDone,
	}
	sec1 := TeacherOutlineSection{
		ID:                "sec_1",
		RunID:             run.ID,
		SectionOrder:      1,
		Title:             "Leader Election and Heartbeats",
		LearningObjective: "Trace term increments, RequestVote RPCs, and randomized election timeouts.",
		Status:            OutlineStatusDone,
	}
	sec2 := TeacherOutlineSection{
		ID:                "sec_2",
		RunID:             run.ID,
		SectionOrder:      2,
		Title:             "Log Replication Invariants",
		LearningObjective: "Verify how AppendEntries ensures quorum consistency across followers.",
		Status:            OutlineStatusDone,
	}
	_ = teacherStore.SaveOutline([]TeacherOutlineSection{sec0, sec1, sec2})

	sec0Data := &TeacherSection{
		ID:            "ts_0",
		RunID:         run.ID,
		OutlineID:     sec0.ID,
		DraftMD:       "Draft 0",
		FinalMD:       "Replicated state machines keep multiple servers in sync by applying deterministic operations in the same sequence.",
		RevisionCount: 0,
	}
	sec1Data := &TeacherSection{
		ID:            "ts_1",
		RunID:         run.ID,
		OutlineID:     sec1.ID,
		DraftMD:       "Draft 1",
		FinalMD:       "In Raft, a single leader manages log entries.\n<!--glossary: Election Timeout=The randomized period a follower waits before becoming a candidate.-->\n<!--glossary: Quorum=A strict majority of nodes required for consensus.-->\nHeartbeats suppress election timeouts.",
		RevisionCount: 1,
	}
	sec2Data := &TeacherSection{
		ID:            "ts_2",
		RunID:         run.ID,
		OutlineID:     sec2.ID,
		DraftMD:       "Draft 2",
		FinalMD:       "Followers reject log entries if their preceding log index and term do not match.\n<!--glossary: Quorum=Duplicate definition that should be deduped.-->\n<!--glossary: Commit Index=The highest log entry known to be committed on a majority.-->\nThis guarantees the Leader Completeness property.",
		RevisionCount: 0,
	}

	_ = teacherStore.SaveSectionDraft(sec0Data)
	_ = teacherStore.UpdateSectionCritique(sec0Data.ID, nil, sec0Data.FinalMD, 0)

	_ = teacherStore.SaveSectionDraft(sec1Data)
	_ = teacherStore.UpdateSectionCritique(sec1Data.ID, nil, sec1Data.FinalMD, 1)

	_ = teacherStore.SaveSectionDraft(sec2Data)
	_ = teacherStore.UpdateSectionCritique(sec2Data.ID, nil, sec2Data.FinalMD, 0)

	orch := NewOrchestratorWithStore(nil, teacherStore, nil, nil)

	reportMD, err := orch.AssembleReport(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("AssembleReport failed: %v", err)
	}

	// 1. Verify Header & Title
	if !strings.Contains(reportMD, "# Raft Consensus Protocol") {
		t.Errorf("expected report to contain main H1 title")
	}

	// 2. Verify "What You'll Learn"
	if !strings.Contains(reportMD, "### What You'll Learn") || !strings.Contains(reportMD, "Leader Election and Heartbeats") {
		t.Errorf("expected report to contain What You'll Learn section with learning objectives")
	}

	// 3. Verify Table of Contents & Anchors
	if !strings.Contains(reportMD, "## Table of Contents") {
		t.Errorf("expected report to contain Table of Contents")
	}
	if !strings.Contains(reportMD, "[Why this matters / core intuition in one paragraph](#why-this-matters-core-intuition-in-one-paragraph)") {
		t.Errorf("expected TOC to contain anchor link for Section 0")
	}
	if !strings.Contains(reportMD, "[Leader Election and Heartbeats](#leader-election-and-heartbeats)") {
		t.Errorf("expected TOC to contain anchor link for Section 1")
	}
	if !strings.Contains(reportMD, "[Glossary](#glossary)") || !strings.Contains(reportMD, "[Where to Go Next](#where-to-go-next)") {
		t.Errorf("expected TOC to contain Glossary and Where to Go Next anchors")
	}

	// 4. Verify Section Headers and Content
	if !strings.Contains(reportMD, "## Leader Election and Heartbeats") {
		t.Errorf("expected section H2 header")
	}
	// Assert glossary tags were stripped from section bodies
	if strings.Contains(reportMD, "<!--glossary:") {
		t.Errorf("expected <!--glossary: ...--> comment tags to be stripped from rendered section text")
	}

	// 5. Verify Compiled Glossary
	if !strings.Contains(reportMD, "## Glossary") {
		t.Errorf("expected Glossary H2 section")
	}
	if !strings.Contains(reportMD, "- **Commit Index**:") ||
		!strings.Contains(reportMD, "- **Election Timeout**:") ||
		!strings.Contains(reportMD, "- **Quorum**:") {
		t.Errorf("expected Glossary to contain extracted terms")
	}

	// Verify deduplication: Quorum should only appear once in Glossary
	quorumCount := strings.Count(reportMD, "- **Quorum**:")
	if quorumCount != 1 {
		t.Errorf("expected Quorum to appear exactly once in Glossary, appeared %d times", quorumCount)
	}

	// 6. Verify Where to Go Next
	if !strings.Contains(reportMD, "## Where to Go Next") {
		t.Errorf("expected Where to Go Next section")
	}
	if !strings.Contains(reportMD, "Byzantine Fault Tolerance") || !strings.Contains(reportMD, "Multi-Raft Sharding") {
		t.Errorf("expected Where to Go Next to mention out-of-scope topics")
	}

	// 7. Verify Persistence & Status
	updatedRun, err := teacherStore.GetRun(run.ID)
	if err != nil || updatedRun == nil {
		t.Fatalf("failed to retrieve run from store: %v", err)
	}
	if updatedRun.Status != RunStatusDone {
		t.Errorf("expected run status %q, got %q", RunStatusDone, updatedRun.Status)
	}
	if updatedRun.CompletedAt == nil {
		t.Errorf("expected CompletedAt to be set")
	}
	if updatedRun.ReportMD != reportMD {
		t.Errorf("persisted report_md does not match assembled report")
	}

	// 8. Verify SQLite FTS5 Indexing
	results, err := teacherStore.SearchFTS("Heartbeats", 5)
	if err != nil {
		t.Fatalf("FTS search failed: %v", err)
	}
	if len(results) == 0 {
		t.Errorf("expected FTS search to find results for 'Heartbeats'")
	}
}

func TestAssembler_AnchorSlugGeneration(t *testing.T) {
	tests := []struct {
		title    string
		expected string
	}{
		{
			title:    "Why this matters / core intuition in one paragraph",
			expected: "why-this-matters-core-intuition-in-one-paragraph",
		},
		{
			title:    "Leader Election & Term Transitions (v1.2)",
			expected: "leader-election-term-transitions-v12",
		},
		{
			title:    "What You'll Learn!",
			expected: "what-youll-learn",
		},
		{
			title:    "---Special---Characters---",
			expected: "special-characters",
		},
	}

	for _, tt := range tests {
		actual := generateAnchorSlug(tt.title)
		if actual != tt.expected {
			t.Errorf("generateAnchorSlug(%q) = %q, expected %q", tt.title, actual, tt.expected)
		}
	}
}
