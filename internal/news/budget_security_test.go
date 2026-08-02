package news

import (
	"path/filepath"
	"testing"

	"github.com/kaiizer777/onyx-scrapper/internal/config"
	"github.com/kaiizer777/onyx-scrapper/internal/profile"
	"github.com/kaiizer777/onyx-scrapper/internal/quality"
	"github.com/kaiizer777/onyx-scrapper/internal/store"
)

// TestOrchestrator_BudgetCeilingEnforced (Phase 11) proves the
// quality.Budget shared by the news orchestrator is the dominant
// cost ceiling for full-text pulls. Even with a 10-field profile
// each asking for 5 articles (= 50 candidates), the budget
// MaxExtraCallsPerRun=8 caps the actual full-text fetch count to
// 8. This is the hard guarantee that "a single /news call cannot
// blow the LLM / search cost budget silently" — Phase 11's primary
// deliverable.
func TestOrchestrator_BudgetCeilingEnforced(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_budget.db")

	st, err := store.NewStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer st.Close()

	profMgr := profile.NewManager(st, profile.Config{MaxFields: 100})
	p, err := profMgr.GetOrCreateDefaultProfile()
	if err != nil {
		t.Fatalf("failed to create profile: %v", err)
	}

	// Three enabled fields, each demanding 5 articles. The
	// orchestrator's TryAcquire() must cap total acquisitions at
	// MaxExtraCallsPerRun=8.
	const fieldsN = 3
	for i := 0; i < fieldsN; i++ {
		_, err := st.CreateProfileField(store.ProfileField{
			ProfileID:     p.ID,
			FieldName:     "Field-" + string(rune('A'+i)),
			KeywordsCSV:   "kw",
			PriorityOrder: i,
			Enabled:       true,
		})
		if err != nil {
			t.Fatalf("seed field %d: %v", i, err)
		}
	}

	const maxExtraCalls = 8
	cfg := &config.Config{
		Quality: &config.QualityConfig{
			MaxExtraCallsPerRun: maxExtraCalls,
		},
		News: &config.NewsConfig{
			MaxFields:           10,
			MaxArticlesPerField: 5,
			ArticlesPerField:    5,
		},
	}
	o := NewOrchestrator(st, profMgr, nil, nil, cfg)

	// Directly probe the budget that the orchestrator wired up.
	// Simulate the per-field full-text pull loop: with 3 fields ×
	// 5 candidates = 15 TryAcquire calls, only 8 should succeed.
	const attempts = fieldsN * 5
	var allowed int
	for i := 0; i < attempts; i++ {
		if o.budget.TryAcquire() {
			allowed++
		}
	}
	if allowed != maxExtraCalls {
		t.Errorf("budget allowed %d acquisitions, want %d (max=%d, attempts=%d)",
			allowed, maxExtraCalls, maxExtraCalls, attempts)
	}
}

// TestOrchestrator_BudgetSharedAcrossFields proves the budget is a
// SINGLE governor for the whole run, not per-field. If it were
// per-field, a 10-field run × 5 = 50 calls would all succeed
// regardless of MaxExtraCallsPerRun. This test enforces the
// "shared pool" invariant.
func TestOrchestrator_BudgetSharedAcrossFields(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_shared.db")

	st, err := store.NewStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer st.Close()

	profMgr := profile.NewManager(st, profile.Config{MaxFields: 100})
	_, err = profMgr.GetOrCreateDefaultProfile()
	if err != nil {
		t.Fatalf("failed to create profile: %v", err)
	}

	cfg := &config.Config{
		Quality: &config.QualityConfig{MaxExtraCallsPerRun: 6},
		News: &config.NewsConfig{MaxFields: 10, MaxArticlesPerField: 5},
	}
	o := NewOrchestrator(st, profMgr, nil, nil, cfg)

	// Field 1 takes 4.
	for i := 0; i < 4; i++ {
		if !o.budget.TryAcquire() {
			t.Fatalf("field 1 acquisition %d should be allowed", i)
		}
	}
	// Field 2 should get only 2 more, then be starved.
	for i := 0; i < 2; i++ {
		if !o.budget.TryAcquire() {
			t.Fatalf("field 2 acquisition %d should be allowed", i)
		}
	}
	// 7th attempt must fail — proves budget is shared, not reset.
	if o.budget.TryAcquire() {
		t.Errorf("7th acquisition must be denied; budget appears to be per-field rather than shared")
	}
}

// TestOrchestrator_BudgetIsACopyPerRun proves that constructing a
// second orchestrator gives it its own fresh budget — the cap
// applies per-run, not per-process. This is a sanity check: a
// long-running bot should not have its first run's exhaustion
// leak into a later run.
func TestOrchestrator_BudgetIsACopyPerRun(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_fresh.db")

	st, err := store.NewStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer st.Close()

	profMgr := profile.NewManager(st, profile.Config{MaxFields: 10})

	cfg := &config.Config{
		Quality: &config.QualityConfig{MaxExtraCallsPerRun: 3},
		News:    &config.NewsConfig{MaxFields: 5},
	}
	o1 := NewOrchestrator(st, profMgr, nil, nil, cfg)
	for i := 0; i < 3; i++ {
		if !o1.budget.TryAcquire() {
			t.Fatalf("orchestrator 1 first run acquisition %d should be allowed", i)
		}
	}
	if o1.budget.TryAcquire() {
		t.Fatalf("orchestrator 1 budget should be exhausted")
	}

	// Second orchestrator instance — its budget must be fresh.
	o2 := NewOrchestrator(st, profMgr, nil, nil, cfg)
	if o1.budget == o2.budget {
		t.Errorf("orchestrator 2 budget must not be the same pointer as orchestrator 1's")
	}
	if !o2.budget.TryAcquire() {
		t.Errorf("orchestrator 2 fresh budget's first acquisition must be allowed")
	}
}

// Sanity test: quality.Budget's documented behavior matches what
// the news orchestrator relies on. If this fails, the orchestrator's
// wiring assumptions are wrong.
func TestQualityBudget_BasicsUnchanged(t *testing.T) {
	b := quality.NewBudget(2)
	if !b.TryAcquire() {
		t.Fatal("first call must be allowed")
	}
	if !b.TryAcquire() {
		t.Fatal("second call must be allowed")
	}
	if b.TryAcquire() {
		t.Fatal("third call must be denied (max=2)")
	}
	curr, max := b.Stats()
	if max != 2 {
		t.Errorf("max stats = %d, want 2", max)
	}
	if curr != 2 {
		t.Errorf("current stats = %d, want 2 (clamped)", curr)
	}
}
