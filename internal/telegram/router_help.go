package telegram

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/kaiizer777/onyx-scrapper/internal/store"
)

// defaultHelpText is the built-in /help response. Operators can
// override it via RouterOption WithHelpText (e.g. to localize).
const defaultHelpText = `Onyx Scrapper — Telegram gateway

/start               welcome + this help
/help                show this message
/status              show the most recent run for this chat
/cancel              cancel an in-flight run

/agent <goal>        run the ReAct agent loop
/research <goal>     run the deep-research orchestrator

/fetch <url>         fetch a URL and reply with cleaned text
/extract <url> <schema>   fetch a URL and reply with structured JSON
/search <query>      run a local FTS5 / SearXNG search

Anything else (no leading slash) is sent to the default mode: agent or deep-research.`

// defaultNotImplementedText is the response engine-backed commands
// produce when they have not yet been wired by main.go. It is short
// on purpose — the user does not need a wall of text explaining
// internal milestone phasing.
const defaultNotImplementedText = "this command is recognised but its engine wiring is not enabled in this build. Restart after enabling it in onyx config / main wiring."

// handleHelp replies with the router's help text.
func (r *Router) handleHelp(ctx context.Context, bot *tgbotapi.BotAPI, msg *tgbotapi.Message, payload string) error {
	// Append the resolved default mode so the user knows what
	// plain-text input will do.
	body := r.help + fmt.Sprintf("\n\ndefault mode: %s", r.defaultMode)
	return reply(bot, msg.Chat.ID, body)
}

// handleStatus reports the most recent run for this chat. Priority:
//
//  1. If a session is in flight, show live progress (step + action + elapsed).
//  2. Else if the store is wired, show the most recent persisted row.
//  3. Else fall back to "no runs yet".
//
// This is the Phase-7 implementation; the work.md calls it out as
// "reads latest session row for the chat".
func (r *Router) handleStatus(ctx context.Context, bot *tgbotapi.BotAPI, msg *tgbotapi.Message, payload string) error {
	// 1. Live session?
	if r.sessions != nil {
		if sess := r.sessions.ActiveSession(msg.Chat.ID); sess != nil {
			sess.mu.Lock()
			step := sess.lastStep
			action := sess.lastAction
			started := sess.startedAt
			runID := sess.RunID
			runType := sess.RunType
			goal := sess.Goal
			sess.mu.Unlock()
			elapsed := time.Since(started).Round(time.Second)
			actionStr := ""
			if action != "" {
				actionStr = fmt.Sprintf("\nlast action: %s", action)
			}
			return reply(bot, msg.Chat.ID, fmt.Sprintf(
				"⏳ %s run in flight (id #%d, session #%d)\nelapsed: %s\nstep: %d%s\ngoal: %s",
				runType, runID, sess.SessionID, elapsed, step, actionStr, truncate(goal, 200),
			))
		}
	}

	// 2. Persisted latest row?
	if r.store != nil {
		row, err := r.store.GetLatestTelegramSession(msg.Chat.ID)
		if err != nil {
			slog.Warn("telegram.status.persisted_lookup_failed",
				slog.Int64("chat_id", msg.Chat.ID),
				slog.String("error", err.Error()),
			)
		}
		if row != nil {
			return reply(bot, msg.Chat.ID, formatPersistedSession(row))
		}
	}

	// 3. Fallback — no live, no persisted.
	return reply(bot, msg.Chat.ID, "no runs yet on this chat. try /agent <goal> or /research <goal>.")
}

// formatPersistedSession renders the most recent persisted row. Kept
// terse so /status never blows past the chunker even on long goals.
func formatPersistedSession(row *store.TelegramSession) string {
	if row == nil {
		return "no runs yet on this chat."
	}
	updatedAgo := time.Since(row.UpdatedAt).Round(time.Second)
	lastStepStr := ""
	if row.LastStep > 0 {
		lastStepStr = fmt.Sprintf("\nlast step: %d (%s)", row.LastStep, truncate(row.LastAction, 80))
	}
	return fmt.Sprintf(
		"last %s run: #%d\nstatus: %s\nupdated %s ago%s\ngoal: %s",
		row.RunType, row.RunID, row.Status, updatedAgo, lastStepStr, truncate(row.Goal, 200),
	)
}

// handleCancel signals the in-flight session's context.CancelFunc.
// No live session => "nothing to cancel". Live session but the worker
// has already finished => we surface the terminal status. Live and
// running => we cancel and report "cancellation requested".
func (r *Router) handleCancel(ctx context.Context, bot *tgbotapi.BotAPI, msg *tgbotapi.Message, payload string) error {
	if r.sessions == nil {
		return reply(bot, msg.Chat.ID, "cancel: session manager not wired in this build.")
	}
	if !r.sessions.IsBusy(msg.Chat.ID) {
		// Either no live session, or the live session has already
		// finished and been removed from the table. Try the
		// persisted row so the user knows the *last* outcome.
		if r.store != nil {
			row, err := r.store.GetLatestTelegramSession(msg.Chat.ID)
			if err != nil {
				slog.Warn("telegram.cancel.persisted_lookup_failed",
					slog.Int64("chat_id", msg.Chat.ID),
					slog.String("error", err.Error()),
				)
			}
			if row != nil {
				return reply(bot, msg.Chat.ID, fmt.Sprintf(
					"no in-flight run to cancel.\nlast %s run #%d status: %s",
					row.RunType, row.RunID, row.Status,
				))
			}
		}
		return reply(bot, msg.Chat.ID, "no in-flight run to cancel.")
	}
	cancelled := r.sessions.Cancel(msg.Chat.ID, 2*time.Second)
	if !cancelled {
		return reply(bot, msg.Chat.ID, "could not deliver cancel signal — try again.")
	}
	return reply(bot, msg.Chat.ID, "cancellation requested — the worker will report its final status shortly.")
}

// payloadRequired is a helper for engine-backed commands that need a
// non-empty payload (e.g. /agent without a goal). It returns true and
// replies with a usage hint when the payload is empty. We centralize
// it here so the per-engine handlers (wired in main.go in Phase 10)
// can stay short.
func payloadRequired(bot *tgbotapi.BotAPI, chatID int64, verb, payload, usage string) bool {
	if strings.TrimSpace(payload) != "" {
		return false
	}
	_ = reply(bot, chatID, fmt.Sprintf("usage: /%s %s", verb, usage))
	return true
}
