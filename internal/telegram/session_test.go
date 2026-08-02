package telegram

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaiizer777/onyx-scrapper/internal/store"
)

// newTestSessionManager builds a SessionManager backed by a real
// on-disk store (under t.TempDir so the test cleans up after itself).
// We use a real file rather than ":memory:" because the Phase-7
// auto-migrate path is the one we want to exercise; :memory: skips
// parts of the WAL setup that we depend on.
func newTestSessionManager(t *testing.T, maxConc int) (*SessionManager, *store.Store) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "session_test.db")
	st, err := store.NewStore(dbPath)
	if err != nil {
		t.Fatalf("store.NewStore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return NewSessionManager(nil, st, maxConc), st
}

// ----- SessionManager unit tests -----

func TestSessionManager_Start_PersistsAndTracksRow(t *testing.T) {
	sm, st := newTestSessionManager(t, 0)

	sess, err := sm.Start(context.Background(), 1001, "agent", "summarize the news", 0)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if sess == nil {
		t.Fatal("Start returned nil session")
	}
	if sess.RunType != "agent" || sess.Goal != "summarize the news" {
		t.Errorf("session fields not echoed back: %+v", sess)
	}
	if sm.ActiveCount() != 1 {
		t.Errorf("ActiveCount = %d, want 1", sm.ActiveCount())
	}
	if !sm.IsBusy(1001) {
		t.Error("IsBusy(1001) = false, want true")
	}
	if sm.IsBusy(2002) {
		t.Error("IsBusy(2002) = true, want false")
	}

	// The row must be persisted with status='pending' (Start transitions
	// it to 'running' after the ack send, but our Start has no api
	// in this test, so it stays 'pending').
	row, err := st.GetLatestTelegramSession(1001)
	if err != nil {
		t.Fatalf("GetLatestTelegramSession: %v", err)
	}
	if row == nil {
		t.Fatal("expected persisted row, got nil")
	}
	if row.RunType != "agent" || row.Goal != "summarize the news" {
		t.Errorf("row fields wrong: %+v", row)
	}
	if row.Status != "pending" && row.Status != "running" {
		t.Errorf("row status = %q, want pending or running", row.Status)
	}
	if row.RunID != nil {
		t.Errorf("row RunID = %v, want nil (back-filled later)", row.RunID)
	}
}

func TestSessionManager_Start_RejectsSecondRunOnSameChat(t *testing.T) {
	sm, _ := newTestSessionManager(t, 0)

	_, err := sm.Start(context.Background(), 1001, "agent", "first", 0)
	if err != nil {
		t.Fatalf("first Start: %v", err)
	}
	_, err = sm.Start(context.Background(), 1001, "research", "second", 0)
	if !errors.Is(err, ErrChatBusy) {
		t.Errorf("second Start err = %v, want ErrChatBusy", err)
	}
}

func TestSessionManager_Start_RespectsMaxConc(t *testing.T) {
	sm, _ := newTestSessionManager(t, 1) // global cap of 1

	_, err := sm.Start(context.Background(), 1001, "agent", "first", 0)
	if err != nil {
		t.Fatalf("first Start: %v", err)
	}
	_, err = sm.Start(context.Background(), 2002, "agent", "second", 0)
	if !errors.Is(err, ErrCapReached) {
		t.Errorf("second Start err = %v, want ErrCapReached", err)
	}
	// After the first finishes the slot frees up.
	sm.Finish(sm.sessions[1001], "completed", "ok", "")
	_, err = sm.Start(context.Background(), 2002, "agent", "second", 0)
	if err != nil {
		t.Errorf("Start after Finish failed: %v", err)
	}
}

func TestSessionManager_Start_ZeroMaxMeansUnlimited(t *testing.T) {
	sm, _ := newTestSessionManager(t, 0)
	for i := int64(0); i < 50; i++ {
		_, err := sm.Start(context.Background(), 1000+i, "agent", "x", 0)
		if err != nil {
			t.Fatalf("Start at chat %d: %v", 1000+i, err)
		}
	}
	if sm.ActiveCount() != 50 {
		t.Errorf("ActiveCount = %d, want 50", sm.ActiveCount())
	}
}

func TestSessionManager_Start_RejectsInvalidRunType(t *testing.T) {
	sm, _ := newTestSessionManager(t, 0)
	_, err := sm.Start(context.Background(), 1001, "crawl", "x", 0)
	if err == nil {
		t.Error("Start accepted invalid run type 'crawl'")
	}
}

func TestSessionManager_Finish_FreesSlotAndPersists(t *testing.T) {
	sm, st := newTestSessionManager(t, 0)
	sess, err := sm.Start(context.Background(), 1001, "agent", "x", 0)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	sm.Finish(sess, "completed", "the result", "")

	if sm.IsBusy(1001) {
		t.Error("IsBusy after Finish = true, want false")
	}
	if sm.ActiveCount() != 0 {
		t.Errorf("ActiveCount after Finish = %d, want 0", sm.ActiveCount())
	}

	row, err := st.GetLatestTelegramSession(1001)
	if err != nil {
		t.Fatalf("GetLatestTelegramSession: %v", err)
	}
	if row == nil {
		t.Fatal("expected persisted row")
	}
	if row.Status != "completed" {
		t.Errorf("row status = %q, want completed", row.Status)
	}
}

func TestSessionManager_Cancel_SignalsContext(t *testing.T) {
	sm, _ := newTestSessionManager(t, 0)
	sess, err := sm.Start(context.Background(), 1001, "research", "x", 0)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Wire the cancel func the same way the worker does.
	workerCtx, cancel := context.WithCancel(context.Background())
	sess.cancel = cancel

	var finished int32
	go func() {
		<-workerCtx.Done()
		atomic.StoreInt32(&finished, 1)
		sm.Finish(sess, "cancelled", "", "")
	}()

	if !sm.Cancel(1001, 100*time.Millisecond) {
		t.Error("Cancel returned false, expected true")
	}
	// Give the goroutine a moment to register.
	deadline := time.Now().Add(200 * time.Millisecond)
	for atomic.LoadInt32(&finished) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if atomic.LoadInt32(&finished) != 1 {
		t.Error("worker goroutine did not observe cancel")
	}
}

func TestSessionManager_Cancel_NoSessionReturnsFalse(t *testing.T) {
	sm, _ := newTestSessionManager(t, 0)
	if sm.Cancel(9999, 10*time.Millisecond) {
		t.Error("Cancel on empty chat returned true, want false")
	}
}

func TestSessionManager_Progress_PersistsLastStep(t *testing.T) {
	sm, st := newTestSessionManager(t, 0)
	sess, err := sm.Start(context.Background(), 1001, "agent", "x", 0)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	sm.Progress(sess, 3, "web_search")
	sm.Progress(sess, 7, "navigate")

	row, err := st.GetLatestTelegramSession(1001)
	if err != nil {
		t.Fatalf("GetLatestTelegramSession: %v", err)
	}
	if row.LastStep != 7 {
		t.Errorf("row.LastStep = %d, want 7", row.LastStep)
	}
	if row.LastAction != "navigate" {
		t.Errorf("row.LastAction = %q, want navigate", row.LastAction)
	}
}

func TestSessionManager_RunWithProgress_PropagatesCancel(t *testing.T) {
	sm, _ := newTestSessionManager(t, 0)
	sess, _ := sm.Start(context.Background(), 1001, "agent", "x", 0)

	// Wire a cancel func the way the real workers do (the
	// SessionManager.Cancel API invokes sess.cancel, which the
	// worker must populate).
	workerCtx, cancelWorker := context.WithCancel(context.Background())
	sess.cancel = cancelWorker

	// Start the worker; the call is long enough that we can cancel
	// it from another goroutine.
	workerDone := make(chan struct{})
	var (
		gotStatus string
		gotErr    error
	)
	go func() {
		status, _, err := sm.RunWithProgress(workerCtx, sess, 50*time.Millisecond, func(ctx context.Context) (string, string, error) {
			<-ctx.Done()
			return "cancelled", "", ctx.Err()
		})
		gotStatus = status
		gotErr = err
		sm.Finish(sess, status, "", "")
		close(workerDone)
	}()

	// Cancel after a beat.
	time.Sleep(20 * time.Millisecond)
	if !sm.Cancel(1001, 100*time.Millisecond) {
		t.Error("Cancel returned false")
	}
	<-workerDone

	if gotStatus != "cancelled" {
		t.Errorf("status = %q, want cancelled", gotStatus)
	}
	if gotErr != nil {
		// RunWithProgress returns nil error for cancellation (the work.md
		// requires a clean status flip, not a propagated error).
		t.Errorf("err = %v, want nil (cancelled should be a clean status)", gotErr)
	}
}

func TestSessionManager_RunWithProgress_PanicRecoveryReturnsFailed(t *testing.T) {
	sm, _ := newTestSessionManager(t, 0)
	sess, _ := sm.Start(context.Background(), 1001, "agent", "x", 0)

	status, _, err := sm.RunWithProgress(context.Background(), sess, 50*time.Millisecond, func(ctx context.Context) (string, string, error) {
		panic("kaboom")
	})
	if status != "failed" {
		t.Errorf("status = %q, want failed", status)
	}
	if err == nil {
		t.Error("err = nil, want non-nil (panic should be wrapped)")
	}
}

func TestSessionManager_StartWithRealRunID_PersistsIt(t *testing.T) {
	sm, st := newTestSessionManager(t, 0)
	runID := int64(42)
	_, err := sm.Start(context.Background(), 1001, "agent", "x", runID)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	row, err := st.GetLatestTelegramSession(1001)
	if err != nil {
		t.Fatalf("GetLatestTelegramSession: %v", err)
	}
	if row == nil || row.RunID == nil || *row.RunID != runID {
		t.Errorf("row.RunID = %v, want pointer to %d", row.RunID, runID)
	}
}

// ----- chunkMessage unit tests (Phase 6 / Phase 8 stub) -----

func TestChunkMessage_UnderCap_ReturnsSingleChunk(t *testing.T) {
	body := "hello world"
	chunks := chunkMessage(body)
	if len(chunks) != 1 || chunks[0] != body {
		t.Errorf("chunks = %v, want [%q]", chunks, body)
	}
}

func TestChunkMessage_EmptyBody_ReturnsPlaceholder(t *testing.T) {
	chunks := chunkMessage("")
	if len(chunks) != 1 || chunks[0] != "(no result)" {
		t.Errorf("chunks = %v, want [(no result)]", chunks)
	}
}

func TestChunkMessage_LongBody_SplitsOnParagraphBoundary(t *testing.T) {
	para1 := strings.Repeat("a", 2000)
	para2 := strings.Repeat("b", 2000)
	para3 := strings.Repeat("c", 2000)
	body := para1 + "\n\n" + para2 + "\n\n" + para3
	chunks := chunkMessage(body)
	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d: %v", len(chunks), chunks)
	}
	// First chunk should be <= 4000 chars and respect a paragraph boundary.
	if len(chunks[0]) > 4000 {
		t.Errorf("chunk[0] len = %d, want <= 4000", len(chunks[0]))
	}
	// Recombining should recover the original body (modulo trimming).
	var re string
	for i, c := range chunks {
		if i > 0 {
			re += " "
		}
		re += c
	}
	if re != para1+" "+para2+" "+para3 {
		t.Errorf("recombined = %q, want original", re)
	}
}

