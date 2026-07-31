package quality

import (
	"log/slog"
	"sync/atomic"
)

// Budget tracks the number of extra calls made for quality checks during a single research run.
// It ensures that pathological queries don't balloon the cost or runtime by enforcing a ceiling.
type Budget struct {
	maxExtraCalls int32
	currentCalls  int32
	exhaustedLog  atomic.Bool
}

// NewBudget creates a new budget governor with the given ceiling.
// If maxExtraCalls <= 0, a default of 40 is used.
func NewBudget(maxExtraCalls int) *Budget {
	if maxExtraCalls <= 0 {
		maxExtraCalls = 40
	}
	return &Budget{
		maxExtraCalls: int32(maxExtraCalls),
	}
}

// TryAcquire attempts to consume one unit of the budget.
// It returns true if the call is allowed, false if the budget is exhausted.
func (b *Budget) TryAcquire() bool {
	if b == nil {
		return true // No budget limit if nil
	}
	
	newVal := atomic.AddInt32(&b.currentCalls, 1)
	if newVal > b.maxExtraCalls {
		// Only log exhaustion once per run
		if b.exhaustedLog.CompareAndSwap(false, true) {
			slog.Warn("quality_budget_exhausted: skipping remaining extra checks", "max_calls", b.maxExtraCalls)
		}
		return false
	}
	return true
}

// Stats returns the current budget usage.
func (b *Budget) Stats() (current, max int) {
	if b == nil {
		return 0, 0
	}
	curr := atomic.LoadInt32(&b.currentCalls)
	if curr > b.maxExtraCalls {
		curr = b.maxExtraCalls
	}
	return int(curr), int(b.maxExtraCalls)
}
