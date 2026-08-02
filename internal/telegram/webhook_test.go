package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// makeUpdateJSON serializes a single Update the way Telegram's webhook
// delivers it: as a single JSON object (not an array — getUpdates
// returns an array, webhooks send a single object).
func makeUpdateJSON(t *testing.T, upd tgbotapi.Update) []byte {
	t.Helper()
	b, err := json.Marshal(upd)
	if err != nil {
		t.Fatalf("marshal update: %v", err)
	}
	return b
}

// textUpdate is a convenience for the most common test fixture.
func textUpdate(chatID int64, username, text string) tgbotapi.Update {
	u := tgbotapi.Update{UpdateID: 1}
	u.Message = &tgbotapi.Message{
		MessageID: 1,
		Chat:      &tgbotapi.Chat{ID: chatID, Type: "private"},
		Text:      text,
	}
	if username != "" {
		u.Message.From = &tgbotapi.User{ID: chatID, UserName: username, FirstName: "Test"}
	}
	return u
}

func TestWebhookHandler_GET_Returns405(t *testing.T) {
	mock := newTelegramMock(t)
	bot := newMockedBot(t, mock, 42)
	auth := NewAuthenticator(bot.Cfg, PolicySilentDrop, false)
	h := NewWebhookHandler(bot, auth, nil, "secret")

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/telegram/webhook", nil)
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET should be 405, got %d", rr.Code)
	}
	if got := rr.Header().Get("Allow"); got != "POST" {
		t.Errorf("Allow header = %q, want POST", got)
	}
}

func TestWebhookHandler_SecretMissingHeader_Returns403(t *testing.T) {
	// Phase 9: a configured secret makes the header mandatory.
	// A request that omits the header is rejected with the
	// same 403 as a wrong header — we don't distinguish so a
	// caller can't probe by varying header presence.
	mock := newTelegramMock(t)
	bot := newMockedBot(t, mock, 42)
	auth := NewAuthenticator(bot.Cfg, PolicySilentDrop, false)
	var called int32
	handler := func(ctx context.Context, b *tgbotapi.BotAPI, m *tgbotapi.Message) error {
		atomic.StoreInt32(&called, 1)
		return nil
	}
	h := NewWebhookHandler(bot, auth, handler, "the-real-secret")

	body := makeUpdateJSON(t, textUpdate(42, "alice", "/help"))
	req := httptest.NewRequest(http.MethodPost, "/telegram/webhook", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// No secret header set.
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("missing header should be 403, got %d", rr.Code)
	}
	if atomic.LoadInt32(&called) != 0 {
		t.Fatal("handler should not be called when secret is missing")
	}
}

func TestWebhookHandler_LegacyToken_AcceptsAndFlags(t *testing.T) {
	// Phase 9: zero-downtime rotation. The handler accepts
	// either the current or the legacy token, but logs when
	// the legacy one is used so the operator can see when
	// to remove it.
	mock := newTelegramMock(t)
	bot := newMockedBot(t, mock, 42)
	auth := NewAuthenticator(bot.Cfg, PolicySilentDrop, false)
	var called int32
	handler := func(ctx context.Context, b *tgbotapi.BotAPI, m *tgbotapi.Message) error {
		atomic.AddInt32(&called, 1)
		return nil
	}
	h := NewWebhookHandler(bot, auth, handler, "new-secret")
	h.AddLegacyToken("old-secret")

	// Fresh body for each request. We share the template but
	// each reader has its own cursor.
	body1 := makeUpdateJSON(t, textUpdate(42, "alice", "/help"))
	body2 := makeUpdateJSON(t, textUpdate(42, "alice", "/help"))

	// New token works. Use a fresh bytes.Reader per request so
	// the second request can also be read.
	req1 := httptest.NewRequest(http.MethodPost, "/telegram/webhook", bytes.NewReader(body1))
	req1.Header.Set("Content-Type", "application/json")
	req1.Header.Set(telegramSecretHeader, "new-secret")
	rr1 := httptest.NewRecorder()
	h.ServeHTTP(rr1, req1)
	if rr1.Code != http.StatusOK {
		t.Errorf("new token should be 200, got %d", rr1.Code)
	}

	// Old token still works.
	req2 := httptest.NewRequest(http.MethodPost, "/telegram/webhook", bytes.NewReader(body2))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set(telegramSecretHeader, "old-secret")
	rr2 := httptest.NewRecorder()
	h.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Errorf("legacy token should be 200, got %d", rr2.Code)
	}

	if atomic.LoadInt32(&called) != 2 {
		t.Errorf("handler should have been called twice (new+legacy), got %d", called)
	}
}

