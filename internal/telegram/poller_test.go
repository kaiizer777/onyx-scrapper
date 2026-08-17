package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// fakeUpdate is a single getUpdates response item. We use the library's
// own Message type so the JSON tags align exactly.
type fakeUpdate struct {
	UpdateID int                `json:"update_id"`
	Message  *tgbotapi.Message  `json:"message,omitempty"`
}

// telegramMock is a minimal in-process Telegram Bot API server. It is
// only used by the Phase 3 integration test — it is not part of the
// production gateway.
type telegramMock struct {
	server *httptest.Server
	mu     sync.Mutex
	// updatesToSend is a queue: the next getUpdates pops one slot, which
	// may be an Update slice, an empty array, or a 409 conflict (signaled
	// by a sentinel error message).
	updatesToSend [][]fakeUpdate
	// receivedOffsets records the offset param the library passed.
	receivedOffsets []int
	// conflictOnce, when >0, makes the next call return 409 once.
	conflictOnce int32
	// calls counts every inbound HTTP call. Subtract the getMe call
	// (made by NewBotAPIWithClient) to derive sendMessage counts.
	calls int32
	// sendMessageCalls counts only sendMessage hits. Tests that need
	// to assert on user-visible replies (panic recovery, help, etc.)
	// should use this counter, not `calls`.
	sendMessageCalls int32
}

func newTelegramMock(t *testing.T) *telegramMock {
	t.Helper()
	m := &telegramMock{}
	mux := http.NewServeMux()
	mux.HandleFunc("/", m.handle)
	m.server = httptest.NewServer(mux)
	t.Cleanup(m.server.Close)
	return m
}

func (m *telegramMock) handle(w http.ResponseWriter, r *http.Request) {
	atomic.AddInt32(&m.calls, 1)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	// Endpoint after /bot<token>/ e.g. "getMe", "getUpdates", "sendMessage".
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if len(parts) < 2 {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	endpoint := parts[len(parts)-1]

	switch endpoint {
	case "getMe":
		_, _ = io.WriteString(w, `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"FakeBot","username":"fake_bot"}}`)
	case "getUpdates":
		off, _ := parseInt(r.Form.Get("offset"))
		m.mu.Lock()
		m.receivedOffsets = append(m.receivedOffsets, off)
		if atomic.LoadInt32(&m.conflictOnce) > 0 {
			atomic.AddInt32(&m.conflictOnce, -1)
			m.mu.Unlock()
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"ok":false,"error_code":409,"description":"terminated by other getUpdates request"}`)
			return
		}
		if len(m.updatesToSend) == 0 {
			m.mu.Unlock()
			// Long-poll: simulate "no updates" with empty array.
			_, _ = io.WriteString(w, `{"ok":true,"result":[]}`)
			return
		}
		batch := m.updatesToSend[0]
		m.updatesToSend = m.updatesToSend[1:]
		m.mu.Unlock()
		body, _ := json.Marshal(batch)
		_, _ = io.WriteString(w, fmt.Sprintf(`{"ok":true,"result":%s}`, body))
	case "sendMessage":
		atomic.AddInt32(&m.sendMessageCalls, 1)
		// Just acknowledge.
		_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":1,"date":1,"chat":{"id":1,"type":"private"},"text":"ok"}}`)
	default:
		_, _ = io.WriteString(w, `{"ok":true,"result":{}}`)
	}
}

// offsets returns a snapshot of receivedOffsets under the mutex.
func (m *telegramMock) offsets() []int {
	m.mu.Lock()
	out := make([]int, len(m.receivedOffsets))
	copy(out, m.receivedOffsets)
	m.mu.Unlock()
	return out
}

func parseInt(s string) (int, error) {
	if s == "" {
		return 0, nil
	}
	var n int
	_, err := fmt.Sscanf(s, "%d", &n)
	return n, err
}

// helper to build a fakeUpdate with a text message.
func makeFakeUpdate(id int, chatID int64, username, text string) fakeUpdate {
	msg := &tgbotapi.Message{
		MessageID: 1,
		Chat:      &tgbotapi.Chat{ID: chatID, Type: "private"},
		Text:      text,
	}
	if username != "" {
		msg.From = &tgbotapi.User{ID: chatID, UserName: username, FirstName: "Test", IsBot: false}
	}
	return fakeUpdate{UpdateID: id, Message: msg}
}

func newMockedBot(t *testing.T, mock *telegramMock, allowedChatID int64) *Bot {
	t.Helper()
	// The library's URL format is "https://host/bot%s/%s" — replicate.
	api, err := tgbotapi.NewBotAPIWithClient(
		"test-token",
		mock.server.URL+"/bot%s/%s",
		&http.Client{Timeout: 5 * time.Second},
	)
	if err != nil {
		t.Fatalf("NewBotAPIWithClient: %v", err)
	}
	return &Bot{
		API: api,
		Cfg: &BotConfig{
			Mode:           "polling",
			AllowedChatIDs: []int64{allowedChatID},
		},
		Self: api.Self,
	}
}

