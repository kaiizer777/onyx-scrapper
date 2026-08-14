package teacher

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kaiizer777/onyx-scrapper/internal/llm"
)

func TestOutline_TopologicalSorting(t *testing.T) {
	// Setup sections where:
	// sec_0: Section 0 (no prereqs)
	// sec_1: Foundations (depends on sec_0)
	// sec_2: Core Mechanics (depends on sec_1)
	// sec_3: Advanced Architectures (depends on sec_2)
	// sec_4: Production Deployment (depends on sec_2)
	rawSections := []OutlinePlannerSection{
		{
			ID:                "sec_3",
			Title:             "Advanced Architectures",
			LearningObjective: "Analyze multi-head and sparse attention mechanics.",
			DependsOn:         []string{"sec_2"},
		},
		{
			ID:                "sec_1",
			Title:             "Foundational Vector Math",
			LearningObjective: "Explain dot-product similarity and matrix dimensions.",
			DependsOn:         []string{"sec_0"},
		},
		{
			ID:                "sec_4",
			Title:             "Production Deployment",
			LearningObjective: "Deploy transformer models with KV caching.",
			DependsOn:         []string{"sec_2"},
		},
		{
			ID:                "sec_2",
			Title:             "Core Self-Attention Mechanics",
			LearningObjective: "Calculate query, key, and value transformations.",
			DependsOn:         []string{"sec_1"},
		},
		{
			ID:                "sec_0",
			Title:             "Why this matters / core intuition in one paragraph",
			LearningObjective: "Grasp why attention revolutionized sequence modeling.",
			DependsOn:         nil,
		},
	}

	sorted, err := TopologicallySortOutline(rawSections, "run_test_topo")
	if err != nil {
		t.Fatalf("TopologicallySortOutline failed: %v", err)
	}

	if len(sorted) != 5 {
		t.Fatalf("expected 5 sorted sections, got %d", len(sorted))
	}

	// Verify Section 0 is always at index 0 and has SectionOrder = 0
	if sorted[0].SectionOrder != 0 {
		t.Errorf("expected SectionOrder 0 for first element, got %d", sorted[0].SectionOrder)
	}
	if sorted[0].Title != SectionZeroTitle {
		t.Errorf("expected Section 0 title %q, got %q", SectionZeroTitle, sorted[0].Title)
	}

	// Verify topological order:
	// "Foundational Vector Math" must come before "Core Self-Attention Mechanics"
	// "Core Self-Attention Mechanics" must come before "Advanced Architectures" and "Production Deployment"
	titleOrder := make(map[string]int)
	for i, s := range sorted {
		titleOrder[s.Title] = i
	}

	if titleOrder["Foundational Vector Math"] >= titleOrder["Core Self-Attention Mechanics"] {
		t.Errorf("prerequisite order violation: Foundations (%d) should be before Core (%d)",
			titleOrder["Foundational Vector Math"], titleOrder["Core Self-Attention Mechanics"])
	}
	if titleOrder["Core Self-Attention Mechanics"] >= titleOrder["Advanced Architectures"] {
		t.Errorf("prerequisite order violation: Core (%d) should be before Advanced (%d)",
			titleOrder["Core Self-Attention Mechanics"], titleOrder["Advanced Architectures"])
	}
	if titleOrder["Core Self-Attention Mechanics"] >= titleOrder["Production Deployment"] {
		t.Errorf("prerequisite order violation: Core (%d) should be before Deployment (%d)",
			titleOrder["Core Self-Attention Mechanics"], titleOrder["Production Deployment"])
	}
}

func TestOutline_CycleDetectionAndResolution(t *testing.T) {
	// Test cyclic dependencies:
	// sec_1 depends on sec_2, sec_2 depends on sec_3, sec_3 depends on sec_1 (Cycle!)
	rawSections := []OutlinePlannerSection{
		{
			ID:                "sec_1",
			Title:             "Concept A",
			LearningObjective: "Explain Concept A",
			DependsOn:         []string{"sec_2"},
		},
		{
			ID:                "sec_2",
			Title:             "Concept B",
			LearningObjective: "Explain Concept B",
			DependsOn:         []string{"sec_3"},
		},
		{
			ID:                "sec_3",
			Title:             "Concept C",
			LearningObjective: "Explain Concept C",
			DependsOn:         []string{"sec_1"},
		},
	}

	sorted, err := TopologicallySortOutline(rawSections, "run_cycle_test")
	if err != nil {
		t.Fatalf("expected cycle to be gracefully resolved without error, got: %v", err)
	}

	// Cycle resolution should still produce all sections and include Section 0 prepended
	if len(sorted) != 4 { // 3 sections + 1 auto-prepended Section 0
		t.Fatalf("expected 4 sections after cycle resolution, got %d", len(sorted))
	}

	if sorted[0].SectionOrder != 0 {
		t.Errorf("expected SectionOrder 0 for Section 0, got %d", sorted[0].SectionOrder)
	}
	if sorted[0].Title != SectionZeroTitle {
		t.Errorf("expected Section 0 as first section, got %q", sorted[0].Title)
	}

	// Verify all section orders are sequential 0, 1, 2, 3
	for i, s := range sorted {
		if s.SectionOrder != i {
			t.Errorf("expected section index %d to have SectionOrder %d, got %d", i, i, s.SectionOrder)
		}
	}
}