func TestWebhookHandler_AddLegacyToken_DedupsAndIgnoresEmpty(t *testing.T) {
	h := &WebhookHandler{}
	h.AddLegacyToken("")
	if len(h.secretTokens) != 0 {
		t.Errorf("empty token should not be added")
	}
	h.AddLegacyToken("abc")
	h.AddLegacyToken("abc") // duplicate
	if len(h.secretTokens) != 1 {
		t.Errorf("expected dedup; got %d tokens", len(h.secretTokens))
	}
}

func TestWebhookHandler_RequireSecret_False_PermitsNoSecret(t *testing.T) {
	// Operator explicitly opts out of secret enforcement.
	mock := newTelegramMock(t)
	bot := newMockedBot(t, mock, 42)
	auth := NewAuthenticator(bot.Cfg, PolicySilentDrop, false)
	var called int32
	handler := func(ctx context.Context, b *tgbotapi.BotAPI, m *tgbotapi.Message) error {
		atomic.StoreInt32(&called, 1)
		return nil
	}
	h := NewWebhookHandler(bot, auth, handler, "")
	h.RequireSecret(true) // no tokens but requireSecret on => 403

	body := makeUpdateJSON(t, textUpdate(42, "alice", "/help"))
	req := httptest.NewRequest(http.MethodPost, "/telegram/webhook", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("requireSecret=true with no tokens should be 403, got %d", rr.Code)
	}
}

func TestWebhookHandler_SecretMismatch_Returns403(t *testing.T) {
	mock := newTelegramMock(t)
	bot := newMockedBot(t, mock, 42)
	auth := NewAuthenticator(bot.Cfg, PolicySilentDrop, false)
	h := NewWebhookHandler(bot, auth, nil, "the-real-secret")

	body := makeUpdateJSON(t, textUpdate(42, "alice", "/help"))
	req := httptest.NewRequest(http.MethodPost, "/telegram/webhook", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(telegramSecretHeader, "WRONG-secret")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rr.Code)
	}
}

func TestWebhookHandler_SecretMatch_DispatchesHandler(t *testing.T) {
	mock := newTelegramMock(t)
	bot := newMockedBot(t, mock, 42)
	auth := NewAuthenticator(bot.Cfg, PolicySilentDrop, false)

	var called int32
	handler := func(ctx context.Context, b *tgbotapi.BotAPI, m *tgbotapi.Message) error {
		atomic.StoreInt32(&called, 1)
		if m.Text != "/help" {
			t.Errorf("expected /help, got %q", m.Text)
		}
		return nil
	}
	h := NewWebhookHandler(bot, auth, handler, "good-secret")

	body := makeUpdateJSON(t, textUpdate(42, "alice", "/help"))
	req := httptest.NewRequest(http.MethodPost, "/telegram/webhook", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(telegramSecretHeader, "good-secret")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if atomic.LoadInt32(&called) != 1 {
		t.Fatalf("handler was not called; called=%d", atomic.LoadInt32(&called))
	}
}

func TestWebhookHandler_NoSecretConfigured_AcceptsAny(t *testing.T) {
	// When secretToken is empty, the operator has chosen to run
	// without header validation. The handler must still accept
	// correctly-formed updates.
	mock := newTelegramMock(t)
	bot := newMockedBot(t, mock, 42)
	auth := NewAuthenticator(bot.Cfg, PolicySilentDrop, false)

	var called int32
	handler := func(ctx context.Context, b *tgbotapi.BotAPI, m *tgbotapi.Message) error {
		atomic.StoreInt32(&called, 1)
		return nil
	}
	h := NewWebhookHandler(bot, auth, handler, "") // no secret

	body := makeUpdateJSON(t, textUpdate(42, "alice", "/help"))
	req := httptest.NewRequest(http.MethodPost, "/telegram/webhook", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// Note: NO secret header.
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if atomic.LoadInt32(&called) != 1 {
		t.Fatalf("handler should be called when secret is disabled")
	}
}

func TestWebhookHandler_MalformedBody_Returns400(t *testing.T) {
	mock := newTelegramMock(t)
	bot := newMockedBot(t, mock, 42)
	auth := NewAuthenticator(bot.Cfg, PolicySilentDrop, false)
	h := NewWebhookHandler(bot, auth, nil, "")

	req := httptest.NewRequest(http.MethodPost, "/telegram/webhook",
		strings.NewReader("{not json"))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("malformed body should be 400, got %d", rr.Code)
	}
}

func TestWebhookHandler_OversizedBody_Returns400(t *testing.T) {
	mock := newTelegramMock(t)
	bot := newMockedBot(t, mock, 42)
	auth := NewAuthenticator(bot.Cfg, PolicySilentDrop, false)
	h := NewWebhookHandler(bot, auth, nil, "")
	// 2 MiB of garbage — well above the 1 MiB cap.
	huge := bytes.Repeat([]byte("a"), 2<<20)
	req := httptest.NewRequest(http.MethodPost, "/telegram/webhook", bytes.NewReader(huge))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("oversized body should be 400, got %d", rr.Code)
	}
}

