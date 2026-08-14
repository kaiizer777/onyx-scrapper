package teacher

import (
	"errors"
	"testing"
)

func TestBrief_GetAndPatchValidation(t *testing.T) {
	rootStore, teacherStore := setupTestTeacherStore(t)
	defer rootStore.Close()

	orch := NewOrchestratorWithStore(nil, teacherStore, nil, nil)

	// 1. Non-existent run
	_, err := orch.GetBrief("non_existent_run")
	if !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("expected ErrRunNotFound, got: %v", err)
	}

	// 2. Create run without brief
	run, err := teacherStore.CreateRun("Learn Quantum Computing")
	if err != nil {
		t.Fatalf("failed to create run: %v", err)
	}

	_, err = orch.GetBrief(run.ID)
	if !errors.Is(err, ErrBriefNotReady) {
		t.Fatalf("expected ErrBriefNotReady, got: %v", err)
	}

	// 3. Attach initial brief
	initialBrief := &LearningBrief{
		Topic:                "Quantum Superposition and Entanglement",
		Domain:               "Quantum Physics / CS",
		LearnerLevel:         "Undergraduate Physics student",
		Motivation:           "Preparing for quantum computing research",
		Depth:                "working_understanding",
		KnownReferencePoints: []string{"Linear Algebra", "Complex Numbers"},
		ExplicitScopeIn:      []string{"Qubits", "Bloch Sphere"},
		ExplicitScopeOut:     []string{"Fault Tolerant Error Correction"},
		FormatPreferences: FormatPreferences{
			Length:        "medium",
			WantsDiagrams: true,
		},
		AssumptionsToAvoid: []string{"Do not assume knowledge of Hilbert spaces"},
	}

	if err := teacherStore.UpdateRunBrief(run.ID, initialBrief); err != nil {
		t.Fatalf("failed to save initial brief: %v", err)
	}

	retrieved, err := orch.GetBrief(run.ID)
	if err != nil {
		t.Fatalf("failed to get brief: %v", err)
	}
	if retrieved.Topic != initialBrief.Topic {
		t.Errorf("expected topic %q, got %q", initialBrief.Topic, retrieved.Topic)
	}

	// 4. Test PatchBrief failure on invalid schema (empty topic)
	_, err = orch.PatchBrief(run.ID, func(b *LearningBrief) error {
		b.Topic = "" // invalid!
		return nil
	})
	if err == nil {
		t.Fatalf("expected PatchBrief to fail on empty topic validation")
	}

	// Verify un-modified brief remains in store
	unchanged, err := orch.GetBrief(run.ID)
	if err != nil {
		t.Fatalf("failed to get brief after failed patch: %v", err)
	}
	if unchanged.Topic != initialBrief.Topic {
		t.Errorf("expected brief topic to remain unchanged %q, got %q", initialBrief.Topic, unchanged.Topic)
	}

	// 5. Test successful PatchBrief
	patched, err := orch.PatchBrief(run.ID, func(b *LearningBrief) error {
		b.LearnerLevel = "Advanced Graduate Student"
		b.Depth = "deep_dive"
		b.ExplicitScopeIn = append(b.ExplicitScopeIn, "Density Matrices")
		return nil
	})
	if err != nil {
		t.Fatalf("PatchBrief failed: %v", err)
	}

	if patched.LearnerLevel != "Advanced Graduate Student" {
		t.Errorf("expected updated learner level, got %s", patched.LearnerLevel)
	}
	if patched.Depth != "deep_dive" {
		t.Errorf("expected depth 'deep_dive', got %s", patched.Depth)
	}
	if len(patched.ExplicitScopeIn) != 3 {
		t.Errorf("expected 3 in-scope items, got %d", len(patched.ExplicitScopeIn))
	}

	// Verify persistence in SQLite
	persistedRun, err := teacherStore.GetRun(run.ID)
	if err != nil {
		t.Fatalf("failed to reload run from store: %v", err)
	}
	if persistedRun.LearningBrief == nil || persistedRun.LearningBrief.LearnerLevel != "Advanced Graduate Student" {
		t.Errorf("persisted brief was not updated correctly in SQLite")
	}

	// 6. Test PatchBriefDirect
	newBrief := *patched
	newBrief.Topic = "Quantum Key Distribution (BB84 Protocol)"
	directPatched, err := orch.PatchBriefDirect(run.ID, &newBrief)
	if err != nil {
		t.Fatalf("PatchBriefDirect failed: %v", err)
	}
	if directPatched.Topic != "Quantum Key Distribution (BB84 Protocol)" {
		t.Errorf("expected updated topic %q, got %q", newBrief.Topic, directPatched.Topic)
	}
}
