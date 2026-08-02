package telegram

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/kaiizer777/onyx-scrapper/internal/agent"
	"github.com/kaiizer777/onyx-scrapper/internal/store"
)

// AgentRunner is the function signature the gateway expects for the
// /agent command. main.go supplies a closure that builds the agent
// from the loaded config (LLM client, registry) and runs it against
// the engine. We use a function type rather than a struct so the
// gateway can stay decoupled from agent/research internal construction
// (LLM client wiring, registry build, etc.) — the wiring is owned by
// cmd/onyx which already has all the dependencies in scope.
type AgentRunner func(ctx context.Context, goal string, runID int64, cb agent.StepCallback) (*store.AgentRun, error)

// ResearchRunner is the matching signature for /research. It mirrors
// the AgentRunner shape: goal, runID, then a callback. The research
// orchestrator does not currently expose a per-step callback, so the
// gateway passes nil — the run will report "running" + final
// "completed" only, with no mid-flight progress (Phase 6 accept the
// trade-off; Phase 8 can add a subquestion-level hook if needed).
type ResearchRunner func(ctx context.Context, goal string, runID int64) (*store.ResearchRun, error)



// EngineBackends bundles the runners for /agent, /research, /fetch,
// and /news plus the SessionManager that owns per-chat slots and
// persistence. main.go constructs this once and injects it via
// WithBackends. All fields are optional: a nil runner leaves the
// corresponding command as the "not wired" stub;
// SessionManager may be nil only in unit tests that don't touch the DB.
type EngineBackends struct {
	Agent    AgentRunner
	Research ResearchRunner
	Fetch    FetchRunner
	Sessions *SessionManager
}

// WithBackends wires the engine-backed command handlers (/agent,
// /research, /fetch, /news) and the chat-busy enforcement
// (/cancel, /status). It is a no-op for the verbs that are already
// handled by built-ins (/start, /help). Calling WithBackends more
// than once is safe — the last call wins.
func WithBackends(b *EngineBackends) RouterOption {
	return func(r *Router) {
		if b == nil {
			return
		}
		if b.Agent != nil {
			r.commandHandlers["agent"] = makeAgentHandler(b.Agent, b.Sessions)
		}
		if b.Research != nil {
			r.commandHandlers["research"] = makeResearchHandler(b.Research, b.Sessions)
		}
		if b.Fetch != nil {
			r.commandHandlers["fetch"] = makeFetchHandler(b.Fetch)
		}
		if b.Sessions != nil {
			// Replace the placeholder help/status/cancel handlers
			// with real implementations that consult the session
			// manager. We assign to the router's map directly so
			// later WithCommandHandler overrides still work.
			r.commandHandlers["status"] = r.handleStatus
			r.commandHandlers["cancel"] = r.handleCancel
			// handleHelp stays the same — the help text already
			// documents the live behaviour.
		}
	}
}

// chunkMessage returns a slice of message bodies, each <= 4000 chars,
// that together cover `body` verbatim. We leave a 96-char margin below
// the 4096 Telegram hard cap to absorb header overhead. The splitter
// prefers paragraph (\n\n) boundaries, then single newlines, then
// hard cuts. Empty bodies are skipped. Phase 8 will replace this with
// a smarter markdown-aware splitter + a file-attachment fallback for
// huge reports; for now this is good enough to keep /agent and
// /research from blowing up on long output.
func chunkMessage(body string) []string {
	const maxChunk = 4000
	body = strings.TrimSpace(body)
	if body == "" {
		return []string{"(no result)"}
	}
	if len(body) <= maxChunk {
		return []string{body}
	}

	var chunks []string
	remaining := body
	for len(remaining) > 0 {
		if len(remaining) <= maxChunk {
			chunks = append(chunks, remaining)
			break
		}
		// Find the last paragraph break at or before maxChunk.
		cut := maxChunk
		if idx := strings.LastIndex(remaining[:maxChunk], "\n\n"); idx > 0 {
			cut = idx
		} else if idx := strings.LastIndex(remaining[:maxChunk], "\n"); idx > 0 {
			cut = idx
		} else if idx := strings.LastIndex(remaining[:maxChunk], " "); idx > 0 {
			cut = idx
		}
		chunks = append(chunks, strings.TrimSpace(remaining[:cut]))
		remaining = strings.TrimSpace(remaining[cut:])
	}
	return chunks
}

