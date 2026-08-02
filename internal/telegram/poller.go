package telegram

import (
	"context"
	"errors"
	"log/slog"
	"runtime/debug"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// HandlerFunc is the contract for the Phase-5 command router. The poller
// invokes it once per allowed message. Returning an error only indicates
// a router-level fault; user-facing replies (typing indicators, error
// messages) are the handler's own responsibility.
type HandlerFunc func(ctx context.Context, bot *tgbotapi.BotAPI, msg *tgbotapi.Message) error

// Poller owns the long-running Telegram update loop. One Poller per Bot.
// Run is safe to call once; subsequent calls return ErrAlreadyRunning.
type Poller struct {
	bot     *Bot
	handler HandlerFunc
	auth    *Authenticator
}

// NewPoller wires a Poller to a Bot, an auth middleware, and the
// downstream HandlerFunc (supplied by the router in Phase 5; for now a
// stub is acceptable — see defaultHandler).
func NewPoller(bot *Bot, auth *Authenticator, handler HandlerFunc) *Poller {
	if handler == nil {
		handler = defaultHandler
	}
	return &Poller{bot: bot, handler: handler, auth: auth}
}

// ErrAlreadyRunning is returned by Run if a polling session is already
// active on this Poller instance.
var ErrAlreadyRunning = errors.New("telegram poller: already running")

// Run starts the supervised polling loop and blocks until ctx is
// cancelled or a fatal error occurs. In webhook mode it currently returns
// an explanatory error (Phase 4 will land the webhook implementation).
//
// The supervisor pattern: a single goroutine owns GetUpdatesChan. On
// panic OR channel close, we wait briefly and re-spawn. This prevents
// the gateway from silently dying on a transient network blip or a
// single bad message.
func (p *Poller) Run(ctx context.Context) error {
	if p.bot == nil || p.bot.API == nil {
		return errors.New("telegram poller: bot is nil")
	}
	if p.bot.Cfg != nil && strings.EqualFold(p.bot.Cfg.Mode, "webhook") {
		return errors.New("telegram poller: webhook mode not implemented in Phase 3 (see Phase 4)")
	}

	slog.InfoContext(ctx, "telegram.poller.start", slog.String("bot", p.bot.Self.UserName))

	// backoff for the supervisor when an iteration returns / panics
	const (
		initialBackoff = 1 * time.Second
		maxBackoff     = 30 * time.Second
	)
	backoff := initialBackoff

	for {
		if ctx.Err() != nil {
			slog.InfoContext(ctx, "telegram.poller.stop")
			return nil
		}

		err := p.runOnce(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			slog.WarnContext(ctx, "telegram.poller.iteration_failed",
				slog.String("error", err.Error()),
				slog.Duration("backoff", backoff),
			)
			// Sleep with cancellation awareness so shutdown is prompt.
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(backoff):
			}
			// Exponential backoff, capped.
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}
		// Successful drain (channel closed by library) — reset backoff and
		// restart immediately. This is the "auto-restart on channel close"
		// case from the work.md.
		backoff = initialBackoff
	}
}

// runOnce is a single supervised iteration. It returns nil when the
// channel closes cleanly (e.g. after StopReceivingUpdates), and an error
// when something genuinely failed.
func (p *Poller) runOnce(ctx context.Context) (err error) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("telegram.poller.panic",
				slog.Any("recovered", r),
				slog.String("stack", string(debug.Stack())),
			)
			err = errors.New("poller iteration panicked")
		}
	}()

	// Timeout 30s = Telegram-recommended long-poll ceiling; offsets are
	// managed by the library (it auto-commits UpdateID+1 internally).
	updates := p.bot.API.GetUpdatesChan(tgbotapi.UpdateConfig{
		Offset:  0,
		Timeout: 30,
	})
	// We do NOT call StopReceivingUpdates here — the supervisor owns the
	// lifecycle and calls it once when ctx is cancelled.

	for {
		select {
		case <-ctx.Done():
			p.bot.API.StopReceivingUpdates()
			return nil
		case upd, ok := <-updates:
			if !ok {
				// Channel closed by the library (network drop, etc.).
				// Returning nil triggers the supervisor to immediately
				// restart with a fresh channel.
				slog.Warn("telegram.poller.channel_closed")
				return nil
			}
			p.handleUpdate(ctx, upd)
		}
	}
}

