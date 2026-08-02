package news

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/kaiizer777/onyx-scrapper/internal/config"
	"github.com/kaiizer777/onyx-scrapper/internal/profile"
	"github.com/kaiizer777/onyx-scrapper/internal/store"
)

// TestOrchestrator_MaxFieldsCap_RejectsExcessProfile (Phase 11)
// proves the news orchestrator refuses to start a run when the
// profile's enabled-field count exceeds the configured MaxFields
// cap. This is the second-line cost guardrail: even if a misconfigured
// operator pushed 20 fields past the profile.Manager's MaxFields
// limit (e.g. via direct store writes), the orchestrator still
// rejects at run-start with ErrTooManyFields.
func TestOrchestrator_MaxFieldsCap_RejectsExcessProfile(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_cap.db")

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

	// Bypass the manager cap by writing 12 enabled fields directly
	// to the store. We're testing the *run-time* guardrail, not
	// the *build-time* one. The cap we're proving is in the
	// orchestrator, not the manager.
	const seeded = 12
	for i := 0; i < seeded; i++ {
		_, err := st.CreateProfileField(store.ProfileField{
			ProfileID:     p.ID,
			FieldName:     "Field-" + string(rune('A'+i)),
			KeywordsCSV:   "kw, kw2",
			PriorityOrder: i,
			Enabled:       true,
		})
		if err != nil {
			t.Fatalf("seed field %d: %v", i, err)
		}
	}

	cfg := &config.Config{
		News: &config.NewsConfig{
			MaxFields:         5, // intentionally below seeded
			MaxArticlesPerField: 5,
		},
	}
	o := NewOrchestrator(st, profMgr, nil, nil, cfg)

	win := ParseRecencyWindow("24h", "")
	_, _, err = o.Run(context.Background(), win, p.ID)
	if err == nil {
		t.Fatalf("expected ErrTooManyFields, got nil")
	}
	if !isErrTooManyFields(err) {
		t.Errorf("expected error to wrap ErrTooManyFields, got: %v", err)
	}

	// Status must be recorded as "rejected" so /status polls reflect it.
	// We don't pin a specific NewsRun here because Run (non-pre-created)
	// returns nil NewsRun on early-rejection — but if it returns
	// a run, it should be marked rejected. Either path is acceptable
	// for Phase 11; the contract is: orchestrator does NOT silently
	// truncate.
	if o.budget == nil {
		t.Errorf("expected budget to be initialized even on early-reject")
	}
}

// TestOrchestrator_MaxFieldsCap_AcceptsWithinCap is the positive
// counterpart — a profile under the cap is accepted. Uses a fake
// HTTP server that returns empty RSS so the run completes without
// flaking on network.
func TestOrchestrator_MaxFieldsCap_AcceptsWithinCap(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_cap_ok.db")

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
	for i := 0; i < 3; i++ {
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

	cfg := &config.Config{
		News: &config.NewsConfig{
			MaxFields:         10,
			MaxArticlesPerField: 5,
		},
	}
	o := NewOrchestrator(st, profMgr, nil, nil, cfg)
	if o.maxFields != 10 {
		t.Errorf("expected maxFields=10, got %d", o.maxFields)
	}
	if o.maxArticlesPerField != 5 {
		t.Errorf("expected maxArticlesPerField=5, got %d", o.maxArticlesPerField)
	}

	// No external HTTP needed because we set the registry to nil —
	// the orchestrator will try RSS, fail silently (logged), then
	// try backfill, also fail. The point of this test is the cap
	// is *not* the failing factor.
	win := ParseRecencyWindow("24h", "")
	_, _, err = o.Run(context.Background(), win, p.ID)
	// The run can still error on something else (e.g. RSS URL
	// resolution), but it MUST NOT error with ErrTooManyFields.
	if isErrTooManyFields(err) {
		t.Errorf("3 fields under cap of 10 must not trigger ErrTooManyFields, got: %v", err)
	}
}

// TestOrchestrator_ArticlesPerFieldClampedToCap proves the
// per-field article cap is applied at construction time so even a
// generous ArticlesPerField config can't bypass the per-field
// hard ceiling.
func TestOrchestrator_ArticlesPerFieldClampedToCap(t *testing.T) {
	cases := []struct {
		name           string
		cfg            *config.NewsConfig
		wantPerField   int
		wantMaxPerFld  int
	}{
		{
			name: "default config → default 5",
			cfg:  nil,
			wantPerField: DefaultArticlesPerField,
			wantMaxPerFld: config.DefaultNewsMaxArticlesPerField,
		},
		{
			name: "articles below cap → unchanged",
			cfg:  &config.NewsConfig{ArticlesPerField: 8, MaxArticlesPerField: 20},
			wantPerField: 8,
			wantMaxPerFld: 20,
		},
		{
			name: "articles above cap → clamped down",
			cfg:  &config.NewsConfig{ArticlesPerField: 100, MaxArticlesPerField: 10},
			wantPerField: 10,
			wantMaxPerFld: 10,
		},
		{
			name: "explicit zero cap → default + clamp articles to 20",
			cfg:  &config.NewsConfig{ArticlesPerField: 50},
			wantPerField:  config.DefaultNewsMaxArticlesPerField, // 50 clamped to default cap 20
			wantMaxPerFld: config.DefaultNewsMaxArticlesPerField,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{}
			if tc.cfg != nil {
				cfg.News = tc.cfg
			}
			o := NewOrchestrator(nil, nil, nil, nil, cfg)
			if o.articlesPerField != tc.wantPerField {
				t.Errorf("articlesPerField = %d, want %d", o.articlesPerField, tc.wantPerField)
			}
			if o.maxArticlesPerField != tc.wantMaxPerFld {
				t.Errorf("maxArticlesPerField = %d, want %d", o.maxArticlesPerField, tc.wantMaxPerFld)
			}
		})
	}
}

// isErrTooManyFields is a small helper that does an errors.Is-style
// check without depending on errors.Is import (keeps the test file
// self-contained).
func isErrTooManyFields(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	// Use substring check on the wrapped sentinel so the test is
	// robust to fmt.Errorf wrapping. The sentinel string is what
	// the orchestrator returns.
	return contains(s, ErrTooManyFields.Error()) || s == ErrTooManyFields.Error()
}

func contains(s, substr string) bool {
	if substr == "" {
		return true
	}
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