// splitPayloadForEngine validates the payload for the engine commands.
// Returns the trimmed payload, plus a bool indicating whether the
// caller should short-circuit (true => usage hint already sent).
func splitPayloadForEngine(bot *tgbotapi.BotAPI, msg *tgbotapi.Message, verb, payload, usage string) (string, bool) {
	trimmed := strings.TrimSpace(payload)
	if trimmed == "" {
		_ = reply(bot, msg.Chat.ID, fmt.Sprintf("usage: /%s %s", verb, usage))
		return "", true
	}
	return trimmed, false
}

// makeAgentHandler builds the /agent command handler. The handler
// runs synchronously up to the ack send + session.Start, then spawns
// the worker goroutine and returns. The router's safeInvokeCmd
// recovers from panics inside the handler; the worker goroutine
// recovers internally (via SessionManager.RunWithProgress).
func makeAgentHandler(run AgentRunner, sm *SessionManager) commandHandler {
	return func(ctx context.Context, bot *tgbotapi.BotAPI, msg *tgbotapi.Message, payload string) error {
		goal, short := splitPayloadForEngine(bot, msg, "agent", payload, "<goal>")
		if short {
			return nil
		}
		if sm == nil {
			// No session manager — fall through to a polite "not wired"
			// message. This matches the Phase-5 stub behaviour so a
			// misconfigured gateway doesn't crash.
			return reply(bot, msg.Chat.ID, "agent runner is not wired in this build")
		}
		if sm.IsBusy(msg.Chat.ID) {
			return reply(bot, msg.Chat.ID, "you already have an agent / research run in flight on this chat. /cancel it first or wait for it to finish.")
		}

		// Reserve a chat slot. The session row is created with
		// run_id=NULL; the worker back-fills it via
		// store.UpdateTelegramSessionRunID once the engine has
		// allocated the engine-side row. /status during the run
		// reads from the in-memory Session, not the DB, so the
		// back-fill is a nice-to-have for /history after the run
		// completes, not a hot-path dependency.
		sess, err := sm.Start(ctx, msg.Chat.ID, "agent", goal, 0)
		if err != nil {
			if errors.Is(err, ErrChatBusy) {
				return reply(bot, msg.Chat.ID, "you already have a run in flight on this chat. /cancel it first or wait for it to finish.")
			}
			if errors.Is(err, ErrCapReached) {
				return reply(bot, msg.Chat.ID, "the gateway is at its max concurrent sessions cap. Try again in a few minutes.")
			}
			return reply(bot, msg.Chat.ID, fmt.Sprintf("could not start session: %s", err.Error()))
		}

		// Spawn the worker. We do not wait for it — the router
		// returns immediately so the user sees the ack in <1s.
		go runAgentWorker(ctx, bot, msg, sess, sm, run, goal)
		return nil
	}
}

