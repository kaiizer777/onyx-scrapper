package teacher

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kaiizer777/onyx-scrapper/internal/config"
	"github.com/kaiizer777/onyx-scrapper/internal/llm"
)

func TestCritic_DetectsBadDraftAndRefines(t *testing.T) {
	rootStore, teacherStore := setupTestTeacherStore(t)
	defer rootStore.Close()

	run, err := teacherStore.CreateRun("Learn Quantum Computing")
	if err != nil {
		t.Fatalf("failed to create run: %v", err)
	}

	brief := &LearningBrief{
		Topic:        "Quantum Superposition",
		Domain:       "Physics",
		LearnerLevel: "Undergraduate STEM student",
		Motivation:   "Exam preparation",
		Depth:        "working_understanding",
	}
	_ = teacherStore.UpdateRunBrief(run.ID, brief)

	secOutline := TeacherOutlineSection{
		ID:                "sec_quant_1",
		RunID:             run.ID,
		SectionOrder:      1,
		Title:             "Qubit Superposition Principles",
		LearningObjective: "Explain how probability amplitudes square to 1 in quantum states.",
		Status:            OutlineStatusCritiquing,
	}
	_ = teacherStore.SaveOutline([]TeacherOutlineSection{secOutline})

	// Initial flawed draft
	initialDraft := "In quantum computing, a qubit is in both 0 and 1 simultaneously like a classical coin spinning."
	initialSec := &TeacherSection{
		ID:            "ts_quant_1",
		RunID:         run.ID,
		OutlineID:     secOutline.ID,
		DraftMD:       initialDraft,
		RevisionCount: 0,
	}
	_ = teacherStore.SaveSectionDraft(initialSec)

	var callCount int32
	server, client := newMockLLMServer(t, func(messages []llm.Message) (string, error) {
		step := atomic.AddInt32(&callCount, 1)
		switch step {
		case 1:
			// 1st Critic call: Catches missing mathematical formalism and weak coin analogy
			resp := CritiqueEvaluationResponse{
				Issues: []CritiqueNote{
					{
						Issue:      "The coin spin analogy is misleading and lacks discussion of complex probability amplitudes.",
						Severity:   "major",
						Suggestion: "Incorporate probability amplitudes alpha and beta where |alpha|^2 + |beta|^2 = 1.",
					},
				},
				Verdict: "revise",
			}
			b, _ := json.Marshal(resp)
			return string(b), nil

		case 2:
			// 2nd Writer revision call: Corrects the draft
			revised := `Superposition is a linear combination of orthonormal basis states |0> and |1>.

Rather than a spinning classical coin, think of a vector pointing to a location on the Bloch sphere with continuous probability amplitudes alpha and beta.

For state |psi> = alpha|0> + beta|1>, normalization dictates |alpha|^2 + |beta|^2 = 1.

A common misconception is that superposition means the particle is physically taking two paths at once; mathematically it represents a single coherent state vector until measurement.

Recap: Superposition allows qubits to store complex amplitude linear combinations, providing exponential state space scaling.`
			return revised, nil

		case 3:
			// 3rd Critic call: Evaluates revised draft and passes
			resp := CritiqueEvaluationResponse{
				Issues:  []CritiqueNote{},
				Verdict: "pass",
			}
			b, _ := json.Marshal(resp)
			return string(b), nil

		default:
			t.Fatalf("unexpected extra LLM call: %d", step)
			return "", nil
		}
	})
	defer server.Close()

	orch := NewOrchestratorWithStore(client, teacherStore, nil, nil)

	refined, err := orch.CritiqueAndRefineSection(context.Background(), run.ID, secOutline.ID)
	if err != nil {
		t.Fatalf("CritiqueAndRefineSection failed: %v", err)
	}

	if refined.RevisionCount != 1 {
		t.Errorf("expected RevisionCount 1, got %d", refined.RevisionCount)
	}

	if !strings.Contains(refined.FinalMD, "Bloch sphere") {
		t.Errorf("expected FinalMD to contain revised content, got: %s", refined.FinalMD)
	}

	// Verify outline section status is marked done
	outlineList, _ := teacherStore.GetOutline(run.ID)
	if outlineList[0].Status != OutlineStatusDone {
		t.Errorf("expected outline status %q, got %q", OutlineStatusDone, outlineList[0].Status)
	}
}

func TestCritic_HardStopAtCritiquePassLimit(t *testing.T) {
	rootStore, teacherStore := setupTestTeacherStore(t)
	defer rootStore.Close()

	run, err := teacherStore.CreateRun("Learn Compilers")
	if err != nil {
		t.Fatalf("failed to create run: %v", err)
	}

	brief := &LearningBrief{
		Topic:        "LLVM IR Generation",
		Domain:       "Compilers",
		LearnerLevel: "Junior Developer",
		Motivation:   "Hobby language project",
		Depth:        "overview",
	}
	_ = teacherStore.UpdateRunBrief(run.ID, brief)

	secOutline := TeacherOutlineSection{
		ID:                "sec_llvm_1",
		RunID:             run.ID,
		SectionOrder:      1,
		Title:             "SSA Form & Phi Nodes",
		LearningObjective: "Explain Static Single Assignment invariants and Phi placement.",
		Status:            OutlineStatusCritiquing,
	}
	_ = teacherStore.SaveOutline([]TeacherOutlineSection{secOutline})

	initialSec := &TeacherSection{
		ID:            "ts_llvm_1",
		RunID:         run.ID,
		OutlineID:     secOutline.ID,
		DraftMD:       "Draft on SSA form.",
		RevisionCount: 0,
	}
	_ = teacherStore.SaveSectionDraft(initialSec)

	// Critic ALWAYS says revise with a major issue
	var criticCalls int32
	var writerCalls int32

	server, client := newMockLLMServer(t, func(messages []llm.Message) (string, error) {
		// Distinguish critic vs writer prompt
		isCritic := false
		for _, m := range messages {
			if strings.Contains(m.Content, "Rubric Dimensions") {
				isCritic = true
				break
			}
		}

		if isCritic {
			atomic.AddInt32(&criticCalls, 1)
			resp := CritiqueEvaluationResponse{
				Issues: []CritiqueNote{
					{
						Issue:      "Critical clarity issue that never gets resolved in mock.",
						Severity:   "major",
						Suggestion: "Make it clearer.",
					},
				},
				Verdict: "revise",
			}
			b, _ := json.Marshal(resp)
			return string(b), nil
		}

		// Writer revision call
		atomic.AddInt32(&writerCalls, 1)
		return "Another revised draft on SSA form.", nil
	})
	defer server.Close()

	cfg := &config.Config{
		Teacher: &config.TeacherConfig{
			CritiquePassLimit: 2, // Hard limit of 2 revisions
		},
	}

	orch := NewOrchestratorWithStore(client, teacherStore, nil, cfg)

	refined, err := orch.CritiqueAndRefineSection(context.Background(), run.ID, secOutline.ID)
	if err != nil {
		t.Fatalf("CritiqueAndRefineSection failed: %v", err)
	}

	// Should stop after 2 revisions
	if refined.RevisionCount != 2 {
		t.Errorf("expected RevisionCount to equal CritiquePassLimit (2), got %d", refined.RevisionCount)
	}

	// Should accept whatever draft existed and mark done
	if refined.FinalMD == "" {
		t.Errorf("expected FinalMD to be populated at hard stop limit")
	}

	outlineList, _ := teacherStore.GetOutline(run.ID)
	if outlineList[0].Status != OutlineStatusDone {
		t.Errorf("expected outline section status %q, got %q", OutlineStatusDone, outlineList[0].Status)
	}
}
