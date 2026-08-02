package telegram

// This file is intentionally kept as an empty package marker so the
// `internal/telegram` package continues to compile in isolation during
// the incremental Phase 2 + Phase 3 rollout. Phase 2 imports
// github.com/go-telegram-bot-api/telegram-bot-api/v5 directly from
// bot.go / auth.go / poller.go, so no blank import is needed here.
