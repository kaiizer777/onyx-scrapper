package telegram

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// newMockedBotAPI returns a *tgbotapi.BotAPI pointed at the supplied
// httptest server so the deliverer / formatter code can be exercised
// without hitting Telegram. The tests use their own server handlers
// to inspect the request body and parse mode.
func newMockedBotAPI(t *testing.T, srv *httptest.Server) *tgbotapi.BotAPI {
	t.Helper()
	api, err := tgbotapi.NewBotAPIWithClient(
		"test-token",
		srv.URL+"/bot%s/%s",
		&http.Client{Timeout: 5 * time.Second},
	)
	if err != nil {
		t.Fatalf("NewBotAPIWithClient: %v", err)
	}
	return api
}

func TestDeliverer_SingleMessage_UsesHTMLParseMode(t *testing.T) {
	var lastParseMode string
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		lastParseMode = r.Form.Get("parse_mode")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1,"date":1,"chat":{"id":1,"type":"private"},"text":"ok"}}`))
	}))
	defer mock.Close()
	api := newMockedBotAPI(t, mock)

	d := newDeliverer()
	err := d.DeliverText(context.Background(), api, 42, "header", "hello world")
	if err != nil {
		t.Fatalf("DeliverText: %v", err)
	}
	if lastParseMode != "HTML" {
		t.Errorf("expected parse_mode=HTML, got %q", lastParseMode)
	}
}

func TestDeliverer_LongBodyChunksAtParagraphBoundary(t *testing.T) {
	var calls int32
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1,"date":1,"chat":{"id":1,"type":"private"},"text":"ok"}}`))
	}))
	defer mock.Close()
	api := newMockedBotAPI(t, mock)

	d := newDeliverer()
	// Build a body that's well over the cap. We don't inspect
	// the chunks here — just that more than one sendMessage
	// was emitted.
	big := strings.Repeat("aaaa ", 1500) + "\n\n" + strings.Repeat("bbbb ", 1500)
	if err := d.DeliverText(context.Background(), api, 42, "", big); err != nil {
		t.Fatalf("DeliverText: %v", err)
	}
	if atomic.LoadInt32(&calls) < 2 {
		t.Errorf("expected multiple chunks; got %d sendMessage calls", calls)
	}
}

