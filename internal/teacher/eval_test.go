package teacher

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaiizer777/onyx-scrapper/internal/config"
	"github.com/kaiizer777/onyx-scrapper/internal/discovery"
	"github.com/kaiizer777/onyx-scrapper/internal/llm"
	"github.com/kaiizer777/onyx-scrapper/internal/store"
)

// goldenTopic defines test parameters for each evaluation benchmark.
type goldenTopic struct {
	Topic        string
	Domain       string
	LearnerLevel string
	Depth        string
	Motivation   string
	RefPoints    []string
	ScopeIn      []string
	ScopeOut     []string
}

func TestGoldenFixtures_StructuralInvariants(t *testing.T) {
	topics := []goldenTopic{
		{
			Topic:        "Transformer Attention",
			Domain:       "Machine Learning",
			LearnerLevel: "intermediate",
			Depth:        "working_understanding",
			Motivation:   "Understand how self-attention works in large language models",
			RefPoints:    []string{"Vector dot products", "Matrix multiplication"},
			ScopeIn:      []string{"Query, Key, Value vectors", "Scaled Dot-Product", "Multi-Head Attention"},
			ScopeOut:     []string{"Training backpropagation math", "CUDA kernel implementations"},
		},
		{
			Topic:        "Causes of WWI",
			Domain:       "World History",
			LearnerLevel: "beginner",
			Depth:        "overview",
			Motivation:   "Understand the geopolitical tensions leading up to 1914",
			RefPoints:    []string{"Modern alliances", "Domino effect"},
			ScopeIn:      []string{"MAIN causes (Militarism, Alliances, Imperialism, Nationalism)", "Assassination of Franz Ferdinand"},
			ScopeOut:     []string{"Detailed battlefield tactics of 1917"},
		},
		{
			Topic:        "Raft Consensus",
			Domain:       "Distributed Systems",
			LearnerLevel: "advanced",
			Depth:        "deep_dive",
			Motivation:   "Understand leader election, log replication, and safety guarantees",
			RefPoints:    []string{"State machines", "Two-phase commit"},
			ScopeIn:      []string{"Leader election", "Log replication", "Safety invariant (Election restriction)", "Heartbeats"},
			ScopeOut:     []string{"Multi-Paxos comparison details"},
		},
		{
			Topic:        "Photosynthesis Light Reactions",
			Domain:       "Biochemistry",
			LearnerLevel: "intermediate",
			Depth:        "working_understanding",
			Motivation:   "Understand electron transport chain and ATP/NADPH synthesis in thylakoid membranes",
			RefPoints:    []string{"Solar panels", "Battery charging"},
			ScopeIn:      []string{"Photosystem II and I", "Electron transport chain", "ATP Synthase", "Water photolysis"},
			ScopeOut:     []string{"Calvin Cycle dark reactions"},
		},
		{
			Topic:        "Music Scales vs Modes",
			Domain:       "Music Theory",
			LearnerLevel: "intermediate",
			Depth:        "working_understanding",
			Motivation:   "Understand modal flavours and how they differ from standard major/minor scales",
			RefPoints:    []string{"Color palette", "Mood variations"},
			ScopeIn:      []string{"Major scale formula", "7 Greek modes (Ionian, Dorian, Phrygian, Lydian, Mixolydian, Aeolian, Locrian)", "Characteristic pitch degrees"},
			ScopeOut:     []string{"Microtonal scales", "Jazz chord substitutions"},
		},
	}

	for _, g := range topics {
		t.Run(g.Topic, func(t *testing.T) {
			// Mock LLM server generating compliant outlines and rich drafts with glossary
			mockLLMServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				var body map[string]interface{}
				_ = json.NewDecoder(r.Body).Decode(&body)
				messages, _ := body["messages"].([]interface{})
				isOutline := false
				for _, m := range messages {
					if mObj, ok := m.(map[string]interface{}); ok {
						if content, ok := mObj["content"].(string); ok && (strings.Contains(content, "Curriculum Designer") || strings.Contains(content, "JSON")) {
							isOutline = true
							break
						}
					}
				}

				var content string
				if isOutline {
					content = fmt.Sprintf(`{
						"sections": [
							{"id": "sec_0", "title": "Core Intuition: Understanding %s", "learning_objective": "Build initial mental model", "depends_on": []},
							{"id": "sec_1", "title": "Foundational Mechanics of %s", "learning_objective": "Understand building blocks", "depends_on": ["sec_0"]},
							{"id": "sec_2", "title": "Deep Dive and Application of %s", "learning_objective": "Apply concepts in practice", "depends_on": ["sec_1"]},
							{"id": "sec_3", "title": "Common Misconceptions and Summary", "learning_objective": "Consolidate learning", "depends_on": ["sec_2"]}
						],
						"verdict": "pass",
						"issues": []
					}`, g.Topic, g.Topic, g.Topic)
				} else {
					content = fmt.Sprintf(`## Section Overview

This comprehensive section covers the core concepts of %s with high technical accuracy and clear analogies.

<!-- glossary: key_mechanism=Fundamental mechanism defining %s operations -->

`+"```mermaid\ngraph TD;\nA-->B;\n```\n", g.Topic, g.Topic)
				}

				resp := map[string]interface{}{
					"choices": []map[string]interface{}{
						{
							"message": map[string]interface{}{
								"role":    "assistant",
								"content": content,
							},
						},
					},
				}
				_ = json.NewEncoder(w).Encode(resp)
			}))
			defer mockLLMServer.Close()

			dbPath := filepath.Join(t.TempDir(), "eval_teacher.db")
			rootStore, err := store.NewStore(dbPath)
			if err != nil {
				t.Fatalf("failed to create store: %v", err)
			}
			defer rootStore.Close()

			cfg := &config.Config{
				ActiveProvider: "custom",
				Providers: map[string]config.ProviderConfig{
					"custom": {
						BaseURL: mockLLMServer.URL,
						Model:   "mock-eval-model",
						APIKey:  "test-eval-key",
					},
				},
				Teacher: &config.TeacherConfig{
					SectionWorkerConcurrency: 2,
					CritiquePassLimit:        1,
				},
			}

			llmClient := llm.NewClient(cfg.ActiveProviderConfig())
			registry := discovery.NewRegistry(nil, nil, nil, nil)
			orch := NewOrchestrator(llmClient, rootStore, registry, cfg)

			run, err := orch.Store().CreateRun(g.Topic)
			if err != nil {
				t.Fatalf("failed to create run: %v", err)
			}

			wantsCode := true
			brief := &LearningBrief{
				Topic:                g.Topic,
				Domain:               g.Domain,
				LearnerLevel:         g.LearnerLevel,
				Depth:                g.Depth,
				Motivation:           g.Motivation,
				KnownReferencePoints: g.RefPoints,
				ExplicitScopeIn:      g.ScopeIn,
				ExplicitScopeOut:     g.ScopeOut,
				FormatPreferences:    FormatPreferences{Length: "medium", WantsDiagrams: true, WantsCodeExamples: &wantsCode},
				AssumptionsToAvoid:   []string{"Do not use unexplained jargon"},
			}
			if err := orch.Store().UpdateRunBrief(run.ID, brief); err != nil {
				t.Fatalf("failed to save brief: %v", err)
			}

			runResult, err := orch.GenerateReport(context.Background(), run.ID)
			if err != nil {
				t.Fatalf("GenerateReport failed for %q: %v", g.Topic, err)
			}
			reportMD := runResult.ReportMD

			// Invariant 1: Non-empty report
			if len(reportMD) < 500 {
				t.Errorf("report is too short (%d bytes), expected > 500 bytes", len(reportMD))
			}

			// Invariant 2: Section count between 3 and 8
			sections, err := orch.Store().GetSectionsForRun(run.ID)
			if err != nil {
				t.Fatalf("failed to get sections: %v", err)
			}
			if len(sections) < 3 || len(sections) > 8 {
				t.Errorf("expected section count between 3 and 8, got %d", len(sections))
			}

			// Invariant 3: Valid TOC anchors exist in report
			if !strings.Contains(reportMD, "## Table of Contents") {
				t.Errorf("report missing Table of Contents header")
			}

			// Invariant 4: Non-empty Glossary section
			if !strings.Contains(reportMD, "## Glossary") {
				t.Errorf("report missing Glossary section")
			}

			// Invariant 5: No stub sections (< 100 bytes)
			for i, sec := range sections {
				if len(sec.FinalMD) < 100 {
					t.Errorf("section %d (%s) is a stub (%d bytes)", i, sec.OutlineID, len(sec.FinalMD))
				}
			}

			// Invariant 6: Verified FTS indexing
			searchResults, err := orch.Store().SearchFTS(g.Topic, 10)
			if err != nil {
				t.Errorf("FTS search error: %v", err)
			}
			if len(searchResults) == 0 {
				t.Errorf("expected FTS search hits for topic %q", g.Topic)
			}
		})
	}
}