func TestPoller_ReceivesUpdate_AndCallsHandler(t *testing.T) {
	mock := newTelegramMock(t)
	mock.updatesToSend = [][]fakeUpdate{
		{makeFakeUpdate(100, 42, "alice", "/help")},
	}

	bot := newMockedBot(t, mock, 42)
	auth := NewAuthenticator(bot.Cfg, PolicySilentDrop, false)

	var got int32
	poller := NewPoller(bot, auth, func(ctx context.Context, b *tgbotapi.BotAPI, m *tgbotapi.Message) error {
		atomic.StoreInt32(&got, 1)
		if m.Text != "/help" {
			t.Errorf("expected /help, got %q", m.Text)
		}
		return nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		_ = poller.Run(ctx)
		close(done)
	}()

	// Wait for the handler to fire or timeout.
	deadline := time.Now().Add(3 * time.Second)
	for atomic.LoadInt32(&got) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done

	if atomic.LoadInt32(&got) != 1 {
		t.Fatalf("handler was never called; got=%d calls=%d offsets=%v", atomic.LoadInt32(&got), atomic.LoadInt32(&mock.calls), mock.offsets())
	}
	// Library must have advanced offset past update_id 100.
	if !containsInt(mock.offsets(), 101) {
		t.Errorf("expected offset 101 to be sent eventually, got %v", mock.offsets())
	}
}

func TestPoller_RejectsUnauthorizedSender(t *testing.T) {
	mock := newTelegramMock(t)
	// Sender chat_id=99 is NOT in the allowlist.
	mock.updatesToSend = [][]fakeUpdate{
		{makeFakeUpdate(200, 99, "eve", "/help")},
	}

	bot := newMockedBot(t, mock, 42) // only 42 allowed
	auth := NewAuthenticator(bot.Cfg, PolicySilentDrop, false)

	var called int32
	poller := NewPoller(bot, auth, func(ctx context.Context, b *tgbotapi.BotAPI, m *tgbotapi.Message) error {
		atomic.StoreInt32(&called, 1)
		return nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = poller.Run(ctx)

	// Wait long enough for the poller to drain the message.
	time.Sleep(800 * time.Millisecond)
	cancel()
	_ = poller // keep linter happy if Run already returned

	if atomic.LoadInt32(&called) != 0 {
		t.Fatalf("handler should not have been called for unauthorized sender")
	}
}

func TestPoller_HandlesNonTextMessage(t *testing.T) {
	mock := newTelegramMock(t)
	// Build a fake "photo" message — the photo field is detected by
	// isTextOnlyMessage; we don't actually need a real Photo in JSON,
	// any non-text marker works for the poller's filter (since the
	// filter operates on the parsed struct, not the JSON shape). We send
	// empty text and rely on the poller's empty-text filter to bounce it.
	mock.updatesToSend = [][]fakeUpdate{
		{makeFakeUpdate(300, 42, "alice", "")},
	}

	bot := newMockedBot(t, mock, 42)
	auth := NewAuthenticator(bot.Cfg, PolicySilentDrop, false)

	var called int32
	poller := NewPoller(bot, auth, func(ctx context.Context, b *tgbotapi.BotAPI, m *tgbotapi.Message) error {
		atomic.StoreInt32(&called, 1)
		return nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = poller.Run(ctx)
	time.Sleep(800 * time.Millisecond)
	cancel()

	if atomic.LoadInt32(&called) != 0 {
		t.Fatalf("handler should not be called for non-text message; got called=%d", called)
	}
}

func TestPoller_RecoversFrom409Conflict(t *testing.T) {
	mock := newTelegramMock(t)
	atomic.StoreInt32(&mock.conflictOnce, 1)
	mock.updatesToSend = [][]fakeUpdate{
		{makeFakeUpdate(400, 42, "alice", "/help")},
	}

	bot := newMockedBot(t, mock, 42)
	auth := NewAuthenticator(bot.Cfg, PolicySilentDrop, false)

	var got int32
	poller := NewPoller(bot, auth, func(ctx context.Context, b *tgbotapi.BotAPI, m *tgbotapi.Message) error {
		atomic.StoreInt32(&got, 1)
		return nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = poller.Run(ctx)

	// Wait long enough: 1 conflict + backoff + retry, then handler.
	deadline := time.Now().Add(4 * time.Second)
	for atomic.LoadInt32(&got) == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	cancel()

	if atomic.LoadInt32(&got) != 1 {
		t.Fatalf("handler should fire after 409 recovery; got=%d calls=%d", got, atomic.LoadInt32(&mock.calls))
	}
}

func TestPoller_GracefulShutdown(t *testing.T) {
	mock := newTelegramMock(t)
	bot := newMockedBot(t, mock, 42)
	auth := NewAuthenticator(bot.Cfg, PolicySilentDrop, false)
	poller := NewPoller(bot, auth, nil) // default handler

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- poller.Run(ctx) }()

	time.Sleep(200 * time.Millisecond) // let it start
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned unexpected error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return within 3s after ctx cancel")
	}
}

// containsInt reports whether v is present in s.
func containsInt(s []int, v int) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
