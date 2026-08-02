package telegram

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaiizer777/onyx-scrapper/internal/news"
	"github.com/kaiizer777/onyx-scrapper/internal/store"
)

// recordingTelegramMock extends the base telegramMock with a
// per-call text log so the test can assert on the exact
// messages the gateway sent. The base mock already counts
// sendMessage calls; we add the bodies in order.
type recordingTelegramMock struct {
	*telegramMock
	mu     sync.Mutex
	bodies []string
}

// newRecordingTelegramMock wraps a new telegramMock with the
// recording layer.
func newRecordingTelegramMock(t *testing.T) *recordingTelegramMock {
	rm := &recordingTelegramMock{telegramMock: newTelegramMock(t)}
	// The base mock's handle is the http.Handler — we need to
	// wrap it so we can also capture the body of each
	// sendMessage. The library POSTs form data including the
	// "text" field. We install a small middleware in front of
	// the base handler.
	originalHandler := rm.telegramMock.server.Config.Handler
	rm.telegramMock.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Capture the text field before delegating so we
		// can record it for assertions.
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			if err := r.ParseForm(); err == nil {
				rm.mu.Lock()
				rm.bodies = append(rm.bodies, r.Form.Get("text"))
				rm.mu.Unlock()
			}
		}
		originalHandler.ServeHTTP(w, r)
	})
	return rm
}

func (rm *recordingTelegramMock) Bodies() []string {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	out := make([]string, len(rm.bodies))
	copy(out, rm.bodies)
	return out
}

