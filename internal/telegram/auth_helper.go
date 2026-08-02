package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// AuthBootstrapResult is what the operator sees after the helper completes.
type AuthBootstrapResult struct {
	ChatID   int64  `json:"chat_id"`
	Username string `json:"username,omitempty"`
	Token    string // not serialized — used for in-process handoff only
}

// AuthBootstrap runs a one-shot polling session that waits for the
// FIRST incoming message from any chat, then returns the sender's chat_id
// and username. The operator can use this to bootstrap the allowlist
// without manually hunting down their chat_id via @userinfobot.
//
// It is safe to call with an already-allowlisted bot — the helper does not
// check the allowlist, it just captures the first message that arrives.
//
// timeout=0 means "wait indefinitely". The caller (CLI) wires a sensible
// default (60s) so a misconfigured token does not hang the operator.
func AuthBootstrap(ctx context.Context, token string, timeout time.Duration) (*AuthBootstrapResult, error) {
	if token == "" {
		return nil, errors.New("telegram auth-bootstrap: token is empty")
	}
	api, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		return nil, fmt.Errorf("telegram auth-bootstrap: new bot api: %w", err)
	}

	// Drop any pending updates so the helper does not pick up stale
	// messages from a previous run. We do this by requesting updates with
	// a high offset (UpdateID+1) to mark everything as read. Telegram's
	// getUpdates supports a negative timeout for "long poll" but we want
	// a one-shot read here, so we use Timeout=0.
	pending, err := api.GetUpdates(tgbotapi.UpdateConfig{Offset: 0, Timeout: 0})
	if err != nil {
		return nil, fmt.Errorf("telegram auth-bootstrap: drain pending updates: %w", err)
	}
	if len(pending) > 0 {
		// Acknowledge them so the next getUpdates starts at the head.
		last := pending[len(pending)-1].UpdateID
		_, _ = api.GetUpdates(tgbotapi.UpdateConfig{Offset: last + 1, Timeout: 0})
	}

	self, err := api.GetMe()
	if err != nil {
		return nil, fmt.Errorf("telegram auth-bootstrap: getMe (token invalid?): %w", err)
	}

	slog.Info("telegram.auth_bootstrap.waiting",
		slog.String("bot_username", self.UserName),
		slog.Duration("timeout", timeout),
	)

	// Now enter a short-poll loop waiting for the operator's first /start.
	// Timeout 25s aligns with Telegram's recommended long-poll ceiling.
	cfg := tgbotapi.UpdateConfig{Timeout: 25}
	if timeout > 0 {
		cfg.Timeout = int(timeout.Seconds())
		if cfg.Timeout > 25 {
			cfg.Timeout = 25
		}
	}

	updates := api.GetUpdatesChan(cfg)
	defer api.StopReceivingUpdates()

	// We honor ctx cancellation AND the timeout — whichever fires first.
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("telegram auth-bootstrap: timed out waiting for first message (try sending /start to @%s)", self.UserName)
		case upd, ok := <-updates:
			if !ok {
				return nil, errors.New("telegram auth-bootstrap: updates channel closed unexpectedly")
			}
			if upd.Message == nil {
				continue
			}
			res := &AuthBootstrapResult{
				ChatID: upd.Message.Chat.ID,
			}
			if upd.Message.From != nil {
				res.Username = upd.Message.From.UserName
			}
			slog.Info("telegram.auth_bootstrap.captured",
				slog.Int64("chat_id", res.ChatID),
				slog.String("username", res.Username),
			)
			return res, nil
		}
	}
}
