package news

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaiizer777/onyx-scrapper/internal/config"
	"github.com/kaiizer777/onyx-scrapper/internal/profile"
	"github.com/kaiizer777/onyx-scrapper/internal/store"
)

// panicOnQueryTransport is a tiny http.RoundTripper that panics
// when the outgoing URL's `q` query parameter contains any of the
// configured substrings, and otherwise proxies the request to a
// real HTTP test server.
//
// Phase 12 invariant: one field's panic must not kill its sibling
// fields. The transport panics from inside the goroutine that
// fetchNewsForField is running on, which is exactly the failure
// mode the orchestrator has to absorb.
//
// We can't intercept by host because the orchestrator hardcodes
// news.google.com. So we intercept by query string: each field
// has a unique keyword, and the field's keyword is echoed into the
// `q=` query parameter. A field that panics has a "doomed"
// keyword containing one of the configured substrings.
type panicOnQueryTransport struct {
	panicQuerySubstrings []string
	targetURL            string
	panicCount           int32
}

// RoundTrip panics if the q= parameter contains any configured
// substring; otherwise rebuilds the request to the configured
// targetURL and dispatches it via http.DefaultTransport.
func (p *panicOnQueryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	q := req.URL.Query().Get("q")
	for _, sub := range p.panicQuerySubstrings {
		if strings.Contains(q, sub) {
			atomic.AddInt32(&p.panicCount, 1)
			panic(fmt.Sprintf("intentional test panic: q contains %q", sub))
		}
	}
	newReq, err := http.NewRequestWithContext(req.Context(), req.Method,
		p.targetURL+req.URL.Path+"?"+req.URL.RawQuery, req.Body)
	if err != nil {
		return nil, err
	}
	if req.Header != nil {
		newReq.Header = req.Header.Clone()
	}
	return http.DefaultTransport.RoundTrip(newReq)
}

