package store

import (
	"testing"
)

func TestProfileStore(t *testing.T) {
	st, err := NewStore(":memory:")
	if err != nil {
		t.Fatalf("NewStore in memory failed: %v", err)
	}
	defer st.Close()

	// 1. Create Profile
	prof, err := st.CreateProfile("Default Profile")
	if err != nil {
		t.Fatalf("CreateProfile failed: %v", err)
	}
	if prof == nil || prof.ID <= 0 || prof.Name != "Default Profile" {
		t.Fatalf("unexpected profile created: %+v", prof)
	}

	// 2. Reject Duplicate Profile Name
	_, err = st.CreateProfile("Default Profile")
	if err == nil {
		t.Fatalf("expected error on duplicate profile name, got nil")
	}

	// 3. Get Profile by ID & Name
	gotID, err := st.GetProfile(prof.ID)
	if err != nil || gotID == nil || gotID.Name != "Default Profile" {
		t.Fatalf("GetProfile failed: %v, got %+v", err, gotID)
	}

	gotName, err := st.GetProfileByName("Default Profile")
	if err != nil || gotName == nil || gotName.ID != prof.ID {
		t.Fatalf("GetProfileByName failed: %v, got %+v", err, gotName)
	}

	// 4. Create Profile Fields
	f1, err := st.CreateProfileField(ProfileField{
		ProfileID:     prof.ID,
		FieldName:     "AI/ML",
		KeywordsCSV:   "LLM, generative AI, machine learning",
		PriorityOrder: 1,
		Enabled:       true,
	})
	if err != nil {
		t.Fatalf("CreateProfileField f1 failed: %v", err)
	}

	f2, err := st.CreateProfileField(ProfileField{
		ProfileID:     prof.ID,
		FieldName:     "Gaming",
		KeywordsCSV:   "game dev, graphics, unreal engine",
		PriorityOrder: 2,
		Enabled:       false,
	})
	if err != nil {
		t.Fatalf("CreateProfileField f2 failed: %v", err)
	}

	// 5. Reject Duplicate Field Name per Profile
	_, err = st.CreateProfileField(ProfileField{
		ProfileID:     prof.ID,
		FieldName:     "AI/ML",
		KeywordsCSV:   "dup keywords",
		PriorityOrder: 3,
		Enabled:       true,
	})
	if err == nil {
		t.Fatalf("expected error on duplicate field_name in same profile, got nil")
	}

	// 6. List Profile Fields & Enabled Profile Fields
	allFields, err := st.ListProfileFields(prof.ID)
	if err != nil || len(allFields) != 2 {
		t.Fatalf("ListProfileFields failed: %v, got %d items", err, len(allFields))
	}
	if allFields[0].FieldName != "AI/ML" || allFields[1].FieldName != "Gaming" {
		t.Fatalf("ListProfileFields order unexpected: %+v", allFields)
	}

	enabledFields, err := st.ListEnabledProfileFields(prof.ID)
	if err != nil || len(enabledFields) != 1 {
		t.Fatalf("ListEnabledProfileFields failed: %v, got %d items", err, len(enabledFields))
	}
	if enabledFields[0].ID != f1.ID {
		t.Fatalf("ListEnabledProfileFields unexpected item: %+v", enabledFields[0])
	}

	// 7. Update Field
	f2.Enabled = true
	f2.KeywordsCSV = "game dev, indie games"
	if err := st.UpdateProfileField(*f2); err != nil {
		t.Fatalf("UpdateProfileField failed: %v", err)
	}

	updatedF2, err := st.GetProfileField(f2.ID)
	if err != nil || updatedF2 == nil || !updatedF2.Enabled || updatedF2.KeywordsCSV != "game dev, indie games" {
		t.Fatalf("GetProfileField after update failed: %v, got %+v", err, updatedF2)
	}

	// 8. Count Profile Fields
	cnt, err := st.CountProfileFields(prof.ID)
	if err != nil || cnt != 2 {
		t.Fatalf("CountProfileFields failed: %v, count=%d", err, cnt)
	}

	// 9. Cascade Delete Profile
	if err := st.DeleteProfile(prof.ID); err != nil {
		t.Fatalf("DeleteProfile failed: %v", err)
	}

	remainingFields, err := st.ListProfileFields(prof.ID)
	if err != nil || len(remainingFields) != 0 {
		t.Fatalf("expected 0 fields after cascade profile delete, got %d", len(remainingFields))
	}
}
