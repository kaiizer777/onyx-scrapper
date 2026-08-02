package telegram

import (
	"context"
	"fmt"
	"log/slog"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Bot is the on-disk wrapper around the Telegram BotAPI client. It carries
// the resolved config so downstream code (poller, router, auth) can make
// allowlist / mode decisions without re-reading the file.
type Bot struct {
	API  *tgbotapi.BotAPI
	Cfg  *BotConfig
	Self tgbotapi.User
}

// BotConfig is the trimmed-down view of config.TelegramConfig that the
// gateway needs at runtime. We mirror the relevant fields directly so
// gateway code never reaches into config internals.
type BotConfig struct {
	Mode                   string
	AllowedChatIDs         []int64
	AllowedUsernames       []string
	DefaultMode            string
	MaxConcurrentSessions  int
	TypingIndicator        bool
	WebhookPublicURL       string
	WebhookListenAddr      string
	WebhookSecretToken     string
}

// NewBot initializes the Telegram BotAPI client, verifies the token by
// calling getMe, and returns a Bot ready for routing.
//
// The token is never logged. The startup log line is structured so the
// operator can grep for `telegram.startup` and see the bot's identity
// without leaking the secret.
func NewBot(ctx context.Context, token string, cfg *BotConfig) (*Bot, error) {
	if token == "" {
		return nil, fmt.Errorf("telegram: bot token is empty")
	}
	api, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		return nil, fmt.Errorf("telegram: new bot api: %w", err)
	}

	// getMe doubles as a token-validity check. If the token is bad this
	// returns 401 with a descriptive message; we surface that as-is so
	// the operator knows whether to rotate the token or fix the env var.
	self, err := api.GetMe()
	if err != nil {
		return nil, fmt.Errorf("telegram: getMe (token invalid?): %w", err)
	}

	// Default to debug off; the v5 library will otherwise print every
	// incoming update to stderr, which is too noisy for production.
	api.Debug = false

	mode := "polling"
	if cfg != nil && cfg.Mode != "" {
		mode = cfg.Mode
	}

	slog.InfoContext(ctx, "telegram.startup",
		slog.String("bot_username", self.UserName),
		slog.Int64("bot_id", self.ID),
		slog.String("mode", mode),
		slog.Int("allowlist_chat_ids", len(cfg.AllowedChatIDs)),
		slog.Int("allowlist_usernames", len(cfg.AllowedUsernames)),
		slog.Int("max_concurrent_sessions", cfg.MaxConcurrentSessions),
	)

	// Register bot commands so the `/` menu appears in Telegram clients.
	commands := tgbotapi.NewSetMyCommands(
		tgbotapi.BotCommand{Command: "agent", Description: "Run an autonomous agent"},
		tgbotapi.BotCommand{Command: "research", Description: "Run deep research"},
		tgbotapi.BotCommand{Command: "news", Description: "Fetch profile-driven news digest [duration]"},
		tgbotapi.BotCommand{Command: "cancel", Description: "Cancel the current run"},
	)
	if _, err := api.Request(commands); err != nil {
		slog.Warn("Failed to register bot commands", "error", err)
	}

	return &Bot{
		API:  api,
		Cfg:  cfg,
		Self: self,
	}, nil
}
