package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/kaiizer777/onyx-scrapper/internal/store"
)

// Session is the in-memory representation of one in-flight Telegram run.
// One per chat; the second concurrent run on the same chat is rejected
// at Start() time. The struct is also persisted (most fields) to
// telegram_sessions so /status survives a gateway restart.
type Session struct {
	ChatID       int64
	SessionID    int64 // PK in telegram_sessions
	RunType      string
	RunID        int64
	Goal         string
	AckMessageID int

	// cancel is the context.CancelFunc for the engine's worker. It is
	// created by Start() and invoked by Cancel() (or by the manager
	// itself when the global cap is exceeded).
	cancel context.CancelFunc

	// done closes when the worker goroutine has returned. /cancel waits
	// on it so we can report "cancelled" vs. "still running" without a
	// second round-trip.
	done chan struct{}

	// finalStatus is the terminal status the worker wrote before
	// closing `done`. Empty while still running.
	finalStatus string
	finalResult string
	finalErr    string

	// mu protects progress fields shared between the engine's
	// StepCallback and the typing-ticker goroutine.
	mu          sync.Mutex
	lastStep    int
	lastAction  string
	startedAt   time.Time
	typingStop  chan struct{}  // closed to stop the typing ticker
	editingAck  sync.Mutex     // serialize EditMessageText calls
}

// SessionManager owns the per-chat Session table and enforces the
// max_concurrent_sessions cap. The manager is goroutine-safe; all
// public methods take mu internally.
type SessionManager struct {
	api     *tgbotapi.BotAPI
	store   *store.Store
	maxConc int

	mu       sync.Mutex
	sessions map[int64]*Session // keyed by ChatID
}

// NewSessionManager builds the manager. maxConc <= 0 means "no cap"
// (every chat can have one running session; per-chat limit is still 1).
// store may be nil — when nil, persistence is skipped (useful in unit
// tests that don't want to touch SQLite).
func NewSessionManager(api *tgbotapi.BotAPI, st *store.Store, maxConc int) *SessionManager {
	if maxConc < 0 {
		maxConc = 0
	}
	return &SessionManager{
		api:       api,
		store:     st,
		maxConc:   maxConc,
		sessions:  map[int64]*Session{},
	}
}

// IsBusy reports whether the chat already has an in-flight run.
// /status uses this to distinguish "your previous run is still going"
// from "your previous run is done, look at run_id N".
func (m *SessionManager) IsBusy(chatID int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.sessions[chatID]
	return ok
}

// ActiveCount returns the number of in-flight sessions across all chats.
// /status on the *gateway* (rather than a single chat) reads this.
func (m *SessionManager) ActiveCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sessions)
}

// ErrChatBusy is returned by Start when the chat already has a running
// session. The handler turns it into a polite "please wait" reply.
var ErrChatBusy = errors.New("telegram: chat already has an in-flight session")

// ErrCapReached is returned by Start when the global cap would be
// exceeded. Distinct from ErrChatBusy so the user message can be more
// specific ("server is busy") vs. ("you are busy").
var ErrCapReached = errors.New("telegram: max_concurrent_sessions cap reached")

// Start registers a brand-new session for chatID. It does NOT spawn the
// worker; the caller (the engine-backed handler) does that after Start
// returns, using the cancel/done pair. This split lets Start do the
// "is the slot free?" check, the persistence insert, and the ack
// message send in one atomic-feeling sequence from the caller's POV.
//
// runType must be "agent" or "research". runID is the engine-side row
// id (agent_runs.id or research_runs.id) the worker will create or
// resume.
func (m *SessionManager) Start(ctx context.Context, chatID int64, runType, goal string, runID int64) (*Session, error) {
	if runType != "agent" && runType != "research" && runType != "news" {
		return nil, fmt.Errorf("telegram: invalid run type %q", runType)
	}
	m.mu.Lock()
	if _, busy := m.sessions[chatID]; busy {
		m.mu.Unlock()
		return nil, ErrChatBusy
	}
	if m.maxConc > 0 && len(m.sessions) >= m.maxConc {
		m.mu.Unlock()
		return nil, ErrCapReached
	}

	// We hold the lock across the persistence insert so the chat is
	// reserved *before* the row is visible. If the insert fails we
	// release the slot; the caller will return an error to the user.
	var sessionID int64
	if m.store != nil {
		var runIDPtr *int64
		if runID > 0 {
			v := runID
			runIDPtr = &v
		}
		id, err := m.store.CreateTelegramSession(chatID, runType, runIDPtr, goal)
		if err != nil {
			m.mu.Unlock()
			return nil, fmt.Errorf("telegram: persist session: %w", err)
		}
		sessionID = id
	}

	sess := &Session{
		ChatID:     chatID,
		SessionID:  sessionID,
		RunType:    runType,
		RunID:      runID,
		Goal:       goal,
		done:       make(chan struct{}),
		startedAt:  time.Now().UTC(),
		typingStop: make(chan struct{}),
	}
	m.sessions[chatID] = sess
	m.mu.Unlock()

	// Send the ack message. We do this after the lock is released
	// because Send is a network call and we don't want to block other
	// chats from acquiring the lock.
	if err := m.sendAck(ctx, sess, runType, goal); err != nil {
		// Ack failure is non-fatal: the session is still valid, the
		// user just won't see the "Starting..." message. We log and
		// proceed.
		slog.WarnContext(ctx, "telegram.session.ack_failed",
			slog.Int64("chat_id", chatID),
			slog.String("error", err.Error()),
		)
	}

	// Mark the row as running now that ack is out (or attempted). The
	// status flip is the canonical "worker is alive" signal for /status
	// queries that hit the DB directly.
	if m.store != nil && sessionID > 0 {
		_ = m.store.UpdateTelegramSessionStatus(sessionID, "running", sess.AckMessageID, 0, "")
	}

	// Start the typing indicator. The bot is "thinking" — until the
	// worker finishes, send a ChatAction: typing every 4s.
	if m.api != nil {
		go m.runTypingTicker(sess)
	}

	return sess, nil
}