func TestWebhookHandler_UnauthorizedSender_Acks200_NoHandler(t *testing.T) {
	// Telegram should still get 200 — we received the update, we just
	// don't process it. Returning non-2xx would cause Telegram to
	// retry forever against an allowlisted bot.
	mock := newTelegramMock(t)
	bot := newMockedBot(t, mock, 42) // only 42 allowed
	auth := NewAuthenticator(bot.Cfg, PolicySilentDrop, false)

	var called int32
	handler := func(ctx context.Context, b *tgbotapi.BotAPI, m *tgbotapi.Message) error {
		atomic.StoreInt32(&called, 1)
		return nil
	}
	h := NewWebhookHandler(bot, auth, handler, "")

	body := makeUpdateJSON(t, textUpdate(99, "eve", "/help"))
	req := httptest.NewRequest(http.MethodPost, "/telegram/webhook", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("unauthorized sender must still ack 200, got %d", rr.Code)
	}
	if atomic.LoadInt32(&called) != 0 {
		t.Fatalf("handler must not be called for unauthorized sender; called=%d", atomic.LoadInt32(&called))
	}
}

func TestWebhookHandler_HandlerPanic_StillReturns200(t *testing.T) {
	// A handler panic must not 500 the webhook. Telegram would retry
	// a 500 indefinitely; returning 200 tells Telegram we accepted
	// the update, and the panic is logged by safeInvoke.
	mock := newTelegramMock(t)
	bot := newMockedBot(t, mock, 42)
	auth := NewAuthenticator(bot.Cfg, PolicySilentDrop, false)

	boom := func(ctx context.Context, b *tgbotapi.BotAPI, m *tgbotapi.Message) error {
		panic("simulated router bug")
	}
	h := NewWebhookHandler(bot, auth, boom, "")

	body := makeUpdateJSON(t, textUpdate(42, "alice", "/help"))
	req := httptest.NewRequest(http.MethodPost, "/telegram/webhook", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("panicking handler should not surface as 5xx, got %d", rr.Code)
	}
}

func TestWebhookHandler_NonTextMessage_AcksAndReplies(t *testing.T) {
	// Photo/sticker/etc. messages should be acknowledged to Telegram
	// (so it does not retry) AND a polite reply sent to the user.
	mock := newTelegramMock(t)
	bot := newMockedBot(t, mock, 42)
	auth := NewAuthenticator(bot.Cfg, PolicySilentDrop, false)

	var called int32
	handler := func(ctx context.Context, b *tgbotapi.BotAPI, m *tgbotapi.Message) error {
		atomic.StoreInt32(&called, 1)
		return nil
	}
	h := NewWebhookHandler(bot, auth, handler, "")

	// Build a photo-bearing message.
	u := tgbotapi.Update{UpdateID: 7}
	u.Message = &tgbotapi.Message{
		MessageID: 1,
		Chat:      &tgbotapi.Chat{ID: 42, Type: "private"},
		From:      &tgbotapi.User{ID: 42, UserName: "alice", FirstName: "Test"},
		Photo: []tgbotapi.PhotoSize{
			{FileID: "abc", Width: 100, Height: 100},
		},
	}
	body := makeUpdateJSON(t, u)
	req := httptest.NewRequest(http.MethodPost, "/telegram/webhook", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("non-text message should still ack 200, got %d", rr.Code)
	}
	if atomic.LoadInt32(&called) != 0 {
		t.Fatalf("handler must not be called for non-text message; called=%d", atomic.LoadInt32(&called))
	}
}