// runAgentWorker is the goroutine body for /agent. It owns the engine
// call lifecycle: cancel func wiring, step callback, persistence link,
// final delivery.
func runAgentWorker(parentCtx context.Context, bot *tgbotapi.BotAPI, msg *tgbotapi.Message, sess *Session, sm *SessionManager, run AgentRunner, goal string) {
	// Build a cancelable context from the *manager's* lifecycle (not
	// the per-update context, which the poller cancels the moment it
	// returns from Handle — the worker must outlive that).
	workerCtx, cancel := context.WithCancel(context.Background())
	sess.cancel = cancel

	// Step callback bridges engine -> session manager.
	cb := func(stepNum int, thought, action, args, result string, err error) {
		sm.Progress(sess, stepNum, action)
	}

	status, result, err := sm.RunWithProgress(workerCtx, sess, 8*time.Second, func(ctx context.Context) (string, string, error) {
		runRow, runErr := run(ctx, goal, 0, cb)
		if runErr != nil {
			return "failed", "", runErr
		}
		if runRow == nil {
			return "failed", "", errors.New("agent runner returned nil run with no error")
		}
		// Back-fill the link in the persisted row. /status on the
		// live session already knows the id via sess.RunID; this
		// is the post-mortem path.
		sess.mu.Lock()
		sess.RunID = runRow.ID
		sess.mu.Unlock()
		if sm.store != nil && sess.SessionID > 0 {
			_ = sm.store.UpdateTelegramSessionRunID(sess.SessionID, runRow.ID)
		}
		return runRow.Status, runRow.Result, nil
	})

	// Persist the final status (Finish) and update the ack message
	// with the final result so the chat thread tells a coherent story.
	errStr := ""
	if err != nil {
		errStr = err.Error()
	}
	sm.Finish(sess, status, result, errStr)

	// Deliver the final result to the user. We chunk the body so a
	// long agent report doesn't trip the 4096-char limit.
	if err != nil {
		// User-facing failure message — never leak the raw engine
		// error verbatim; the work.md requires a "polite" message.
		body := fmt.Sprintf("❌ agent run failed: %s", shortUserError(err))
		_ = reply(bot, msg.Chat.ID, body)
		return
	}

	// Build the final message. We include the run id so the user can
	// correlate with the on-disk record. The deliverer handles
	// chunking, file fallback, and HTML rendering (Phase 8).
	header := fmt.Sprintf("✅ agent run #%d complete (status: %s)", sess.RunID, status)
	_ = newDeliverer().DeliverReport(parentCtx, bot, msg.Chat.ID, header, result, nil)
}

// makeResearchHandler is the /research sibling of makeAgentHandler. The
// orchestrator does not yet expose a per-step callback, so the progress
// edit just shows the run is alive until completion.
func makeResearchHandler(run ResearchRunner, sm *SessionManager) commandHandler {
	return func(ctx context.Context, bot *tgbotapi.BotAPI, msg *tgbotapi.Message, payload string) error {
		goal, short := splitPayloadForEngine(bot, msg, "research", payload, "<goal>")
		if short {
			return nil
		}
		if sm == nil {
			return reply(bot, msg.Chat.ID, "research runner is not wired in this build")
		}
		if sm.IsBusy(msg.Chat.ID) {
			return reply(bot, msg.Chat.ID, "you already have an agent / research run in flight on this chat. /cancel it first or wait for it to finish.")
		}

		sess, err := sm.Start(ctx, msg.Chat.ID, "research", goal, 0)
		if err != nil {
			if errors.Is(err, ErrChatBusy) {
				return reply(bot, msg.Chat.ID, "you already have a run in flight on this chat. /cancel it first or wait for it to finish.")
			}
			if errors.Is(err, ErrCapReached) {
				return reply(bot, msg.Chat.ID, "the gateway is at its max concurrent sessions cap. Try again in a few minutes.")
			}
			return reply(bot, msg.Chat.ID, fmt.Sprintf("could not start session: %s", err.Error()))
		}
		go runResearchWorker(ctx, bot, msg, sess, sm, run, goal)
		return nil
	}
}