// Finish marks the session as complete, removes it from the live
// table, and persists the terminal status. Always call Finish (or
// Cancel) from the worker goroutine via defer, even on error.
func (m *SessionManager) Finish(sess *Session, status, result, errMsg string) {
	if sess == nil {
		return
	}
	sess.mu.Lock()
	sess.finalStatus = status
	sess.finalResult = result
	sess.finalErr = errMsg
	close(sess.done)
	sess.mu.Unlock()

	// Stop the typing ticker. Safe to close multiple times because
	// runTypingTicker reads with a recovery guard.
	select {
	case <-sess.typingStop:
		// already closed
	default:
		close(sess.typingStop)
	}

	m.mu.Lock()
	delete(m.sessions, sess.ChatID)
	m.mu.Unlock()

	if m.store != nil && sess.SessionID > 0 {
		if err := m.store.UpdateTelegramSessionStatus(sess.SessionID, status, sess.AckMessageID, sess.lastStep, sess.lastAction); err != nil {
			slog.Warn("telegram.session.finish_persist_failed",
				slog.Int64("session_id", sess.SessionID),
				slog.String("error", err.Error()),
			)
		}
	}
}

// Cancel signals the worker's context.CancelFunc and waits up to
// timeout for the worker to acknowledge. Returns true if cancellation
// was actually delivered (i.e. the worker was still running). When
// false, the chat had no live session — /cancel reports "nothing to
// cancel" in that case.
func (m *SessionManager) Cancel(chatID int64, timeout time.Duration) bool {
	m.mu.Lock()
	sess, ok := m.sessions[chatID]
	m.mu.Unlock()
	if !ok {
		return false
	}

	sess.cancel()

	select {
	case <-sess.done:
		return true
	case <-time.After(timeout):
		return true // cancel was delivered; worker is just slow to exit
	}
}

// ActiveSession returns the in-flight session for a chat, or nil if
// there is none. /status uses this to decide between "still running,
// here's the latest step" and "your last run completed — read the
// persisted row instead".
func (m *SessionManager) ActiveSession(chatID int64) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[chatID]
}

// Progress is the hook the engine calls from its StepCallback. It
// records the step on the Session and persists the progress fields. The
// ack-message edit is NOT done here — that is the responsibility of the
// per-session progress goroutine started by RunWithProgress (it has a
// separate ticker so chat spam is rate-limited).
func (m *SessionManager) Progress(sess *Session, step int, action string) {
	if sess == nil {
		return
	}
	sess.mu.Lock()
	sess.lastStep = step
	sess.lastAction = action
	sess.mu.Unlock()

	if m.store != nil && sess.SessionID > 0 {
		_ = m.store.UpdateTelegramSessionProgress(sess.SessionID, step, action)
	}
}

// RunWithProgress is the workhorse the engine-backed handler calls
// inside its worker goroutine. It does three things concurrently:
//
//  1. Runs `worker` to completion (the engine call).
//  2. Ticks the ack message every `editEvery` so the user sees the
//     latest step number + action in the original "Starting..." reply.
//     This is the strategy the work.md picks ("edit the ack message
//     periodically ... to avoid chat spam").
//  3. Drains the typing indicator (already running from Start()).
//
// It returns the status the worker should be recorded under
// ("completed" / "failed" / "cancelled") and the result string for the
// caller to deliver. The caller is responsible for calling Finish with
// these values.
func (m *SessionManager) RunWithProgress(ctx context.Context, sess *Session, editEvery time.Duration, worker func(ctx context.Context) (string, string, error)) (status, result string, err error) {
	if editEvery <= 0 {
		editEvery = 8 * time.Second
	}

	// Edit ticker.
	editDone := make(chan struct{})
	go func() {
		defer close(editDone)
		t := time.NewTicker(editEvery)
		defer t.Stop()
		for {
			select {
			case <-sess.typingStop:
				return
			case <-t.C:
				m.editProgressMessage(ctx, sess)
			}
		}
	}()

	// Run the worker with panic recovery — a panicking engine must NOT
	// take down the gateway. The recovery writes to outer-scope vars
	// declared below; the IIFE returns them by value.
	var workerStatus, workerResult string
	var workerErr error
	func() {
		defer func() {
			if r := recover(); r != nil {
				workerStatus = "failed"
				workerResult = ""
				workerErr = fmt.Errorf("worker panicked: %v", r)
			}
		}()
		workerStatus, workerResult, workerErr = worker(ctx)
	}()

	// Stop the edit ticker (typingStop signals both). The progress
	// message gets one final edit before the worker returns so the
	// "running" state doesn't linger.
	select {
	case <-sess.typingStop:
	default:
		close(sess.typingStop)
	}
	<-editDone

	if workerErr != nil {
		// Distinguish cancellation from real errors. The work.md
		// requires /cancel to mark the run as "cancelled" and the
		// user message to be "cancelled" — not "failed".
		if errors.Is(workerErr, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			return "cancelled", workerResult, nil
		}
		return "failed", workerResult, workerErr
	}
	return workerStatus, workerResult, nil
}

