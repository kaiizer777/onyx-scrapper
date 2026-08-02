package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/kaiizer777/onyx-scrapper/internal/store"
)

// Router is the command dispatcher that sits between the
// transport-layer ingestion (poller or webhook) and the Onyx core
// engines (agent, deep-research, fetch, extract, search). The router
// itself contains NO engine logic — it only parses the message text
// into a (verb, payload) pair and calls the registered handler for
// that verb. Engine glue lives in cmd/onyx/main.go where the
// constructors are already imported.
//
// Router implements HandlerFunc, so it can be passed directly to
// NewPoller and NewWebhookHandler.
type Router struct {
	api     *tgbotapi.BotAPI
	cfg     *BotConfig
	help    string
	notImpl string

	// commandHandlers is the verb -> handler table. Populated by
	// NewRouter with built-in handlers and augmented by Register.
	commandHandlers map[string]commandHandler

	// defaultMode is what plain-text (non-`/command`) messages route
	// to. Set from cfg.DefaultMode, falling back to "agent". Valid
	// values: "agent", "deep-research".
	defaultMode string

	// sessions is an optional reference to the SessionManager. When
	// nil, /status and /cancel fall back to the "not wired" stubs;
	// when set, they read the live session table and consult
	// telegram_sessions for the persisted state.
	sessions *SessionManager

	// store is the optional Phase-7 persistence backend. /status
	// reads the most recent session row from here when there is no
	// in-flight session. nil-safe.
	store *store.Store

	// rateLimiter is the per-chat token bucket from Phase 9. When
	// non-nil, every Handle() call consumes a token. nil disables
	// the rate limit (useful in unit tests that don't care).
	rateLimiter *rateLimiter

	// auth is a reference to the same Authenticator the poller /
	// webhook handler use, kept here so the defense-in-depth
	// re-check in Handle() can use it without re-importing the
	// middleware.
	auth *Authenticator
}

// commandHandler is the per-verb signature. It receives the parsed
// payload (everything after the verb) and the original message. It
// owns its own reply generation — including error replies — so the
// router stays a pure dispatcher.
type commandHandler func(ctx context.Context, bot *tgbotapi.BotAPI, msg *tgbotapi.Message, payload string) error

// RouterOption configures a Router at construction.
type RouterOption func(*Router)

// WithCommandHandler registers or overrides a command handler. Useful
// for tests and for letting cmd/onyx inject real engine-backed
// implementations after NewRouter has applied its built-in defaults.
func WithCommandHandler(verb string, h commandHandler) RouterOption {
	return func(r *Router) {
		r.commandHandlers[strings.ToLower(strings.TrimSpace(verb))] = h
	}
}

// WithHelpText overrides the /help text. Operators can localize or
// brand it without forking the package.
func WithHelpText(s string) RouterOption {
	return func(r *Router) { r.help = s }
}

// WithSessionManager wires the SessionManager that /status and /cancel
// consult. Calling with nil clears it (restoring the placeholder
// behaviour). Combined with WithBackends in main.go this gives a
// single construction point for the gateway.
func WithSessionManager(sm *SessionManager) RouterOption {
	return func(r *Router) {
		r.sessions = sm
		if sm != nil {
			// Re-bind /status and /cancel to the live
			// implementations; main.go does not need to repeat
			// the registration.
			r.commandHandlers["status"] = r.handleStatus
			r.commandHandlers["cancel"] = r.handleCancel
		}
	}
}

// WithStore wires the optional Phase-7 store reference. /status reads
// the most recent persisted session row from here when no live
// session exists. nil-safe.
func WithStore(st *store.Store) RouterOption {
	return func(r *Router) { r.store = st }
}

// WithRateLimiter wires the per-chat token-bucket from Phase 9.
// Passing nil disables the rate limit. When set, every command
// routed through Handle() first consumes a token; a chat whose
// bucket is empty gets a polite "slow down" reply and the
// underlying handler is NOT invoked.
func WithRateLimiter(rl *rateLimiter) RouterOption {
	return func(r *Router) { r.rateLimiter = rl }
}

// WithAuthenticator wires the Authenticator for the Phase-9
// defense-in-depth re-check. The same Authenticator the poller
// uses is reused here so the canonical allowlist logic stays in
// one place (auth.go).
func WithAuthenticator(a *Authenticator) RouterOption {
	return func(r *Router) { r.auth = a }
}