func runResearchWorker(parentCtx context.Context, bot *tgbotapi.BotAPI, msg *tgbotapi.Message, sess *Session, sm *SessionManager, run ResearchRunner, goal string) {
	workerCtx, cancel := context.WithCancel(context.Background())
	sess.cancel = cancel

	status, result, err := sm.RunWithProgress(workerCtx, sess, 10*time.Second, func(ctx context.Context) (string, string, error) {
		runRow, runErr := run(ctx, goal, 0)
		if runErr != nil {
			return "failed", "", runErr
		}
		if runRow == nil {
			return "failed", "", errors.New("research runner returned nil run with no error")
		}
		sess.mu.Lock()
		sess.RunID = runRow.ID
		sess.mu.Unlock()
		if sm.store != nil && sess.SessionID > 0 {
			_ = sm.store.UpdateTelegramSessionRunID(sess.SessionID, runRow.ID)
		}
		return runRow.Status, runRow.ReportMD, nil
	})

	errStr := ""
	if err != nil {
		errStr = err.Error()
	}
	sm.Finish(sess, status, result, errStr)

	if err != nil {
		body := fmt.Sprintf("❌ research run failed: %s", shortUserError(err))
		_ = reply(bot, msg.Chat.ID, body)
		return
	}

	header := fmt.Sprintf("✅ research run #%d complete (status: %s)", sess.RunID, status)
	_ = newDeliverer().DeliverReport(parentCtx, bot, msg.Chat.ID, header, result, nil)
}

// shortUserError turns a raw error into a single-line, user-safe
// message. We do not want to leak stack traces, file paths, or token
// fragments to the chat.
func shortUserError(err error) string {
	if err == nil {
		return ""
	}
	// Phase 9: scrub the error through RedactToken before
	// returning, so a Telegram API error string that happened to
	// contain a token fragment (extremely rare, but possible —
	// e.g. the library echoing the bearer in a 401 message)
	// cannot leak to the user.
	s := RedactToken(err.Error())
	// Truncate anything over 240 chars so a runaway error doesn't
	// blow past the 4096 cap on its own (it can't, but be safe).
	if len(s) > 240 {
		s = s[:240] + "…"
	}
	return s
}

// FetchRunner is the function signature the gateway expects for the
// /fetch command. main.go supplies a closure that calls
// extract.Fetch. We keep the indirection so the gateway can stay
// decoupled from extract's exact signature (e.g. when the
// ScraperAPI key needs to be threaded through).
type FetchRunner func(ctx context.Context, targetURL string) (string, error)

// makeFetchHandler builds the /fetch command handler. It runs
// the URL through SanitizeURLStrict first (Phase 9 SSRF) and then
// hands the clean URL to the runner. The runner is synchronous —
// the chat shows a "fetching..." indicator (we don't have one for
// short-running commands yet, Phase 6 only covers the long runners).
func makeFetchHandler(run FetchRunner) commandHandler {
	return func(ctx context.Context, bot *tgbotapi.BotAPI, msg *tgbotapi.Message, payload string) error {
		rawURL := strings.TrimSpace(payload)
		if rawURL == "" {
			return reply(bot, msg.Chat.ID, "usage: /fetch <url>")
		}
		cleanURL := SanitizeURL(rawURL)
		if cleanURL == "" {
			return reply(bot, msg.Chat.ID, "I can't fetch that URL — only public http(s) URLs are allowed (loopback, private, and metadata addresses are blocked).")
		}
		if run == nil {
			return reply(bot, msg.Chat.ID, "fetch is not wired in this build.")
		}
		body, err := run(ctx, cleanURL)
		if err != nil {
			return reply(bot, msg.Chat.ID, fmt.Sprintf("❌ fetch failed: %s", shortUserError(err)))
		}
		// Cap the inline result so a huge page doesn't blow
		// the chunker. Anything bigger goes as a file.
		const inlineCap = 16 * 1024
		if len(body) > inlineCap {
			filename, fileBody, caption := newFormatter().formatCodeAsFile(body, "html", "fetched page")
			return newDeliverer().sendFile(ctx, bot, msg.Chat.ID, filename, fileBody, caption)
		}
		return newDeliverer().DeliverText(ctx, bot, msg.Chat.ID, fmt.Sprintf("📄 fetched %s", cleanURL), body)
	}
}


