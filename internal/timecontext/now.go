package timecontext

import "time"

var overrideDate *time.Time

// Now returns the current system time, or the overridden date if set.
func Now() time.Time {
	if overrideDate != nil {
		return *overrideDate
	}
	return time.Now()
}

// SetOverrideDate allows tests or CLI flags to force a specific current date.
func SetOverrideDate(t time.Time) {
	overrideDate = &t
}

// ClearOverrideDate removes any override, returning Now() to the real system time.
func ClearOverrideDate() {
	overrideDate = nil
}