// editProgressMessage rewrites the ack message in place with the
// current step counter. Failures are silently dropped — editing a
// deleted / old message is best-effort, and the worker will overwrite
// it with the final report on completion anyway.
func (m *SessionManager) editProgressMessage(ctx context.Context, sess *Session) {
	if m.api == nil || sess == nil || sess.AckMessageID == 0 {
		return
	}
	sess.mu.Lock()
	step := sess.lastStep
	action := sess.lastAction
	startedAt := sess.startedAt
	sess.mu.Unlock()

	elapsed := time.Since(startedAt).Round(time.Second)
	body := formatProgressBody(sess.RunType, sess.Goal, step, action, elapsed, false)
	edit := tgbotapi.NewEditMessageText(sess.ChatID, sess.AckMessageID, body)
	sess.editingAck.Lock()
	defer sess.editingAck.Unlock()
	if _, err := m.api.Send(edit); err != nil {
		// "message is not modified" is a benign 4xx; don't warn.
		slog.DebugContext(ctx, "telegram.session.edit_progress_failed",
			slog.Int64("chat_id", sess.ChatID),
			slog.String("error", err.Error()),
		)
	}
}

// sendAck is the immediate "Starting..." reply sent synchronously
// before the worker goroutine spawns. This is the work.md Phase 6
// requirement: "Immediate ack message sent synchronously before
// spawning worker".
func (m *SessionManager) sendAck(ctx context.Context, sess *Session, runType, goal string) error {
	if m.api == nil {
		return nil
	}
	body := formatProgressBody(runType, goal, 0, "", 0, true)
	msg := tgbotapi.NewMessage(sess.ChatID, body)
	sent, err := m.api.Send(msg)
	if err != nil {
		return err
	}
	sess.mu.Lock()
	sess.AckMessageID = sent.MessageID
	sess.mu.Unlock()
	return nil
}

// runTypingTicker sends ChatAction: typing every 4 seconds while the
// session is alive. It is owned by the session: closing sess.typingStop
// stops it.
func (m *SessionManager) runTypingTicker(sess *Session) {
	if m.api == nil || sess == nil {
		return
	}
	// Telegram drops typing indicators after 5s, so a 4s cadence
	// keeps the "Onyx is typing..." badge alive without flooding.
	t := time.NewTicker(4 * time.Second)
	defer t.Stop()

	// Send one immediately so the badge appears without waiting 4s.
	m.sendTyping(sess)

	for {
		select {
		case <-sess.typingStop:
			return
		case <-t.C:
			m.sendTyping(sess)
		}
	}
}

func (m *SessionManager) sendTyping(sess *Session) {
	if m.api == nil || sess == nil {
		return
	}
	action := tgbotapi.NewChatAction(sess.ChatID, tgbotapi.ChatTyping)
	if _, err := m.api.Send(action); err != nil {
		slog.Debug("telegram.session.typing_send_failed",
			slog.Int64("chat_id", sess.ChatID),
			slog.String("error", err.Error()),
		)
	}
}

// formatProgressBody renders the ack / progress message. The "starting"
// variant is friendlier; the live variant is terse so a 4096-char
// ceiling never bites us even for long research sessions.
func formatProgressBody(runType, goal string, step int, action string, elapsed time.Duration, starting bool) string {
	if starting {
		switch runType {
		case "agent":
			return fmt.Sprintf("⏳ Starting agent run — this may take a few minutes.\n\ngoal: %s", truncate(goal, 280))
		case "research":
			return fmt.Sprintf("🔎 Starting research — this may take several minutes.\n\ngoal: %s", truncate(goal, 280))
		default:
			return fmt.Sprintf("⏳ Starting %s run.\n\ngoal: %s", runType, truncate(goal, 280))
		}
	}
	elapsedStr := ""
	if elapsed > 0 {
		elapsedStr = fmt.Sprintf(" (elapsed %s)", elapsed)
	}
	actionStr := ""
	if action != "" {
		actionStr = fmt.Sprintf("\nlast action: %s", action)
	}
	return fmt.Sprintf("⏳ %s running... step %d%s%s",
		runType, step, elapsedStr, actionStr,
	)
}
