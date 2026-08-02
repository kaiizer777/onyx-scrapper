package timecontext

import (
	"testing"
	"time"
)

func TestNow(t *testing.T) {
	// Ensure cleanup
	defer ClearOverrideDate()

	// Default behavior
	ClearOverrideDate()
	realNow := Now()
	if realNow.IsZero() {
		t.Error("Expected non-zero time when no override is set")
	}

	// Override behavior
	mockTime := time.Date(2028, 1, 1, 0, 0, 0, 0, time.UTC)
	SetOverrideDate(mockTime)

	overriddenNow := Now()
	if !overriddenNow.Equal(mockTime) {
		t.Errorf("Expected %v, got %v", mockTime, overriddenNow)
	}

	// Clear override behavior
	ClearOverrideDate()
	clearedNow := Now()
	if clearedNow.Equal(mockTime) {
		t.Error("Expected time to be different from mock time after clear")
	}
}