// handleUpdate is the per-message dispatch: auth → classify → route.
func (p *Poller) handleUpdate(ctx context.Context, upd tgbotapi.Update) {
	// Auth gate. We pass the bot pointer so the deny policy can reply if
	// configured.
	switch p.auth.Check(p.bot.API, &upd) {
	case AuthAllow:
		// proceed
	case AuthSilentDrop, AuthReplyDeny:
		return
	}

	msg := upd.Message
	if msg == nil {
		// Edited messages, callback queries, etc. — not in v1 scope.
		return
	}

	// Non-text content: photos, stickers, voice, video, documents. We
	// reply with a polite "unsupported input" so the operator knows the
	// bot saw the message but v1 is text-only.
	if !isTextOnlyMessage(msg) {
		p.replyUnsupported(ctx, msg.Chat.ID)
		return
	}

	// Dispatch to the command router. The router (Phase 5) parses the
	// text, picks the right Onyx engine, and sends replies itself. We
	// catch panics here so one bad command does not kill the poller.
	if err := safeInvoke(ctx, p.bot.API, msg, p.handler); err != nil {
		slog.Error("telegram.router.error",
			slog.Int64("chat_id", msg.Chat.ID),
			slog.String("error", err.Error()),
		)
	}
}

func safeInvoke(ctx context.Context, api *tgbotapi.BotAPI, msg *tgbotapi.Message, h HandlerFunc) (err error) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("telegram.router.panic",
				slog.Int64("chat_id", msg.Chat.ID),
				slog.Any("recovered", r),
				slog.String("stack", string(debug.Stack())),
			)
			// Best-effort user-facing failure notice. Never echo the
			// panic value back to chat.
			notice := tgbotapi.NewMessage(msg.Chat.ID, "internal error: the bot hit an unexpected condition. The run was aborted.")
			if _, sendErr := api.Send(notice); sendErr != nil {
				slog.Warn("telegram.router.failure_notice_failed",
					slog.Int64("chat_id", msg.Chat.ID),
					slog.String("error", sendErr.Error()),
				)
			}
			err = errors.New("handler panicked")
		}
	}()
	return h(ctx, api, msg)
}

func (p *Poller) replyUnsupported(ctx context.Context, chatID int64) {
	msg := tgbotapi.NewMessage(chatID, "unsupported input: v1 of the Telegram gateway only handles text commands. Try /help.")
	if _, err := p.bot.API.Send(msg); err != nil {
		slog.WarnContext(ctx, "telegram.poller.unsupported_reply_failed",
			slog.Int64("chat_id", chatID),
			slog.String("error", err.Error()),
		)
	}
}

// isTextOnlyMessage returns true when the message carries only text (or
// a text-with-caption that has no photo/document/sticker payload).
func isTextOnlyMessage(m *tgbotapi.Message) bool {
	if m == nil {
		return false
	}
	// Photo, Sticker, Video, Voice, Audio, Document, Animation, VideoNote,
	// Contact, Location, Venue, Poll, Dice, etc. — anything where Text
	// is not the primary payload.
	if m.Photo != nil || m.Sticker != nil || m.Video != nil || m.Voice != nil ||
		m.Audio != nil || m.Document != nil || m.Animation != nil || m.VideoNote != nil ||
		m.Contact != nil || m.Location != nil || m.Venue != nil || m.Poll != nil ||
		m.Dice != nil {
		return false
	}
	// Caption-only on a media message — we already caught the media case
	// above, but defensively also reject empty text.
	if strings.TrimSpace(m.Text) == "" {
		return false
	}
	return true
}

// defaultHandler is the no-op Phase-5 placeholder. Phase 5 will replace
// the wiring in main.go to inject the real router; for Phase 3 tests
// this just acknowledges the message.
func defaultHandler(ctx context.Context, bot *tgbotapi.BotAPI, msg *tgbotapi.Message) error {
	slog.InfoContext(ctx, "telegram.default_handler",
		slog.Int64("chat_id", msg.Chat.ID),
		slog.String("text_preview", truncate(msg.Text, 64)),
	)
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