// TestOrchestrator_PerFieldWorkerIsolation (Phase 12) is the
// per-field worker isolation invariant. The orchestrator runs each
// field's fetch in its own goroutine inside a sync.WaitGroup. If
// one goroutine panics — for example, because a hostile upstream
// returned a malformed response, or a SearXNG integration added a
// nil-deref path — the panic MUST NOT:
//
//  1. crash the orchestrator's process (the test process survives),
//  2. kill the other field goroutines (they must complete normally),
//  3. mark the whole run as failed (sibling fields still produce
//     items, the run is still useful to the user).
//
// The test seeds 3 fields. Field-A's keyword contains "panicfield"
// which the transport panics on. Fields B and C receive normal,
// valid responses. After Run() returns, we assert:
//   - The orchestrator's Run did not error out (the panic was
//     absorbed, not propagated to the caller).
//   - The run was marked "completed" (not "failed" / "rejected").
//   - Field A's slot in the per-field results is empty (the panic
//     swallowed its work).
//   - Field B and Field C each produced their expected item, and
//     the items are not cross-contaminated.
//   - The panic count in the transport is exactly 1 (only Field A
//     panicked; the others never reached the hostile keyword).
//
// If the orchestrator has no recover() in the worker goroutine,
// this test FAILS — the panic propagates out of the test goroutine
// and Go reports it as a fatal runtime error. That is the point:
// the test encodes the invariant the production code has to
// satisfy.
func TestOrchestrator_PerFieldWorkerIsolation(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_isolation.db")

	st, err := store.NewStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer st.Close()

	profMgr := profile.NewManager(st, profile.Config{MaxFields: 10})
	p, err := profMgr.GetOrCreateDefaultProfile()
	if err != nil {
		t.Fatalf("failed to create default profile: %v", err)
	}

	// Field-A's keyword contains the panic-trigger substring.
	// Fields B and C have plain keywords that the happy server
	// recognises and renders distinct responses for.
	if _, err := profMgr.AddField(p.ID, "Field-A-Doomed", "panicfield-kw", 1, true); err != nil {
		t.Fatalf("add field A: %v", err)
	}
	if _, err := profMgr.AddField(p.ID, "Field-B-Safe", "bkeywords", 2, true); err != nil {
		t.Fatalf("add field B: %v", err)
	}
	if _, err := profMgr.AddField(p.ID, "Field-C-Safe", "ckeywords", 3, true); err != nil {
		t.Fatalf("add field C: %v", err)
	}

	// Happy-path server: returns one article whose title/desc
	// echoes the keyword it was queried with. This lets the test
	// distinguish "Field B got its own items" from "Field B got
	// Field C's items by mistake".
	happyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		// The query is e.g. `(bkeywords OR ...) when:1d` — pull
		// the first token (the keyword or OR-group).
		kw := strings.TrimSpace(strings.SplitN(q, " ", 2)[0])
		kw = strings.Trim(kw, `"`)
		kw = strings.Trim(kw, `()`)

		var title, desc string
		switch kw {
		case "bkeywords":
			title = "B headline"
			desc = "B snippet"
		case "ckeywords":
			title = "C headline"
			desc = "C snippet"
		default:
			title = "Generic headline"
			desc = "Generic snippet"
		}
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0"><channel><title>mock</title><link>x</link>
<item><title>%s</title><link>https://%s.example/x</link><pubDate>Sun, 02 Aug 2026 01:00:00 GMT</pubDate><description>%s</description><source url="https://%s.example">src</source></item>
</channel></rss>`, title, kw, desc, kw)
	}))
	defer happyServer.Close()

	pt := &panicOnQueryTransport{
		panicQuerySubstrings: []string{"panicfield"},
		targetURL:            happyServer.URL,
	}
	httpClient := &http.Client{
		Timeout:   5 * time.Second,
		Transport: pt,
	}

	cfg := &config.Config{
		News: &config.NewsConfig{
			DefaultWindow:       "24h",
			ArticlesPerField:    5,
			MinArticlesBackfill: 0, // we don't care about backfill for this test
			MaxFields:           10,
			MaxArticlesPerField: 5,
		},
		Quality: &config.QualityConfig{
			MaxExtraCallsPerRun: 10,
		},
	}

	orch := NewOrchestrator(st, profMgr, nil, nil, cfg)
	orch.httpClient = httpClient

	win := ParseRecencyWindow("24h", "24h")

	// The actual test: this MUST NOT panic. If the orchestrator
	// lacks recover() in its worker goroutine, this call will
	// crash the test process.
	run, results, err := orch.RunWithOptions(context.Background(), win, p.ID, "")
	if err != nil {
		t.Fatalf("RunWithOptions returned error (expected nil — isolation invariant says a single field's panic must NOT fail the run): %v", err)
	}
	if run == nil {
		t.Fatal("expected non-nil run even after a field panic")
	}
	if run.Status != "completed" {
		t.Errorf("expected run status=completed (sibling fields succeeded), got %q", run.Status)
	}

	// Panic count: only Field A panicked, exactly once. If the
	// transport panicked more than once, the test is broken
	// (e.g. backfill retried the RSS URL).
	if got := atomic.LoadInt32(&pt.panicCount); got != 1 {
		t.Errorf("expected exactly 1 panic (Field A only), got %d", got)
	}

	// 3 fields in the results.
	if len(results) != 3 {
		t.Fatalf("expected 3 field results, got %d", len(results))
	}

	// Index results by field name.
	resultsByName := map[string]FetchedFieldNews{}
	for _, r := range results {
		resultsByName[r.Field.FieldName] = r
	}

	// Field A's slot is empty (panic swallowed its work).
	fieldA := resultsByName["Field-A-Doomed"]
	if len(fieldA.Items) != 0 {
		t.Errorf("Field A should have 0 items (it panicked), got %d: %+v", len(fieldA.Items), fieldA.Items)
	}

	// Field B got its OWN item, not Field C's.
	fieldB := resultsByName["Field-B-Safe"]
	if len(fieldB.Items) != 1 {
		t.Fatalf("Field B should have 1 item, got %d", len(fieldB.Items))
	}
	if !strings.Contains(fieldB.Items[0].Title, "B headline") {
		t.Errorf("Field B item should be its own (B headline), got %q", fieldB.Items[0].Title)
	}

	// Field C also got its OWN item.
	fieldC := resultsByName["Field-C-Safe"]
	if len(fieldC.Items) != 1 {
		t.Fatalf("Field C should have 1 item, got %d", len(fieldC.Items))
	}
	if !strings.Contains(fieldC.Items[0].Title, "C headline") {
		t.Errorf("Field C item should be its own (C headline), got %q", fieldC.Items[0].Title)
	}

	// Cross-contamination check: Field B's URL must not equal
	// Field C's URL.
	if fieldB.Items[0].URL == fieldC.Items[0].URL {
		t.Errorf("Field B and Field C share the same item URL — isolation broken: %s", fieldB.Items[0].URL)
	}
}
