package profile

import (
	"errors"
	"testing"

	"github.com/kaiizer777/onyx-scrapper/internal/store"
)

func setupTestManager(t *testing.T, maxFields int) (*Manager, *store.Store) {
	st, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("Failed to create in-memory store: %v", err)
	}
	mgr := NewManager(st, Config{MaxFields: maxFields})
	return mgr, st
}

func TestDefaultProfileInitialization(t *testing.T) {
	mgr, st := setupTestManager(t, 5)
	defer st.Close()

	prof, err := mgr.GetOrCreateDefaultProfile()
	if err != nil {
		t.Fatalf("GetOrCreateDefaultProfile failed: %v", err)
	}
	if prof == nil || prof.Name != DefaultProfileName {
		t.Fatalf("expected profile with name %q, got %+v", DefaultProfileName, prof)
	}

	// Calling again should return existing profile
	prof2, err := mgr.GetOrCreateDefaultProfile()
	if err != nil {
		t.Fatalf("second GetOrCreateDefaultProfile failed: %v", err)
	}
	if prof2.ID != prof.ID {
		t.Fatalf("expected same profile ID %d, got %d", prof.ID, prof2.ID)
	}
}

func TestValidationFunctions(t *testing.T) {
	t.Run("ValidateFieldName", func(t *testing.T) {
		name, err := ValidateFieldName("  AI & ML  ")
		if err != nil || name != "AI & ML" {
			t.Fatalf("unexpected result: name=%q, err=%v", name, err)
		}

		_, err = ValidateFieldName("   ")
		if !errors.Is(err, ErrEmptyFieldName) {
			t.Fatalf("expected ErrEmptyFieldName, got %v", err)
		}
	})

	t.Run("ValidateKeywordsCSV", func(t *testing.T) {
		csv, keywords, err := ValidateKeywordsCSV(" LLM , generative AI, , machine learning ")
		if err != nil {
			t.Fatalf("ValidateKeywordsCSV failed: %v", err)
		}
		if csv != "LLM, generative AI, machine learning" {
			t.Fatalf("unexpected cleaned CSV: %q", csv)
		}
		if len(keywords) != 3 {
			t.Fatalf("expected 3 keywords, got %d", len(keywords))
		}

		_, _, err = ValidateKeywordsCSV(" , , ")
		if !errors.Is(err, ErrEmptyKeywords) {
			t.Fatalf("expected ErrEmptyKeywords, got %v", err)
		}
	})
}

func TestAddFieldValidationsAndBoundaries(t *testing.T) {
	mgr, st := setupTestManager(t, 2) // Max 2 fields
	defer st.Close()

	prof, err := mgr.GetOrCreateDefaultProfile()
	if err != nil {
		t.Fatalf("failed to setup profile: %v", err)
	}

	// 1. Valid Add
	f1, err := mgr.AddField(prof.ID, "AI/ML", "LLM, GPT-4", 1, true)
	if err != nil || f1 == nil {
		t.Fatalf("AddField f1 failed: %v", err)
	}
	if f1.KeywordsCSV != "LLM, GPT-4" {
		t.Fatalf("unexpected keywords: %q", f1.KeywordsCSV)
	}

	// 2. Empty Field Name
	_, err = mgr.AddField(prof.ID, "  ", "LLM", 2, true)
	if !errors.Is(err, ErrEmptyFieldName) {
		t.Fatalf("expected ErrEmptyFieldName, got %v", err)
	}

	// 3. Empty Keywords
	_, err = mgr.AddField(prof.ID, "Gaming", " , ", 2, true)
	if !errors.Is(err, ErrEmptyKeywords) {
		t.Fatalf("expected ErrEmptyKeywords, got %v", err)
	}

	// 4. Duplicate Field Name (case-insensitive)
	_, err = mgr.AddField(prof.ID, "ai/ml", "neural networks", 2, true)
	if !errors.Is(err, ErrDuplicateFieldName) {
		t.Fatalf("expected ErrDuplicateFieldName, got %v", err)
	}

	// 5. Add second field (reaches limit of 2)
	f2, err := mgr.AddField(prof.ID, "Gaming", "Unreal, Unity", 2, true)
	if err != nil || f2 == nil {
		t.Fatalf("AddField f2 failed: %v", err)
	}

	// 6. Max Fields Boundary Exceeded
	_, err = mgr.AddField(prof.ID, "Cricket", "IPL, T20", 3, true)
	if !errors.Is(err, ErrMaxFieldsExceeded) {
		t.Fatalf("expected ErrMaxFieldsExceeded, got %v", err)
	}
}