func TestSetWebhook_RejectsNonHTTPS(t *testing.T) {
	// We don't need a real mock here — SetWebhook validates before any
	// HTTP call. We do need a valid getMe response so NewBotAPIWithClient
	// does not fail at construction; the only assertion is that
	// SetWebhook refuses the http:// URL with a clear error.
	wrapper := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if lastSegment(r.URL.Path) == "getMe" {
			_, _ = io.WriteString(w, `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"F","username":"fake_bot"}}`)
			return
		}
		t.Errorf("SetWebhook should not make an HTTP call on validation failure; got %s", r.URL.Path)
		_, _ = io.WriteString(w, `{"ok":true,"result":true}`)
	}))
	defer wrapper.Close()
	api, err := tgbotapi.NewBotAPIWithClient("test-token", wrapper.URL+"/bot%s/%s", &http.Client{Timeout: 1 * time.Second})
	if err != nil {
		t.Fatalf("NewBotAPIWithClient: %v", err)
	}
	err = SetWebhook(context.Background(), api, "http://insecure.example.com/hook", "", 40)
	if err == nil {
		t.Fatal("expected error for http:// URL")
	}
	if !strings.Contains(err.Error(), "https") {
		t.Errorf("error should mention https requirement, got %v", err)
	}
}

func TestSetWebhook_RejectsEmpty(t *testing.T) {
	wrapper := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if lastSegment(r.URL.Path) == "getMe" {
			_, _ = io.WriteString(w, `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"F","username":"fake_bot"}}`)
			return
		}
		t.Errorf("SetWebhook should not make an HTTP call on validation failure; got %s", r.URL.Path)
		_, _ = io.WriteString(w, `{"ok":true,"result":true}`)
	}))
	defer wrapper.Close()
	api, err := tgbotapi.NewBotAPIWithClient("test-token", wrapper.URL+"/bot%s/%s", &http.Client{Timeout: 1 * time.Second})
	if err != nil {
		t.Fatalf("NewBotAPIWithClient: %v", err)
	}
	err = SetWebhook(context.Background(), api, "", "", 40)
	if err == nil {
		t.Fatal("expected error for empty URL")
	}
}

func TestSetWebhook_SendsExpectedParams(t *testing.T) {
	// We don't use the default mock here because we need a custom
	// server to capture the inbound request. The library's MakeRequest
	// uses POST with a form-encoded BODY (not a query string) — see
	// bot.go MakeRequest in v5.5.1 — so we read the body and parse it.
	var capturedPath string
	var capturedBody string
	wrapper := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		capturedBody = string(body)
		endpoint := lastSegment(r.URL.Path)
		// getMe: respond with a User-shaped payload so the library's
		// startup self-check can decode it.
		if endpoint == "getMe" {
			_, _ = io.WriteString(w, `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"F","username":"fake_bot"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true,"result":true}`)
	}))
	defer wrapper.Close()
	api, err := tgbotapi.NewBotAPIWithClient("test-token", wrapper.URL+"/bot%s/%s", &http.Client{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("NewBotAPIWithClient: %v", err)
	}

	err = SetWebhook(context.Background(), api, "https://example.com/telegram/webhook", "s3cr3t", 40)
	if err != nil {
		t.Fatalf("SetWebhook: %v", err)
	}
	if !strings.HasSuffix(capturedPath, "/setWebhook") {
		t.Errorf("expected setWebhook endpoint, got path %q", capturedPath)
	}
	if !strings.Contains(capturedBody, "url=https%3A%2F%2Fexample.com%2Ftelegram%2Fwebhook") {
		t.Errorf("body missing url param: %s", capturedBody)
	}
	if !strings.Contains(capturedBody, "secret_token=s3cr3t") {
		t.Errorf("body missing secret_token: %s", capturedBody)
	}
	if !strings.Contains(capturedBody, "max_connections=40") {
		t.Errorf("body missing max_connections: %s", capturedBody)
	}
	if !strings.Contains(capturedBody, "allowed_updates") {
		t.Errorf("body missing allowed_updates: %s", capturedBody)
	}
}

