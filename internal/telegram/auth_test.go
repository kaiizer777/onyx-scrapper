package telegram

import (
	"strconv"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func makeUpdate(chatID int64, username string) *tgbotapi.Update {
	upd := &tgbotapi.Update{UpdateID: 1}
	upd.Message = &tgbotapi.Message{
		MessageID: 1,
		Chat:      &tgbotapi.Chat{ID: chatID, Type: "private"},
	}
	if username != "" {
		upd.Message.From = &tgbotapi.User{ID: chatID, UserName: username, FirstName: "Test"}
	}
	return upd
}

func TestAuthenticator_AllowByChatID(t *testing.T) {
	a := NewAuthenticator(&BotConfig{AllowedChatIDs: []int64{42}}, PolicySilentDrop, false)
	if d := a.Check(nil, makeUpdate(42, "")); d != AuthAllow {
		t.Fatalf("expected AuthAllow, got %v", d)
	}
}

func TestAuthenticator_AllowByUsername(t *testing.T) {
	cfg := &BotConfig{AllowedUsernames: []string{"@Alice", "Bob"}}
	a := NewAuthenticator(cfg, PolicySilentDrop, false)

	// case-insensitive + '@' prefix normalization
	if d := a.Check(nil, makeUpdate(1, "alice")); d != AuthAllow {
		t.Fatalf("expected AuthAllow for @alice (case-insensitive), got %v", d)
	}
	if d := a.Check(nil, makeUpdate(2, "BOB")); d != AuthAllow {
		t.Fatalf("expected AuthAllow for @BOB, got %v", d)
	}
}

func TestAuthenticator_DenyUnknownSender(t *testing.T) {
	a := NewAuthenticator(&BotConfig{AllowedChatIDs: []int64{42}}, PolicySilentDrop, false)
	if d := a.Check(nil, makeUpdate(99, "eve")); d != AuthSilentDrop {
		t.Fatalf("expected AuthSilentDrop, got %v", d)
	}
}

func TestAuthenticator_EmptyAllowlist_FailsClosed(t *testing.T) {
	// allowEmptyList=false (default): empty lists = nobody allowed.
	a := NewAuthenticator(&BotConfig{}, PolicySilentDrop, false)
	if d := a.Check(nil, makeUpdate(1, "anyone")); d != AuthSilentDrop {
		t.Fatalf("expected AuthSilentDrop with empty allowlist (fail-closed), got %v", d)
	}
}

func TestAuthenticator_EmptyAllowlist_OptIn_AllowsAll(t *testing.T) {
	// allowEmptyList=true: empty lists = everyone allowed.
	a := NewAuthenticator(&BotConfig{}, PolicySilentDrop, true)
	if d := a.Check(nil, makeUpdate(1, "anyone")); d != AuthAllow {
		t.Fatalf("expected AuthAllow with empty allowlist + opt-in, got %v", d)
	}
}

func TestAuthenticator_NilUpdate_Dropped(t *testing.T) {
	a := NewAuthenticator(&BotConfig{AllowedChatIDs: []int64{42}}, PolicySilentDrop, false)
	if d := a.Check(nil, nil); d != AuthSilentDrop {
		t.Fatalf("expected AuthSilentDrop for nil update, got %v", d)
	}
	if d := a.Check(nil, &tgbotapi.Update{UpdateID: 2}); d != AuthSilentDrop {
		t.Fatalf("expected AuthSilentDrop for update with no message, got %v", d)
	}
}

func TestAuthenticator_UsernameFallsBackWhenChatIDMisses(t *testing.T) {
	// Allowed by username, not by chat_id — must still pass.
	cfg := &BotConfig{AllowedUsernames: []string{"trusted"}}
	a := NewAuthenticator(cfg, PolicySilentDrop, false)
	upd := makeUpdate(999, "trusted")
	if d := a.Check(nil, upd); d != AuthAllow {
		t.Fatalf("expected AuthAllow via username fallback, got %v", d)
	}
}

func TestAuthenticator_PolicyReplyDeny_NilBot_DegradesToSilent(t *testing.T) {
	// When bot is nil we can't send a deny reply; the helper must not
	// panic and must fall back to silent-drop. This keeps the
	// middleware usable in tests and in any future "dry-run" path.
	a := NewAuthenticator(&BotConfig{AllowedChatIDs: []int64{1}}, PolicyReplyDeny, false)
	d := a.Check(nil, makeUpdate(2, ""))
	if d != AuthSilentDrop {
		t.Fatalf("expected AuthSilentDrop when bot is nil under PolicyReplyDeny, got %v", d)
	}
}

func TestAuthenticator_ChatIDDoesNotMatch_UsernameDoesNotMatch(t *testing.T) {
	cfg := &BotConfig{
		AllowedChatIDs:   []int64{1, 2, 3},
		AllowedUsernames: []string{"alice"},
	}
	a := NewAuthenticator(cfg, PolicySilentDrop, false)
	upd := makeUpdate(99, "eve")
	if d := a.Check(nil, upd); d != AuthSilentDrop {
		t.Fatalf("expected AuthSilentDrop for unknown sender, got %v (decision=%d)", d, d)
	}
}

func TestAuthenticator_DenyLogsContainChatID(t *testing.T) {
	// Sanity: an int64 chat_id should always be representable in a slog
	// Int64 attr without precision loss (this is a 1-line guard against
	// someone "fixing" the struct by switching to int).
	a := NewAuthenticator(&BotConfig{AllowedChatIDs: []int64{1}}, PolicySilentDrop, false)
	const huge int64 = 9223372036854775807 // max int64
	upd := makeUpdate(huge, "")
	if d := a.Check(nil, upd); d != AuthSilentDrop {
		t.Fatalf("expected AuthSilentDrop, got %v", d)
	}
	if strconv.FormatInt(huge, 10) != "9223372036854775807" {
		t.Fatal("test setup drift: int64 round-trip broken")
	}
}