func TestUpdateAndRemoveField(t *testing.T) {
	mgr, st := setupTestManager(t, 5)
	defer st.Close()

	prof, err := mgr.GetOrCreateDefaultProfile()
	if err != nil {
		t.Fatalf("failed to setup profile: %v", err)
	}

	f1, _ := mgr.AddField(prof.ID, "AI/ML", "LLM", 1, true)
	f2, _ := mgr.AddField(prof.ID, "Gaming", "Unreal", 2, true)

	// Update f1 with duplicate name of f2 -> should fail
	f1.FieldName = "gaming"
	err = mgr.UpdateField(*f1)
	if !errors.Is(err, ErrDuplicateFieldName) {
		t.Fatalf("expected ErrDuplicateFieldName on update, got %v", err)
	}

	// Update f1 with valid new values
	f1.FieldName = "Artificial Intelligence"
	f1.KeywordsCSV = "LLM, Claude, Gemini"
	err = mgr.UpdateField(*f1)
	if err != nil {
		t.Fatalf("UpdateField failed: %v", err)
	}

	pwf, err := mgr.GetProfileWithFields(prof.ID)
	if err != nil || len(pwf.Fields) != 2 {
		t.Fatalf("GetProfileWithFields failed: %v", err)
	}
	if pwf.Fields[0].FieldName != "Artificial Intelligence" {
		t.Fatalf("updated field name not reflected: %q", pwf.Fields[0].FieldName)
	}

	// Remove f2
	if err := mgr.RemoveField(f2.ID); err != nil {
		t.Fatalf("RemoveField failed: %v", err)
	}

	pwf2, err := mgr.GetProfileWithFields(prof.ID)
	if err != nil || len(pwf2.Fields) != 1 {
		t.Fatalf("expected 1 remaining field, got %d", len(pwf2.Fields))
	}

	// Remove non-existent field -> ErrFieldNotFound
	err = mgr.RemoveField(99999)
	if !errors.Is(err, ErrFieldNotFound) {
		t.Fatalf("expected ErrFieldNotFound, got %v", err)
	}
}

func TestProfileNotFound(t *testing.T) {
	mgr, st := setupTestManager(t, 5)
	defer st.Close()

	_, err := mgr.AddField(9999, "Test", "kw", 1, true)
	if !errors.Is(err, ErrProfileNotFound) {
		t.Fatalf("expected ErrProfileNotFound, got %v", err)
	}

	_, err = mgr.GetProfileWithFields(9999)
	if !errors.Is(err, ErrProfileNotFound) {
		t.Fatalf("expected ErrProfileNotFound, got %v", err)
	}
}

func TestSyncFields(t *testing.T) {
	mgr, st := setupTestManager(t, 3)
	defer st.Close()

	prof, err := mgr.GetOrCreateDefaultProfile()
	if err != nil {
		t.Fatalf("failed to setup profile: %v", err)
	}

	fieldsToSync := []store.ProfileField{
		{FieldName: "AI/ML", KeywordsCSV: "LLM, GPT", Enabled: true},
		{FieldName: "Gaming", KeywordsCSV: "Steam, PC", Enabled: false},
	}

	synced, err := mgr.SyncFields(prof.ID, fieldsToSync)
	if err != nil {
		t.Fatalf("SyncFields failed: %v", err)
	}
	if len(synced) != 2 {
		t.Fatalf("expected 2 synced fields, got %d", len(synced))
	}
	if synced[0].PriorityOrder != 1 || synced[1].PriorityOrder != 2 {
		t.Fatalf("unexpected priority order: %d, %d", synced[0].PriorityOrder, synced[1].PriorityOrder)
	}

	// Test duplicate name
	dups := []store.ProfileField{
		{FieldName: "AI/ML", KeywordsCSV: "LLM"},
		{FieldName: "ai/ml", KeywordsCSV: "GPT"},
	}
	_, err = mgr.SyncFields(prof.ID, dups)
	if !errors.Is(err, ErrDuplicateFieldName) {
		t.Fatalf("expected ErrDuplicateFieldName, got %v", err)
	}

	// Test max fields exceeded (limit 3)
	tooMany := []store.ProfileField{
		{FieldName: "F1", KeywordsCSV: "k1"},
		{FieldName: "F2", KeywordsCSV: "k2"},
		{FieldName: "F3", KeywordsCSV: "k3"},
		{FieldName: "F4", KeywordsCSV: "k4"},
	}
	_, err = mgr.SyncFields(prof.ID, tooMany)
	if !errors.Is(err, ErrMaxFieldsExceeded) {
		t.Fatalf("expected ErrMaxFieldsExceeded, got %v", err)
	}
}

