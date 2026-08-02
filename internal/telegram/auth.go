package telegram

import (
	"log/slog"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// AuthDecision is the result of a single allowlist check.
type AuthDecision int

const (
	// AuthAllow: the sender is in the allowlist. Pass the update to the router.
	AuthAllow AuthDecision = iota
	// AuthSilentDrop: deny without sending a reply (default — never confirms
	// the bot exists to outsiders).
	AuthSilentDrop
	// AuthReplyDeny: deny and send a one-line "unauthorized" reply so the
	// operator can see during testing that auth is firing.
	AuthReplyDeny
)

// AuthPolicy controls how the middleware reacts to disallowed senders.
type AuthPolicy int

const (
	// PolicySilentDrop: do not reply to disallowed senders at all.
	PolicySilentDrop AuthPolicy = iota
	// PolicyReplyDeny: send a single "unauthorized" message back.
	PolicyReplyDeny
)

// Authenticator is the middleware applied to every update before it reaches
// the command router. It is intentionally cheap (in-memory set lookup) so it
// can run on every polling tick without measurable cost.
type Authenticator struct {
	chatIDs   map[int64]struct{}
	usernames map[string]struct{}
	policy    AuthPolicy
	// allowEmptyList: when true and both lists are empty, EVERY sender is
	// allowed. When false (the default), an empty allowlist means EVERY
	// sender is rejected — fail-closed. This prevents a misconfiguration
	// from accidentally exposing the bot.
	allowEmptyList bool
}

// NewAuthenticator builds the middleware from the bot config. It is
// nil-safe on cfg so callers can pass nil for "no allowlist configured".
func NewAuthenticator(cfg *BotConfig, policy AuthPolicy, allowEmptyList bool) *Authenticator {
	a := &Authenticator{
		chatIDs:        map[int64]struct{}{},
		usernames:      map[string]struct{}{},
		policy:         policy,
		allowEmptyList: allowEmptyList,
	}
	if cfg == nil {
		return a
	}
	for _, id := range cfg.AllowedChatIDs {
		a.chatIDs[id] = struct{}{}
	}
	for _, u := range cfg.AllowedUsernames {
		// Strip leading '@' if present, normalize to lower-case — Telegram
		// usernames are case-insensitive.
		normalized := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(u), "@"))
		if normalized != "" {
			a.usernames[normalized] = struct{}{}
		}
	}
	return a
}

// Check inspects an update and returns the decision. The bot pointer is
// only used to send a deny-reply when policy == PolicyReplyDeny; nil is
// safe for the silent-drop path.
func (a *Authenticator) Check(bot *tgbotapi.BotAPI, update *tgbotapi.Update) AuthDecision {
	// Edge case: update with no message (only edited_message, callback_query,
	// etc.) — v1 only handles Message. For everything else we silently drop
	// because there is no sender identity we can allowlist against in the
	// current data model.
	if update == nil || update.Message == nil {
		return AuthSilentDrop
	}

	chatID := update.Message.Chat.ID
	username := ""
	if update.Message.From != nil {
		username = strings.ToLower(update.Message.From.UserName)
	}

	// Empty allowlist handling.
	if len(a.chatIDs) == 0 && len(a.usernames) == 0 {
		if a.allowEmptyList {
			return AuthAllow
		}
		// Fail-closed: empty allowlist = nobody allowed. This is the
		// safer default — operator has to deliberately add chat IDs.
		return a.deny(bot, chatID, "empty allowlist", false)
	}

	if _, ok := a.chatIDs[chatID]; ok {
		return AuthAllow
	}
	if username != "" {
		if _, ok := a.usernames[username]; ok {
			return AuthAllow
		}
	}

	return a.deny(bot, chatID, username, true)
}

// AllowlistIdentity is the minimal payload DefenseCheck needs. It is
// exported so security.go (and any future internal caller) can pass
// it to IsAllowed without depending on the tgbotapi types.
type AllowlistIdentity struct {
	ChatID   int64
	Username string
}

// IsAllowed is the boolean-only variant of Check used by the Phase-9
// defense-in-depth re-check. It is a one-liner over Check so the
// canonical allowlist logic stays in this file (no risk of two
// different decision paths drifting).
func (a *Authenticator) IsAllowed(ident AllowlistIdentity) bool {
	return a.matchChatAndUsername(ident.ChatID, strings.ToLower(ident.Username))
}

// matchChatAndUsername runs the same allowlist logic as Check but on
// a pair of (chatID, username) rather than a full tgbotapi.Update.
// We factor it out so the synthetic-update path in IsAllowed and the
// production path in Check share the same decision tree.
func (a *Authenticator) matchChatAndUsername(chatID int64, username string) bool {
	if a == nil {
		return true
	}
	if len(a.chatIDs) == 0 && len(a.usernames) == 0 {
		return a.allowEmptyList
	}
	if _, ok := a.chatIDs[chatID]; ok {
		return true
	}
	if username != "" {
		if _, ok := a.usernames[username]; ok {
			return true
		}
	}
	return false
}

func (a *Authenticator) deny(bot *tgbotapi.BotAPI, chatID int64, identity string, logged bool) AuthDecision {
	// Log every deny so the operator can audit who is poking the bot.
	// We log chat_id and (lowercased) username; the bot's own self.UserName
	// is never logged here — that's in bot.go once at startup.
	if logged {
		slog.Warn("telegram.auth.denied",
			slog.Int64("chat_id", chatID),
			slog.String("identity", identity),
		)
	}

	if a.policy == PolicyReplyDeny && bot != nil {
		msg := tgbotapi.NewMessage(chatID, "unauthorized: your chat is not on the allowlist")
		// Fire-and-forget. If the reply fails (chat deleted, blocked, etc.)
		// there is nothing useful to do; log and move on.
		if _, err := bot.Send(msg); err != nil {
			slog.Warn("telegram.auth.deny_reply_failed", slog.Int64("chat_id", chatID), slog.String("error", err.Error()))
		}
		return AuthReplyDeny
	}
	return AuthSilentDrop
}
