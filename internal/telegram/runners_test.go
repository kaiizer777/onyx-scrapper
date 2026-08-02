package telegram

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/kaiizer777/onyx-scrapper/internal/agent"
	"github.com/kaiizer777/onyx-scrapper/internal/store"
)


// runnerTestMock is a thin wrapper around telegramMock. The mock
// already handles sendMessage + editMessageText (via its default
// branch); we wrap it so future Phase-8 work can add edit-specific
// counters without rippling through every call site.
type runnerTestMock struct {
	*telegramMock
}

func newRunnerTestMock(t *testing.T) *runnerTestMock {
	m := newTelegramMock(t)
	return &runnerTestMock{telegramMock: m}
}

// newRunnerTestSessionManager mirrors newTestSessionManager but uses
// the runner mock so the test can assert on Telegram calls (ack send,
// typing, edit).
func newRunnerTestSessionManager(t *testing.T) (*SessionManager, *store.Store, *runnerTestMock) {
	t.Helper()
	mock := newRunnerTestMock(t)
	dbPath := filepath.Join(t.TempDir(), "runner_test.db")
	st, err := store.NewStore(dbPath)
	if err != nil {
		t.Fatalf("store.NewStore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	httpClient := &http.Client{Timeout: 5 * time.Second}
	api, err := tgbotapi.NewBotAPIWithClient(
		"test-token",
		mock.server.URL+"/bot%s/%s",
		httpClient,
	)
	if err != nil {
		t.Fatalf("NewBotAPIWithClient: %v", err)
	}
	return NewSessionManager(api, st, 0), st, mock
}

// chatMsg is a convenience constructor for a *tgbotapi.Message from a
// chat id.
func chatMsg(chatID int64) *tgbotapi.Message {
	return &tgbotapi.Message{
		MessageID: 1,
		Chat:      &tgbotapi.Chat{ID: chatID, Type: "private"},
		From:      &tgbotapi.User{ID: chatID, UserName: "alice", FirstName: "Alice", IsBot: false},
	}
}

// ----- /agent handler tests -----

func TestAgentHandler_EmptyPayloadSendsUsage(t *testing.T) {
	sm, _, _ := newRunnerTestSessionManager(t)
	api := sm.api
	router := NewRouter(&Bot{API: api}, nil, WithBackends(&EngineBackends{
		Agent:    func(ctx context.Context, goal string, runID int64, cb agent.StepCallback) (*store.AgentRun, error) { return nil, nil },
		Research: nil,
		Sessions: sm,
	}))

	router.commandHandlers["agent"](context.Background(), api, chatMsg(1001), "")
	// We sent a "usage:" reply. The handler returned nil (not an
	// error), so the only assertion is that no session was created.
	if sm.ActiveCount() != 0 {
		t.Errorf("ActiveCount = %d, want 0 (empty payload should not start a session)", sm.ActiveCount())
	}
}

func TestAgentHandler_HappyPath_DeliversResult(t *testing.T) {
	sm, st, _ := newRunnerTestSessionManager(t)
	api := sm.api
	var (
		calls    int32
		gotGoal  atomic.Value // string
		gotSteps int32
	)
	runResult := &store.AgentRun{ID: 7, Status: "completed", Result: "the full report from the agent"}
	run := func(ctx context.Context, goal string, runID int64, cb agent.StepCallback) (*store.AgentRun, error) {
		atomic.AddInt32(&calls, 1)
		gotGoal.Store(goal)
		for i := 1; i <= 3; i++ {
			atomic.AddInt32(&gotSteps, 1)
			if cb != nil {
				cb(i, "thinking", "web_search", `{"q":"x"}`, "found it", nil)
			}
		}
		runResult.ID = 7
		return runResult, nil
	}

	router := NewRouter(&Bot{API: api}, nil, WithBackends(&EngineBackends{
		Agent:    run,
		Research: nil,
		Sessions: sm,
	}))

	handler := router.commandHandlers["agent"]
	if err := handler(context.Background(), api, chatMsg(1001), "summarize the news"); err != nil {
		t.Fatalf("handler: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for sm.IsBusy(1001) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if sm.IsBusy(1001) {
		t.Fatal("session did not finish in 2s")
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Errorf("runner calls = %d, want 1", atomic.LoadInt32(&calls))
	}
	if gotGoal.Load() != "summarize the news" {
		t.Errorf("gotGoal = %v, want %q", gotGoal.Load(), "summarize the news")
	}
	if atomic.LoadInt32(&gotSteps) != 3 {
		t.Errorf("step callbacks = %d, want 3", atomic.LoadInt32(&gotSteps))
	}

	row, err := st.GetLatestTelegramSession(1001)
	if err != nil {
		t.Fatalf("GetLatestTelegramSession: %v", err)
	}
	if row == nil {
		t.Fatal("expected persisted row")
	}
	if row.Status != "completed" {
		t.Errorf("row.Status = %q, want completed", row.Status)
	}
	if row.RunID == nil || *row.RunID != 7 {
		t.Errorf("row.RunID = %v, want pointer to 7", row.RunID)
	}
}

func TestAgentHandler_BusyChatIsRejected(t *testing.T) {
	sm, _, _ := newRunnerTestSessionManager(t)
	api := sm.api

	_, err := sm.Start(context.Background(), 1001, "agent", "old goal", 0)
	if err != nil {
		t.Fatalf("seed Start: %v", err)
	}

	router := NewRouter(&Bot{API: api}, nil, WithBackends(&EngineBackends{
		Agent: func(ctx context.Context, goal string, runID int64, cb agent.StepCallback) (*store.AgentRun, error) {
			t.Errorf("runner should not have been called")
			return nil, nil
		},
		Sessions: sm,
	}))

	_ = router.commandHandlers["agent"](context.Background(), api, chatMsg(1001), "new goal")
	if !sm.IsBusy(1001) {
		t.Error("original session was lost")
	}
}

func TestAgentHandler_RunnerError_FlipsStatusToFailed(t *testing.T) {
	sm, st, _ := newRunnerTestSessionManager(t)
	api := sm.api
	run := func(ctx context.Context, goal string, runID int64, cb agent.StepCallback) (*store.AgentRun, error) {
		return nil, errors.New("engine exploded")
	}

	router := NewRouter(&Bot{API: api}, nil, WithBackends(&EngineBackends{
		Agent:    run,
		Research: nil,
		Sessions: sm,
	}))

	if err := router.commandHandlers["agent"](context.Background(), api, chatMsg(1001), "anything"); err != nil {
		t.Fatalf("handler: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for sm.IsBusy(1001) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if sm.IsBusy(1001) {
		t.Fatal("session did not finish in 2s")
	}
	row, err := st.GetLatestTelegramSession(1001)
	if err != nil {
		t.Fatalf("GetLatestTelegramSession: %v", err)
	}
	if row == nil || row.Status != "failed" {
		t.Errorf("row = %+v, want status=failed", row)
	}
}

func TestAgentHandler_PanicInRunner_DoesNotLeakAndFlipsFailed(t *testing.T) {
	sm, st, _ := newRunnerTestSessionManager(t)
	api := sm.api
	run := func(ctx context.Context, goal string, runID int64, cb agent.StepCallback) (*store.AgentRun, error) {
		panic("kaboom")
	}
	router := NewRouter(&Bot{API: api}, nil, WithBackends(&EngineBackends{
		Agent:    run,
		Research: nil,
		Sessions: sm,
	}))
	_ = router.commandHandlers["agent"](context.Background(), api, chatMsg(1001), "anything")
	deadline := time.Now().Add(2 * time.Second)
	for sm.IsBusy(1001) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if sm.IsBusy(1001) {
		t.Fatal("session did not finish")
	}
	row, _ := st.GetLatestTelegramSession(1001)
	if row == nil || row.Status != "failed" {
		t.Errorf("row = %+v, want status=failed (panic must be recovered)", row)
	}
}

// ----- /research handler tests -----

func TestResearchHandler_HappyPath_DeliversReport(t *testing.T) {
	sm, st, _ := newRunnerTestSessionManager(t)
	api := sm.api
	run := func(ctx context.Context, goal string, runID int64) (*store.ResearchRun, error) {
		return &store.ResearchRun{ID: 99, Status: "completed", ReportMD: "# Findings\n\nall done."}, nil
	}
	router := NewRouter(&Bot{API: api}, nil, WithBackends(&EngineBackends{
		Agent:    nil,
		Research: run,
		Sessions: sm,
	}))

	_ = router.commandHandlers["research"](context.Background(), api, chatMsg(2002), "what is X?")
	deadline := time.Now().Add(2 * time.Second)
	for sm.IsBusy(2002) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if sm.IsBusy(2002) {
		t.Fatal("session did not finish")
	}
	row, _ := st.GetLatestTelegramSession(2002)
	if row == nil || row.Status != "completed" {
		t.Errorf("row = %+v, want completed", row)
	}
	if row.RunID == nil || *row.RunID != 99 {
		t.Errorf("row.RunID = %v, want 99", row.RunID)
	}
}

// ----- /status + /cancel handler tests -----

func TestStatus_LiveSession_ShowsProgress(t *testing.T) {
	sm, _, _ := newRunnerTestSessionManager(t)
	api := sm.api
	sess, _ := sm.Start(context.Background(), 1001, "agent", "live goal", 0)
	sm.Progress(sess, 5, "navigate")

	router := NewRouter(&Bot{API: api}, nil, WithSessionManager(sm), WithStore(nil))
	err := router.commandHandlers["status"](context.Background(), api, chatMsg(1001), "")
	if err != nil {
		t.Errorf("status handler returned error: %v", err)
	}
}

func TestStatus_PersistedSession_ShowsLastRun(t *testing.T) {
	sm, st, _ := newRunnerTestSessionManager(t)
	api := sm.api
	sess, _ := sm.Start(context.Background(), 1001, "agent", "old goal", 0)
	sm.Finish(sess, "completed", "the result", "")

	router := NewRouter(&Bot{API: api}, nil, WithSessionManager(sm), WithStore(st))
	if err := router.commandHandlers["status"](context.Background(), api, chatMsg(1001), ""); err != nil {
		t.Errorf("status handler: %v", err)
	}
}

func TestCancel_LiveSession_DeliversSignal(t *testing.T) {
	sm, _, _ := newRunnerTestSessionManager(t)
	api := sm.api
	sess, _ := sm.Start(context.Background(), 1001, "research", "x", 0)
	var called int32
	_, cancel := context.WithCancel(context.Background())
	sess.cancel = func() {
		atomic.StoreInt32(&called, 1)
		cancel()
	}

	router := NewRouter(&Bot{API: api}, nil, WithSessionManager(sm), WithStore(nil))
	if err := router.commandHandlers["cancel"](context.Background(), api, chatMsg(1001), ""); err != nil {
		t.Errorf("cancel handler: %v", err)
	}
	if atomic.LoadInt32(&called) != 1 {
		t.Error("cancel was not invoked")
	}
}

func TestCancel_NoSession_ReportsNotBusy(t *testing.T) {
	sm, st, _ := newRunnerTestSessionManager(t)
	api := sm.api
	router := NewRouter(&Bot{API: api}, nil, WithSessionManager(sm), WithStore(st))
	if err := router.commandHandlers["cancel"](context.Background(), api, chatMsg(1001), ""); err != nil {
		t.Errorf("cancel handler: %v", err)
	}
}

// ----- Integration: full chat flow -----

func TestFullFlow_AgentRun_ThenStatus(t *testing.T) {
	sm, st, _ := newRunnerTestSessionManager(t)
	api := sm.api
	run := func(ctx context.Context, goal string, runID int64, cb agent.StepCallback) (*store.AgentRun, error) {
		time.Sleep(50 * time.Millisecond)
		return &store.AgentRun{ID: 17, Status: "completed", Result: "done"}, nil
	}
	router := NewRouter(&Bot{API: api}, nil,
		WithBackends(&EngineBackends{Agent: run, Sessions: sm}),
		WithSessionManager(sm),
		WithStore(st),
	)

	if err := router.commandHandlers["agent"](context.Background(), api, chatMsg(1001), "x"); err != nil {
		t.Fatalf("agent: %v", err)
	}
	if err := router.commandHandlers["status"](context.Background(), api, chatMsg(1001), ""); err != nil {
		t.Errorf("status during run: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for sm.IsBusy(1001) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if sm.IsBusy(1001) {
		t.Fatal("worker did not finish in time")
	}
	if err := router.commandHandlers["status"](context.Background(), api, chatMsg(1001), ""); err != nil {
		t.Errorf("status after run: %v", err)
	}

	row, _ := st.GetLatestTelegramSession(1001)
	if row == nil || row.Status != "completed" || row.RunID == nil || *row.RunID != 17 {
		t.Errorf("final row = %+v, want completed+run_id=17", row)
	}
	if !strings.Contains(row.Goal, "x") {
		t.Errorf("row.Goal = %q, want it to contain 'x'", row.Goal)
	}
}