func TestDeliverer_HugeBodyFallsBackToFile(t *testing.T) {
	// Track sendDocument hits (the mock will see a different
	// request shape: file=... etc.).
	gotDoc := false
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		// sendDocument sends the file as multipart form data;
		// the simplest signal is that the path contains
		// "/sendDocument" rather than "/sendMessage".
		if strings.Contains(r.URL.Path, "sendDocument") {
			gotDoc = true
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1,"date":1,"chat":{"id":1,"type":"private"},"text":"ok"}}`))
	}))
	defer mock.Close()
	api := newMockedBotAPI(t, mock)

	d := newDeliverer()
	huge := strings.Repeat("a paragraph of text. ", FileFallbackThreshold/20)
	err := d.DeliverReport(context.Background(), api, 42, "huge report", huge, nil)
	if err != nil {
		t.Fatalf("DeliverReport: %v", err)
	}
	if !gotDoc {
		t.Errorf("expected sendDocument to be used for huge body")
	}
}

// TestDeliverer_FileFallback_SendsMarkdownNotHTML is a regression test for the
// bug where DeliverReport sent HTML-rendered content (with <b>, <i>, <a href>
// tags) as the file attachment, causing users to see raw HTML source instead of
// readable text. The file body must contain the original markdown, not HTML.
func TestDeliverer_FileFallback_SendsMarkdownNotHTML(t *testing.T) {
	var capturedBody []byte
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "sendDocument") {
			if err := r.ParseMultipartForm(10 << 20); err == nil {
				f, _, err := r.FormFile("document")
				if err == nil {
					defer f.Close()
					buf := make([]byte, 10<<20)
					n, _ := f.Read(buf)
					capturedBody = buf[:n]
				}
			}
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1,"date":1,"chat":{"id":1,"type":"private"},"text":"ok"}}`))
	}))
	defer mock.Close()
	api := newMockedBotAPI(t, mock)

	d := newDeliverer()
	// Construct a markdown report big enough to trigger the file fallback.
	// It contains bold markers (**) that the formatter would convert to <b> tags.
	mdLine := "**bold heading** and some text. "
	huge := strings.Repeat(mdLine, FileFallbackThreshold/len(mdLine)+10)
	if err := d.DeliverReport(context.Background(), api, 42, "", huge, nil); err != nil {
		t.Fatalf("DeliverReport: %v", err)
	}
	if len(capturedBody) == 0 {
		t.Fatal("expected sendDocument to be called with a file body")
	}
	body := string(capturedBody)
	// The body must be raw markdown — it must contain the original ** markers,
	// not the HTML <b> tags the formatter would produce.
	if strings.Contains(body, "<b>") || strings.Contains(body, "</b>") {
		t.Errorf("file body contains HTML tags — expected raw markdown; got prefix: %q", body[:min(200, len(body))])
	}
	if !strings.Contains(body, "**bold heading**") {
		t.Errorf("file body missing original markdown markers; got prefix: %q", body[:min(200, len(body))])
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestDeliverer_EmptyBodyIsNoOp(t *testing.T) {
	var calls int32
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		// First call is getMe (constructor). The DeliverText
		// and DeliverReport calls below must NOT trigger a
		// sendMessage — we count sendMessage separately via
		// the path.
		if strings.Contains(r.URL.Path, "sendMessage") {
			t.Errorf("sendMessage should not be called for empty body")
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1,"date":1,"chat":{"id":1,"type":"private"},"text":"ok"}}`))
	}))
	defer mock.Close()
	api := newMockedBotAPI(t, mock)

	d := newDeliverer()
	if err := d.DeliverText(context.Background(), api, 42, "", ""); err != nil {
		t.Fatalf("DeliverText(empty): %v", err)
	}
	if err := d.DeliverReport(context.Background(), api, 42, "", "", nil); err != nil {
		t.Fatalf("DeliverReport(empty): %v", err)
	}
}

func TestDeliverer_NilBotIsNoOp(t *testing.T) {
	d := newDeliverer()
	// All methods must tolerate a nil bot without panicking.
	if err := d.DeliverText(context.Background(), nil, 42, "h", "b"); err != nil {
		t.Errorf("DeliverText(nil bot): %v", err)
	}
	if err := d.DeliverReport(context.Background(), nil, 42, "h", "b", nil); err != nil {
		t.Errorf("DeliverReport(nil bot): %v", err)
	}
	if err := d.DeliverJSON(context.Background(), nil, 42, "h", map[string]int{"a": 1}); err != nil {
		t.Errorf("DeliverJSON(nil bot): %v", err)
	}
}

func TestChunkMessageHTML_RespectsMax(t *testing.T) {
	body := strings.Repeat("a ", MaxMessageChars+100)
	chunks := chunkMessageHTML(body)
	if len(chunks) == 0 {
		t.Fatal("expected chunks")
	}
	for i, c := range chunks {
		if len(c) > MaxMessageChars {
			t.Errorf("chunk %d len %d > max %d", i, len(c), MaxMessageChars)
		}
	}
}

func TestChunkMessageHTML_EmptyBody(t *testing.T) {
	if got := chunkMessageHTML(""); got != nil {
		t.Errorf("empty body should produce nil; got %v", got)
	}
	if got := chunkMessageHTML("   \n\n  "); got != nil {
		t.Errorf("whitespace body should produce nil; got %v", got)
	}
}

func TestChunkMessageHTML_ShortBodySingleChunk(t *testing.T) {
	body := "hello world"
	chunks := chunkMessageHTML(body)
	if len(chunks) != 1 {
		t.Errorf("short body should be 1 chunk, got %d", len(chunks))
	}
}

func TestDeliverer_ReportWithCitations(t *testing.T) {
	// Capture all sendMessage bodies so we can verify the
	// sources block is included.
	var bodies []string
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		bodies = append(bodies, r.Form.Get("text"))
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1,"date":1,"chat":{"id":1,"type":"private"},"text":"ok"}}`))
	}))
	defer mock.Close()
	api := newMockedBotAPI(t, mock)

	d := newDeliverer()
	cites := []Citation{
		{URL: "https://example.com/a", Title: "A"},
	}
	err := d.DeliverReport(context.Background(), api, 42, "", "the report", cites)
	if err != nil {
		t.Fatalf("DeliverReport: %v", err)
	}
	joined := strings.Join(bodies, "\n")
	if !strings.Contains(joined, "Sources") {
		t.Errorf("expected Sources block; got %q", joined)
	}
	if !strings.Contains(joined, "example.com/a") {
		t.Errorf("expected source link; got %q", joined)
	}
}