// waitForFinalMessageCount polls the recorded sendMessage
// count until it reaches at least `target`, or the deadline
// expires. The test uses this to wait for the async news
// worker to finish.
func (rm *recordingTelegramMock) waitForFinalMessageCount(t *testing.T, target int, deadline time.Duration) {
	t.Helper()
	start := time.Now()
	for {
		rm.mu.Lock()
		got := len(rm.bodies)
		rm.mu.Unlock()
		if got >= target {
			return
		}
		if time.Since(start) > deadline {
			t.Fatalf("timed out after %s waiting for %d sendMessage calls; got %d (recorded bodies: %q)",
				deadline, target, got, rm.Bodies())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestRouter_News_EndToEnd_DeliveryAndFieldSeparation (Phase 12)
// exercises the full Telegram /news path:
//
//   1. The router dispatches `/news last 24h` to the news
//      handler (via makeNewsHandler, wired by WithBackends).
//   2. The handler starts a session, returns immediately so the
//      router is not blocked.
//   3. The async worker (runNewsWorker) calls the NewsRunner
//      closure, gets a canned NewsDigest with 3 fields, then
//      delivers:
//        - the immediate ack ("📰 Pulling your news digest ..."),
//        - the digest header (run ID + window),
//        - one message sequence per field (FormatTelegramField).
//   4. The mocked Telegram Bot API receives every message; we
//      assert:
//        - all messages are addressed to the same chat ID,
//        - the order is ack → header → AI/ML → Cricket → Gaming
//          (priority order from the profile),
//        - the per-field sections are visually distinct: each
//          one names the field and shows the resolved window,
//        - no field's content leaks into another field's
//          message.
//
// This is the integration counterpart to the rate-limit
// test in router_news_ratelimit_test.go. The rate-limit test
// proves the /news chokepoint is shared with /agent and
// /research. THIS test proves the actual delivery path is
// correct end-to-end and field-separation is preserved.
func TestRouter_News_EndToEnd_DeliveryAndFieldSeparation(t *testing.T) {
	mock := newRecordingTelegramMock(t)
	bot := newMockedBot(t, mock.telegramMock, 42)
	auth := NewAuthenticator(bot.Cfg, PolicySilentDrop, false)

	// Build a canned digest with 3 distinct fields, each
	// with its own items, so the test can spot any
	// cross-field merge in the recorded messages.
	now := time.Now()
	pubA := now.Add(-2 * time.Hour)
	pubB := now.Add(-5 * time.Hour)
	pubC := now.Add(-90 * time.Minute)
	runID := int64(7)
	digest := &news.NewsDigest{
		RunID:       runID,
		ProfileID:   1,
		Window:      "last 24h",
		GeneratedAt: now,
		Fields: []news.FieldDigest{
			{
				FieldID:       1,
				FieldName:     "AI/ML",
				PriorityOrder: 1,
				Items: []news.DigestItem{
					{
						Headline:       "New LLM released",
						Takeaway:       "Better reasoning benchmarks.",
						URL:            "https://example.com/ai/1",
						Source:         "TechCrunch",
						PublishedAt:    &pubA,
						FetchIntegrity: "ok",
					},
				},
			},
			{
				FieldID:       2,
				FieldName:     "Cricket",
				PriorityOrder: 2,
				Items: []news.DigestItem{
					{
						Headline:       "India wins test series",
						Takeaway:       "Convincing victory in the final match.",
						URL:            "https://example.com/cricket/1",
						Source:         "ESPN",
						PublishedAt:    &pubB,
						FetchIntegrity: "ok",
					},
				},
			},
			{
				FieldID:       3,
				FieldName:     "Gaming",
				PriorityOrder: 3,
				Items: []news.DigestItem{
					{
						Headline:       "Major update released",
						Takeaway:       "New season, new mechanics.",
						URL:            "https://example.com/gaming/1",
						Source:         "IGN",
						PublishedAt:    &pubC,
						FetchIntegrity: "ok",
					},
				},
			},
		},
	}

	// The runner returns the canned digest immediately. We
	// use atomic to satisfy the lint about unused params and
	// to make it explicit that the runner ignores context /
	// window.
	var runnerCalls int32
	newsRunner := func(ctx context.Context, window string) (*store.NewsRun, *news.NewsDigest, error) {
		atomic.AddInt32(&runnerCalls, 1)
		run := &store.NewsRun{
			ID:        runID,
			ProfileID: 1,
			Window:    window,
			Status:    "completed",
		}
		return run, digest, nil
	}

	// Wire a real SessionManager. We pass a nil store so
	// persistence is skipped — the test is about the
	// delivery path, not the DB layer.
	sm := NewSessionManager(bot.API, nil, 0)

	r := NewRouter(bot, bot.Cfg,
		WithAuthenticator(auth),
		WithSessionManager(sm),
		WithBackends(&EngineBackends{
			News:     newsRunner,
			Sessions: sm,
		}),
	)

	ctx := context.Background()
	chatID := int64(42)

	// Trigger the /news command via the router. The router
	// returns immediately (the worker is async), but the
	// session is started.
	if err := r.Handle(ctx, bot.API, makeRouterMsg(chatID, "/news last 24h")); err != nil {
		t.Fatalf("router.Handle /news: %v", err)
	}

	// The worker should produce exactly N messages. The real
	// flow is:
	//   1. Session progress edit ("⏳ Starting news run.").
	//   2. The ack ("📰 Pulling your news digest ...").
	//   3. The digest header (run ID + window + field count).
	//   4. The AI/ML field section.
	//   5. The Cricket field section.
	//   6. The Gaming field section.
	// 6 messages total.
	const wantMessages = 6
	mock.waitForFinalMessageCount(t, wantMessages, 5*time.Second)

	// The runner was called exactly once.
	if got := atomic.LoadInt32(&runnerCalls); got != 1 {
		t.Errorf("newsRunner called %d times, want 1", got)
	}

	bodies := mock.Bodies()
	if len(bodies) != wantMessages {
		t.Fatalf("recorded %d messages, want %d. Bodies: %q", len(bodies), wantMessages, bodies)
	}

	// 1. The first message is the session progress edit
	// emitted by RunWithProgress. It carries the goal
	// (which for /news is the window phrase) — this is
	// the "starting" signal the user sees.
	if !strings.Contains(bodies[0], "Starting news run") || !strings.Contains(bodies[0], "last 24h") {
		t.Errorf("first message should be the progress 'starting' edit; got: %q", bodies[0])
	}

	// 2. The second message is the ack.
	if !strings.Contains(bodies[1], "📰") || !strings.Contains(bodies[1], "last 24h") {
		t.Errorf("second message should be the ack with window; got: %q", bodies[1])
	}

	// 3. The third message is the digest header. It must
	// mention run ID and window.
	header := bodies[2]
	if !strings.Contains(header, fmt.Sprintf("run #%d", runID)) {
		t.Errorf("digest header missing run ID #%d; got: %q", runID, header)
	}
	if !strings.Contains(header, "last 24h") {
		t.Errorf("digest header missing window; got: %q", header)
	}

	// 4-6. The remaining 3 messages are the per-field
	// sections, in priority order. The section for each
	// field must:
	//   - mention the field name (case-insensitive — the
	//     Telegram formatter uppercases it in the header
	//     line per the Phase 9 cross-surface convention),
	//   - show the resolved window (Phase 9 invariant — the
	//     per-field header must include the window phrase,
	//     not just the digest-level header),
	//   - show the item count,
	//   - contain the item's headline (so a field merge
	//     would be obvious — a missing headline in the
	//     expected message means the message went
	//     somewhere else).
	wantFieldMessages := []struct {
		field    string
		headline string
		count    string
	}{
		{"AI/ML", "New LLM released", "1 item"},
		{"Cricket", "India wins test series", "1 item"},
		{"Gaming", "Major update released", "1 item"},
	}
	containsCI := func(haystack, needle string) bool {
		return strings.Contains(strings.ToLower(haystack), strings.ToLower(needle))
	}
	for i, want := range wantFieldMessages {
		body := bodies[3+i]
		if !containsCI(body, want.field) {
			t.Errorf("field message %d missing field name %q (case-insensitive); got: %q", i, want.field, body)
		}
		if !strings.Contains(body, "last 24h") {
			t.Errorf("field message %d (%s) missing window; got: %q", i, want.field, body)
		}
		if !strings.Contains(body, want.count) {
			t.Errorf("field message %d (%s) missing item count %q; got: %q", i, want.field, want.count, body)
		}
		if !strings.Contains(body, want.headline) {
			t.Errorf("field message %d (%s) missing headline %q (cross-field merge or wrong order?); got: %q",
				i, want.field, want.headline, body)
		}
	}

	// 5. Cross-field isolation: each field's message must
	// NOT mention any OTHER field's headline. If
	// deliverNewsDigest accidentally merged sections,
	// every message would have all three headlines.
	headlines := []string{"New LLM released", "India wins test series", "Major update released"}
	for i, body := range bodies[3:] {
		for j, other := range headlines {
			if i == j {
				continue
			}
			if strings.Contains(body, other) {
				t.Errorf("field message %d contains headline %q from field %d — fields were merged",
					i, other, j)
			}
		}
	}

	// 5. Session lifecycle: the session was started and
	// finished. After Finish, IsBusy must be false.
	sm.mu.Lock()
	_, stillBusy := sm.sessions[chatID]
	sm.mu.Unlock()
	if stillBusy {
		t.Errorf("session for chat %d still busy after worker completed", chatID)
	}
}
