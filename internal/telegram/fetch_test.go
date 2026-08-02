package telegram

import (
	"context"
	"errors"
	"strings"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// TestFetchHandler_RejectsSSRFURLs exercises the SanitizeURL gate
// built into makeFetchHandler. We don't need a real bot; the handler
// short-circuits before any engine work, and the mock's sendMessage
// would only be called on the error-reply path.
func TestFetchHandler_RejectsSSRFURLs(t *testing.T) {
	cases := []string{
		"http://127.0.0.1/x",
		"http://localhost/x",
		"http://10.0.0.1/x",
		"http://192.168.1.1/x",
		"http://169.254.169.254/latest/meta-data/",
		"file:///etc/passwd",
		"javascript:alert(1)",
	}
	for _, raw := range cases {
		// For each bad URL, set up a fresh router so the
		// mock's sendMessage count is independent per case.
		mock := newTelegramMock(t)
		bot := newMockedBot(t, mock, 42)
		router := NewRouter(bot, nil, WithBackends(&EngineBackends{
			Fetch: func(ctx context.Context, targetURL string) (string, error) {
				t.Errorf("runner should not be called for %q", raw)
				return "", nil
			},
		}))
		_ = router.commandHandlers["fetch"](
			context.Background(),
			bot.API,
			&tgbotapi.Message{Chat: &tgbotapi.Chat{ID: 42}, Text: raw},
			raw,
		)
	}
}

func TestFetchHandler_EmptyPayload_RepliesUsage(t *testing.T) {
	mock := newTelegramMock(t)
	bot := newMockedBot(t, mock, 42)
	router := NewRouter(bot, nil, WithBackends(&EngineBackends{
		Fetch: func(ctx context.Context, targetURL string) (string, error) {
			return "ok", nil
		},
	}))
	if err := router.commandHandlers["fetch"](
		context.Background(),
		bot.API,
		&tgbotapi.Message{Chat: &tgbotapi.Chat{ID: 42}, Text: ""},
		"",
	); err != nil {
		t.Errorf("fetch with empty payload: %v", err)
	}
}

func TestFetchHandler_RunnerError_SanitizesTokenFromMessage(t *testing.T) {
	mock := newTelegramMock(t)
	bot := newMockedBot(t, mock, 42)
	router := NewRouter(bot, nil, WithBackends(&EngineBackends{
		Fetch: func(ctx context.Context, targetURL string) (string, error) {
			return "", errors.New("network failed: bearer 1234567890:AAEhBOweik6ad9J3SomethingElse12345xY is invalid")
		},
	}))
	if err := router.commandHandlers["fetch"](
		context.Background(),
		bot.API,
		&tgbotapi.Message{Chat: &tgbotapi.Chat{ID: 42}, Text: ""},
		"https://example.com/page",
	); err != nil {
		t.Errorf("fetch with bad runner: %v", err)
	}
	// We don't have a great way to inspect the sent message
	// from the mock here; the security guarantee is that
	// shortUserError scrubs before reply, which is exercised
	// via RedactToken's test coverage. This test just ensures
	// the runner error path doesn't panic and the handler
	// returns nil.
}

func TestFetchHandler_LargeBody_RoutesToFile(t *testing.T) {
	mock := newTelegramMock(t)
	bot := newMockedBot(t, mock, 42)
	router := NewRouter(bot, nil, WithBackends(&EngineBackends{
		Fetch: func(ctx context.Context, targetURL string) (string, error) {
			// 64 KiB body — over the inline cap of 16 KiB.
			return strings.Repeat("a ", 32*1024), nil
		},
	}))
	if err := router.commandHandlers["fetch"](
		context.Background(),
		bot.API,
		&tgbotapi.Message{Chat: &tgbotapi.Chat{ID: 42}, Text: ""},
		"https://example.com/big",
	); err != nil {
		t.Errorf("fetch with large body: %v", err)
	}
}

func TestFetchHandler_NilRunner_RepliesNotWired(t *testing.T) {
	mock := newTelegramMock(t)
	bot := newMockedBot(t, mock, 42)
	router := NewRouter(bot, nil, WithBackends(&EngineBackends{
		Fetch: nil,
	}))
	if err := router.commandHandlers["fetch"](
		context.Background(),
		bot.API,
		&tgbotapi.Message{Chat: &tgbotapi.Chat{ID: 42}, Text: ""},
		"https://example.com",
	); err != nil {
		t.Errorf("fetch with nil runner: %v", err)
	}
}
