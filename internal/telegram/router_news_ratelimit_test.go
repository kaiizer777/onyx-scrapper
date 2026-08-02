package telegram

import (
	"context"
	"sync/atomic"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// TestRouter_NewsCommand_RateLimited (Phase 11) proves that the
// `/news` Telegram command flows through the same per-chat
// rate-limiter chokepoint as `/agent` and `/research`. Specifically:
//
//   - First `/news` call: handler runs, rate-limit token consumed.
//   - Second `/news` call from the same chat within the bucket
//     window: rate-limited, the `/news` handler is NOT invoked,
//     the user gets the "slow down" reply.
//
// The rate limiter is the same one configured at the gateway
// (Phase 9). Reusing it for /news is the Phase 11 guardrail
// against a single allowlisted user spamming /news to drain the
// LLM / search budget.
func TestRouter_NewsCommand_RateLimited(t *testing.T) {
	mock := newTelegramMock(t)
	bot := newMockedBot(t, mock, 42)
	auth := NewAuthenticator(bot.Cfg, PolicySilentDrop, false)

	// Counter on the /news handler — if rate limit works, this
	// stays at 1 even after we send the second /news.
	var newsHandlerCalls int32
	newsHandler := func(ctx context.Context, b *tgbotapi.BotAPI, m *tgbotapi.Message, payload string) error {
		atomic.AddInt32(&newsHandlerCalls, 1)
		return nil
	}

	// Burst=1, refill=very-slow so the second /news is denied.
	// (We use the unexported constructor; the package-internal
	// test path is intentional — the test lives in the same
	// package, so it can build a *rateLimiter directly.)
	rl := newRateLimiter(1, 0.0001) // ~1 token / 2.7 hours

	r := NewRouter(bot, bot.Cfg,
		WithAuthenticator(auth),
		WithRateLimiter(rl),
		WithCommandHandler("news", newsHandler),
	)

	ctx := context.Background()
	chatID := int64(42)

	// First /news — should pass the rate limiter and invoke the handler.
	if err := r.Handle(ctx, bot.API, makeRouterMsg(chatID, "/news last 24h")); err != nil {
		t.Fatalf("first /news should not error, got %v", err)
	}
	if got := atomic.LoadInt32(&newsHandlerCalls); got != 1 {
		t.Fatalf("first /news should invoke the handler once, got %d", got)
	}

	// Second /news — same chat, immediately after. Bucket is
	// empty (burst=1, refill is 0.0001/sec ≈ 1 token per 2.7h).
	// The handler MUST NOT be invoked.
	if err := r.Handle(ctx, bot.API, makeRouterMsg(chatID, "/news past week")); err != nil {
		t.Fatalf("second /news should not error (rate limit is silent reply, not an error), got %v", err)
	}
	if got := atomic.LoadInt32(&newsHandlerCalls); got != 1 {
		t.Fatalf("second /news must NOT invoke the handler again (rate limit guard), got %d invocations", got)
	}
}

// TestRouter_NewsCommand_SharesBucketWithAgent proves /news and
// /agent share the SAME per-chat bucket. A user who burned 6
// tokens on /agent calls in the same second is denied on the next
// /news. This is the Phase 11 "same budget pool" invariant from
// the work plan.
func TestRouter_NewsCommand_SharesBucketWithAgent(t *testing.T) {
	mock := newTelegramMock(t)
	bot := newMockedBot(t, mock, 42)
	auth := NewAuthenticator(bot.Cfg, PolicySilentDrop, false)

	var newsCalls, agentCalls int32
	newsHandler := func(ctx context.Context, b *tgbotapi.BotAPI, m *tgbotapi.Message, payload string) error {
		atomic.AddInt32(&newsCalls, 1)
		return nil
	}
	agentHandler := func(ctx context.Context, b *tgbotapi.BotAPI, m *tgbotapi.Message, payload string) error {
		atomic.AddInt32(&agentCalls, 1)
		return nil
	}

	rl := newRateLimiter(2, 0.0001) // burst=2 → can fire 2 commands, then empty
	r := NewRouter(bot, bot.Cfg,
		WithAuthenticator(auth),
		WithRateLimiter(rl),
		WithCommandHandler("news", newsHandler),
		WithCommandHandler("agent", agentHandler),
	)

	ctx := context.Background()
	chatID := int64(42)

	// First /agent — pass.
	if err := r.Handle(ctx, bot.API, makeRouterMsg(chatID, "/agent hi")); err != nil {
		t.Fatalf("first /agent: %v", err)
	}
	// Second /agent — pass (last token).
	if err := r.Handle(ctx, bot.API, makeRouterMsg(chatID, "/agent hi again")); err != nil {
		t.Fatalf("second /agent: %v", err)
	}
	// /news now — bucket empty, must be denied.
	if err := r.Handle(ctx, bot.API, makeRouterMsg(chatID, "/news last 24h")); err != nil {
		t.Fatalf("/news after bucket drain: %v", err)
	}
	if got := atomic.LoadInt32(&newsCalls); got != 0 {
		t.Errorf("/news handler should not have been invoked after bucket drained, got %d", got)
	}
	if got := atomic.LoadInt32(&agentCalls); got != 2 {
		t.Errorf("agent calls = %d, want 2", got)
	}
}

// makeRouterMsg builds a tgbotapi.Message for the router Handle
// path. The chat ID is the rate-limiter key.
func makeRouterMsg(chatID int64, text string) *tgbotapi.Message {
	return &tgbotapi.Message{
		MessageID: 1,
		Chat:      &tgbotapi.Chat{ID: chatID, Type: "private"},
		From:      &tgbotapi.User{ID: chatID, UserName: "tester", FirstName: "Test", IsBot: false},
		Text:      text,
	}
}