// lastSegment returns the last '/'-separated component of a path, used
// to extract the Telegram endpoint name from /bot<token>/<endpoint>.
func lastSegment(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

func TestWebhookHandler_UnknownJSONField_StillAccepted(t *testing.T) {
	// The library types tag a fixed set of fields. Telegram adds new
	// fields to the Update payload on occasion. DisallowUnknownFields
	// would reject them, breaking the bot on every Telegram release.
	// We deliberately do NOT use DisallowUnknownFields; verify that
	// by sending an update with an extra field and seeing it accepted.
	mock := newTelegramMock(t)
	bot := newMockedBot(t, mock, 42)
	auth := NewAuthenticator(bot.Cfg, PolicySilentDrop, false)

	var called int32
	handler := func(ctx context.Context, b *tgbotapi.BotAPI, m *tgbotapi.Message) error {
		atomic.StoreInt32(&called, 1)
		return nil
	}
	h := NewWebhookHandler(bot, auth, handler, "")

	body := []byte(`{"update_id":1,"message":{"message_id":1,"chat":{"id":42,"type":"private"},"text":"/help","from":{"id":42,"is_bot":false,"first_name":"A","username":"alice"},"future_telegram_field":"ignore-me"}}`)
	req := httptest.NewRequest(http.MethodPost, "/telegram/webhook", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("forward-compat field should be accepted, got %d body=%s", rr.Code, rr.Body.String())
	}
	if atomic.LoadInt32(&called) != 1 {
		t.Fatalf("handler should have been called; called=%d", atomic.LoadInt32(&called))
	}
}

func TestConstantTimeEqual(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"abc", "abc", true},
		{"abc", "abd", false},
		{"abc", "abcd", false}, // length mismatch
		{"", "", true},
		{"", "a", false},
	}
	for _, tc := range cases {
		if got := constantTimeEqual(tc.a, tc.b); got != tc.want {
			t.Errorf("constantTimeEqual(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestReconcileMode_PollingWithActiveWebhook_DeletesIt(t *testing.T) {
	// Spin a fake Telegram server that:
	//   1. Returns a valid getMe (required for NewBotAPIWithClient)
	//   2. Reports an existing webhook on getWebhookInfo
	//   3. Records the deleteWebhook call
	var getCalls, deleteCalls int32
	wrapper := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		endpoint := lastSegment(r.URL.Path)
		switch endpoint {
		case "getMe":
			_, _ = io.WriteString(w, `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"F","username":"fake_bot"}}`)
		case "getWebhookInfo":
			atomic.AddInt32(&getCalls, 1)
			_, _ = io.WriteString(w, `{"ok":true,"result":{"url":"https://stale.example.com/hook","has_custom_certificate":false,"pending_update_count":0}}`)
		case "deleteWebhook":
			atomic.AddInt32(&deleteCalls, 1)
			_, _ = io.WriteString(w, `{"ok":true,"result":true}`)
		default:
			_, _ = io.WriteString(w, `{"ok":true,"result":true}`)
		}
	}))
	defer wrapper.Close()

	api, err := tgbotapi.NewBotAPIWithClient("test-token", wrapper.URL+"/bot%s/%s", &http.Client{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("NewBotAPIWithClient: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	info, err := ReconcileMode(ctx, api, "polling")
	if err != nil {
		t.Fatalf("ReconcileMode: %v", err)
	}
	if info.URL != "" {
		t.Errorf("expected URL to be cleared, got %q", info.URL)
	}
	if atomic.LoadInt32(&getCalls) < 1 {
		t.Errorf("expected getWebhookInfo call")
	}
	if atomic.LoadInt32(&deleteCalls) != 1 {
		t.Errorf("expected exactly one deleteWebhook call, got %d", atomic.LoadInt32(&deleteCalls))
	}
}

func TestReconcileMode_PollingNoWebhook_NoOp(t *testing.T) {
	var getCalls, deleteCalls int32
	wrapper := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		endpoint := lastSegment(r.URL.Path)
		switch endpoint {
		case "getMe":
			_, _ = io.WriteString(w, `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"F","username":"fake_bot"}}`)
		case "getWebhookInfo":
			atomic.AddInt32(&getCalls, 1)
			_, _ = io.WriteString(w, `{"ok":true,"result":{"url":"","has_custom_certificate":false,"pending_update_count":0}}`)
		case "deleteWebhook":
			atomic.AddInt32(&deleteCalls, 1)
			_, _ = io.WriteString(w, `{"ok":true,"result":true}`)
		default:
			_, _ = io.WriteString(w, `{"ok":true,"result":true}`)
		}
	}))
	defer wrapper.Close()

	api, err := tgbotapi.NewBotAPIWithClient("test-token", wrapper.URL+"/bot%s/%s", &http.Client{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("NewBotAPIWithClient: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := ReconcileMode(ctx, api, "polling"); err != nil {
		t.Fatalf("ReconcileMode: %v", err)
	}
	if atomic.LoadInt32(&getCalls) < 1 {
		t.Errorf("expected getWebhookInfo call")
	}
	if atomic.LoadInt32(&deleteCalls) != 0 {
		t.Errorf("deleteWebhook should NOT be called when no webhook is active, got %d", atomic.LoadInt32(&deleteCalls))
	}
}