// NewRouter builds the router with built-in handlers for the verbs
// that do not require the Onyx core engine (/start, /help, /status,
// /cancel). The engine-backed handlers (/agent, /research, /fetch,
// /extract, /search) are left as "not yet implemented" stubs — main.go
// (or tests) wires the real implementations via WithCommandHandler.
//
// bot may be nil in unit tests that only exercise ParseCommand. When
// nil, the router's command handlers cannot actually send replies;
// they will panic-recover via safeInvokeCmd, which logs and returns
// an error rather than calling the underlying tgbotapi.BotAPI.Send.
// In production, always pass the real *Bot from telegram.NewBot.
func NewRouter(bot *Bot, cfg *BotConfig, opts ...RouterOption) *Router {
	if cfg == nil {
		cfg = &BotConfig{}
	}
	var api *tgbotapi.BotAPI
	if bot != nil {
		api = bot.API
	}
	r := &Router{
		api:             api,
		cfg:             cfg,
		help:            defaultHelpText,
		notImpl:         defaultNotImplementedText,
		defaultMode:     strings.ToLower(strings.TrimSpace(cfg.DefaultMode)),
		commandHandlers: map[string]commandHandler{},
	}
	if r.defaultMode == "" {
		r.defaultMode = "agent"
	}
	if r.defaultMode != "agent" && r.defaultMode != "deep-research" && r.defaultMode != "news" {
		slog.Warn("telegram.router.invalid_default_mode",
			slog.String("got", cfg.DefaultMode),
			slog.String("fallback", "agent"),
		)
		r.defaultMode = "agent"
	}

	// Built-in (engine-free) handlers. Help/Status/Cancel are defined
	// as methods on *Router in router_help.go so they can read the
	// router's help text / default mode without globals.
	r.commandHandlers["start"] = handleStart
	r.commandHandlers["help"] = r.handleHelp
	r.commandHandlers["status"] = r.handleStatus
	r.commandHandlers["cancel"] = r.handleCancel

	// Engine-backed verbs get a default stub that tells the user
	// "this command is wired in but the engine glue is not yet
	// implemented in this build" — main.go is expected to override
	// these with WithCommandHandler. We register the stub ONLY when
	// no caller has provided a real handler.
	stub := func(ctx context.Context, bot *tgbotapi.BotAPI, msg *tgbotapi.Message, payload string) error {
		return reply(bot, msg.Chat.ID, r.notImpl)
	}
	// Both "agent" and "research" are registered so the router can
	// route to either from the slash command AND from the default
	// mode (which uses the alias "deep-research" in the config).
	r.commandHandlers["agent"] = stub
	r.commandHandlers["research"] = stub
	r.commandHandlers["fetch"] = stub
	r.commandHandlers["extract"] = stub
	r.commandHandlers["search"] = stub
	r.commandHandlers["news"] = stub

	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Handle implements HandlerFunc. It parses the message text, looks
// up the verb, and dispatches to the matching handler. Errors from
// handlers are returned so the caller's safeInvoke can log them, but
// user-visible failure messages are the handler's own responsibility.
func (r *Router) Handle(ctx context.Context, bot *tgbotapi.BotAPI, msg *tgbotapi.Message) error {
	if msg == nil {
		return errors.New("router: nil message")
	}
	// Phase 9 — rate limit + defense-in-depth allowlist re-check.
	// Both checks are cheap and run before any engine work.
	if r.rateLimiter != nil && !r.rateLimiter.Allow(msg.Chat.ID) {
		return reply(bot, msg.Chat.ID, "you're sending commands too quickly — please wait a moment and try again.")
	}
	if r.auth != nil {
		username := ""
		if msg.From != nil {
			username = msg.From.UserName
		}
		if !r.auth.IsAllowed(AllowlistIdentity{ChatID: msg.Chat.ID, Username: username}) {
			// This should not happen — the poller / webhook
			// already ran the same check. If it does, the
			// defense has caught a real drift, and we silently
			// drop so we don't reveal anything to a possibly
			// un-authorized sender.
			slog.Warn("telegram.router.defense_deny",
				slog.Int64("chat_id", msg.Chat.ID),
			)
			return nil
		}
	}
	verb, payload, ok := ParseCommand(msg.Text)
	if !ok {
		// Not a slash command -> route to the configured default mode.
		// We use the trimmed text as the payload so the agent or
		// research engine gets the full user message.
		payload = strings.TrimSpace(msg.Text)
		if payload == "" {
			return reply(bot, msg.Chat.ID, "send me something to work on, or try /help.")
		}
		// The config's default_mode uses human-readable aliases;
		// internally "deep-research" maps to the "research" slash-command
		// verb. "news" maps directly (same string). Resolve once.
		defaultVerb := r.defaultMode
		if defaultVerb == "deep-research" {
			defaultVerb = "research"
		}
		// "news" needs no alias — the handler is registered under that key.
		h, exists := r.commandHandlers[defaultVerb]
		if !exists {
			return reply(bot, msg.Chat.ID, fmt.Sprintf("default mode %q is not wired in this build", r.defaultMode))
		}
		return safeInvokeCmd(ctx, bot, msg, h, payload, defaultVerb)
	}

	verb = strings.ToLower(strings.TrimSpace(verb))
	h, exists := r.commandHandlers[verb]
	if !exists {
		// Unknown command: help text, not silence (work.md Phase 5).
		return reply(bot, msg.Chat.ID, fmt.Sprintf("unknown command /%s. %s", verb, r.help))
	}
	return safeInvokeCmd(ctx, bot, msg, h, payload, verb)
}

// Help returns the help text the router uses for /help and the
// unknown-command fallback. Exposed for tests.
func (r *Router) Help() string { return r.help }

// DefaultMode returns the resolved default mode (always lowercase,
// always one of "agent" / "deep-research").
func (r *Router) DefaultMode() string { return r.defaultMode }

// safeInvokeCmd wraps a per-command handler in panic recovery and
// user-facing failure messaging. Mirrors safeInvoke in poller.go but
// carries the verb so the log line is more useful.
func safeInvokeCmd(ctx context.Context, bot *tgbotapi.BotAPI, msg *tgbotapi.Message, h commandHandler, payload, verb string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("telegram.router.command_panic",
				slog.String("verb", verb),
				slog.Int64("chat_id", msg.Chat.ID),
				slog.Any("recovered", r),
			)
			notice := tgbotapi.NewMessage(msg.Chat.ID, "internal error: the bot hit an unexpected condition. The command was aborted.")
			if _, sendErr := bot.Send(notice); sendErr != nil {
				slog.Warn("telegram.router.failure_notice_failed",
					slog.Int64("chat_id", msg.Chat.ID),
					slog.String("error", sendErr.Error()),
				)
			}
			err = fmt.Errorf("command %q panicked", verb)
		}
	}()
	return h(ctx, bot, msg, payload)
}