func TestOutline_EnsureSectionZeroPrepend(t *testing.T) {
	// Model returns outline missing Section 0
	rawSections := []OutlinePlannerSection{
		{
			ID:                "sec_1",
			Title:             "Transformers Overview",
			LearningObjective: "Describe high-level encoder-decoder blocks.",
			DependsOn:         nil,
		},
		{
			ID:                "sec_2",
			Title:             "Self-Attention Deep Dive",
			LearningObjective: "Compute scaled dot-product attention step by step.",
			DependsOn:         []string{"sec_1"},
		},
	}

	normalized := ensureSectionZero(rawSections, "Transformer Architectures")
	if len(normalized) != 3 {
		t.Fatalf("expected 3 sections after prepending Section 0, got %d", len(normalized))
	}

	if normalized[0].ID != "sec_0" {
		t.Errorf("expected prepended section ID sec_0, got %s", normalized[0].ID)
	}
	if normalized[0].Title != SectionZeroTitle {
		t.Errorf("expected prepended section title %q, got %q", SectionZeroTitle, normalized[0].Title)
	}
}

func TestOutline_GenerateOutlineEndToEnd(t *testing.T) {
	rootStore, teacherStore := setupTestTeacherStore(t)
	defer rootStore.Close()

	// 1. Create a run and attach an initial brief
	run, err := teacherStore.CreateRun("Explain distributed consensus algorithms")
	if err != nil {
		t.Fatalf("failed to create run: %v", err)
	}

	brief := &LearningBrief{
		Topic:                "Raft and Paxos Consensus",
		Domain:               "Distributed Systems",
		LearnerLevel:         "Senior Engineer wanting deep internals",
		Motivation:           "Building a replicated state machine",
		Depth:                "deep_dive",
		KnownReferencePoints: []string{"TCP/IP", "Two-Phase Commit"},
		ExplicitScopeIn:      []string{"Leader Election", "Log Replication", "Safety invariants"},
		ExplicitScopeOut:     []string{"Byzantine Fault Tolerance"},
		FormatPreferences: FormatPreferences{
			Length:        "long",
			WantsDiagrams: true,
		},
		AssumptionsToAvoid: []string{"Do not assume hardware clocks are synchronized"},
	}

	if err := teacherStore.UpdateRunBrief(run.ID, brief); err != nil {
		t.Fatalf("failed to save brief: %v", err)
	}

	// 2. Set up mock LLM server returning structured outline
	server, client := newMockLLMServer(t, func(messages []llm.Message) (string, error) {
		resp := OutlinePlannerResponse{
			Thought: "Organizing distributed consensus outline from fundamentals to leader election, log replication, and safety.",
			Sections: []OutlinePlannerSection{
				{
					ID:                "sec_0",
					Title:             SectionZeroTitle,
					LearningObjective: "Understand why replicated state machines require consensus algorithms like Raft and Paxos.",
					DependsOn:         []string{},
				},
				{
					ID:                "sec_1",
					Title:             "Foundations: The Replicated State Machine Model",
					LearningObjective: "Explain how deterministic state machines and write-ahead logs achieve fault-tolerant replication.",
					DependsOn:         []string{"sec_0"},
				},
				{
					ID:                "sec_2",
					Title:             "Raft Leader Election & Term Transitions",
					LearningObjective: "Trace randomized election timers, RequestVote RPCs, and split-vote mitigation.",
					DependsOn:         []string{"sec_1"},
				},
				{
					ID:                "sec_3",
					Title:             "Log Replication & Commit Index Guarantees",
					LearningObjective: "Describe AppendEntries heartbeats, quorum acknowledgments, and log consistency checks.",
					DependsOn:         []string{"sec_2"},
				},
				{
					ID:                "sec_4",
					Title:             "Safety Invariants and Joint Consensus Membership Changes",
					LearningObjective: "Prove Leader Completeness and state machine safety across dynamic cluster reconfigurations.",
					DependsOn:         []string{"sec_3"},
				},
			},
		}
		b, _ := json.Marshal(resp)
		return string(b), nil
	})
	defer server.Close()

	orch := NewOrchestratorWithStore(client, teacherStore, nil, nil)

	// 3. Generate outline
	sections, err := orch.GenerateOutline(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("GenerateOutline failed: %v", err)
	}

	if len(sections) != 5 {
		t.Fatalf("expected 5 sections, got %d", len(sections))
	}

	// Verify Section 0 is at order 0
	if sections[0].SectionOrder != 0 {
		t.Errorf("expected SectionOrder 0 for section 0, got %d", sections[0].SectionOrder)
	}
	if sections[0].Title != SectionZeroTitle {
		t.Errorf("expected Section 0 title %q, got %q", SectionZeroTitle, sections[0].Title)
	}

	// Verify sections were persisted to SQLite
	persistedOutline, err := teacherStore.GetOutline(run.ID)
	if err != nil {
		t.Fatalf("failed to fetch persisted outline: %v", err)
	}
	if len(persistedOutline) != 5 {
		t.Fatalf("expected 5 persisted sections in DB, got %d", len(persistedOutline))
	}

	// Verify run status was updated to researching
	updatedRun, err := teacherStore.GetRun(run.ID)
	if err != nil {
		t.Fatalf("failed to get run: %v", err)
	}
	if updatedRun.Status != RunStatusResearching {
		t.Errorf("expected run status %q, got %q", RunStatusResearching, updatedRun.Status)
	}

	// Verify depends_on IDs point to persisted IDs
	if persistedOutline[1].DependsOn == "" {
		t.Errorf("expected section 1 to have prerequisite link to section 0")
	}
	if !strings.Contains(persistedOutline[1].DependsOn, persistedOutline[0].ID) {
		t.Errorf("expected section 1 DependsOn (%s) to contain section 0 ID (%s)",
			persistedOutline[1].DependsOn, persistedOutline[0].ID)
	}
}