func TestFormatProgressBody_StartingAndRunningVariants(t *testing.T) {
	start := formatProgressBody("research", "what is X", 0, "", 0, true)
	if !strings.Contains(start, "Starting research") {
		t.Errorf("starting variant missing 'Starting research': %q", start)
	}
	live := formatProgressBody("research", "what is X", 3, "web_search", 12*time.Second, false)
	if !strings.Contains(live, "step 3") {
		t.Errorf("live variant missing step: %q", live)
	}
	if !strings.Contains(live, "web_search") {
		t.Errorf("live variant missing action: %q", live)
	}
	if !strings.Contains(live, "12s") {
		t.Errorf("live variant missing elapsed: %q", live)
	}
}

// ----- Stress / race-free checks -----

func TestSessionManager_ConcurrentStartsAreSerialized(t *testing.T) {
	sm, _ := newTestSessionManager(t, 0)

	const n = 20
	var (
		successes int32
		wg        sync.WaitGroup
	)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, err := sm.Start(context.Background(), 1001, "agent", "x", 0)
			if err == nil {
				atomic.AddInt32(&successes, 1)
			}
		}(i)
	}
	wg.Wait()
	if atomic.LoadInt32(&successes) != 1 {
		t.Errorf("concurrent Starts successes = %d, want 1 (chat must be exclusive)", atomic.LoadInt32(&successes))
	}
}