// ParseCommand splits "/verb payload..." into ("verb", "payload",
// true). Returns ("", "", false) for non-slash input. The verb is
// always lowercased and stripped of any @botname suffix that Telegram
// appends in group contexts ("/help@onyx_bot" -> "help"). The payload
// is returned with surrounding whitespace trimmed.
//
// We do NOT split on spaces inside the payload: payloads are
// free-form text (research goals, agent goals, even multi-line text
// pasted in) and word-splitting there would lose information.
func ParseCommand(text string) (verb, payload string, ok bool) {
	t := strings.TrimSpace(text)
	if t == "" {
		return "", "", false
	}
	if !strings.HasPrefix(t, "/") {
		return "", "", false
	}
	// Drop the leading slash, then split on the first run of
	// whitespace.
	t = t[1:]
	// Telegram's group mention suffix: "/help@some_bot". Stop at
	// the first '@' that is preceded by non-space characters.
	if at := strings.IndexAny(t, " \t\n@"); at >= 0 {
		verb = t[:at]
		rest := t[at:]
		// If we split on '@' (no whitespace yet), continue scanning
		// for the actual whitespace boundary. The @botname suffix is
		// always followed by whitespace or end-of-string in the
		// inbound message — except in edge cases where the user
		// types "/help@some_bot extra" with a literal '@' in the
		// payload. We treat the first whitespace as the boundary.
		if ws := strings.IndexAny(rest, " \t\n"); ws >= 0 {
			payload = strings.TrimSpace(rest[ws:])
		} else {
			payload = ""
		}
	} else {
		verb = t
		payload = ""
	}
	verb = strings.ToLower(strings.TrimSpace(verb))
	if verb == "" {
		return "", "", false
	}
	return verb, payload, true
}

// reply is a tiny helper that sends a single message and returns the
// send error so handlers don't have to repeat the boilerplate.
func reply(bot *tgbotapi.BotAPI, chatID int64, text string) error {
	_, err := bot.Send(tgbotapi.NewMessage(chatID, text))
	return err
}

// ---------- built-in handlers ----------

func handleStart(ctx context.Context, bot *tgbotapi.BotAPI, msg *tgbotapi.Message, payload string) error {
	return reply(bot, msg.Chat.ID, "Welcome to Onyx. Send /help to see available commands.")
}
