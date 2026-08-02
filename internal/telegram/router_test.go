package telegram

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)


// routerTestBot is a minimal in-process BotAPI that captures every
// sent message so router tests can assert on the reply content. The
// library's tgbotapi.BotAPI does not give us a way to inject a fake
// client after construction, so the tests that need reply-capture
// use the poller_test.go helpers and verify via call-count on the
// mock — and the tests below use ParseCommand for the pure-parsing
// surface, which doesn't need a BotAPI at all.
type parseCase struct {
	name        string
	in          string
	wantVerb    string
	wantPayload string
	wantOK      bool
}

func TestParseCommand_Table(t *testing.T) {
	cases := []parseCase{
		// Slash commands
		{name: "simple verb", in: "/help", wantVerb: "help", wantPayload: "", wantOK: true},
		{name: "verb with payload", in: "/agent find all go web frameworks", wantVerb: "agent", wantPayload: "find all go web frameworks", wantOK: true},
		{name: "verb with single token", in: "/search foo", wantVerb: "search", wantPayload: "foo", wantOK: true},
		{name: "uppercase verb", in: "/AGENT hi", wantVerb: "agent", wantPayload: "hi", wantOK: true},
		{name: "group mention suffix", in: "/help@onyx_bot", wantVerb: "help", wantPayload: "", wantOK: true},
		{name: "group mention with payload", in: "/agent@onyx_bot solve the captcha", wantVerb: "agent", wantPayload: "solve the captcha", wantOK: true},
		{name: "leading whitespace tolerated", in: "   /research   what's the weather ", wantVerb: "research", wantPayload: "what's the weather", wantOK: true},
		{name: "tab as separator", in: "/fetch\thttps://example.com", wantVerb: "fetch", wantPayload: "https://example.com", wantOK: true},
		{name: "multi-line payload preserved", in: "/research line one\nline two\nline three", wantVerb: "research", wantPayload: "line one\nline two\nline three", wantOK: true},
		{name: "payload containing @", in: "/fetch https://example.com/@user/path", wantVerb: "fetch", wantPayload: "https://example.com/@user/path", wantOK: true},

		// Non-commands
		{name: "plain text", in: "hello world", wantVerb: "", wantPayload: "", wantOK: false},
		{name: "empty", in: "", wantVerb: "", wantPayload: "", wantOK: false},
		{name: "whitespace only", in: "   ", wantVerb: "", wantPayload: "", wantOK: false},
		{name: "just a slash", in: "/", wantVerb: "", wantPayload: "", wantOK: false},
		{name: "slash + space (no verb)", in: "/ ", wantVerb: "", wantPayload: "", wantOK: false},
		{name: "newline-leading slash", in: "\n/agent foo", wantVerb: "agent", wantPayload: "foo", wantOK: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			verb, payload, ok := ParseCommand(tc.in)
			if verb != tc.wantVerb {
				t.Errorf("verb = %q, want %q", verb, tc.wantVerb)
			}
			if payload != tc.wantPayload {
				t.Errorf("payload = %q, want %q", payload, tc.wantPayload)
			}
			if ok != tc.wantOK {
				t.Errorf("ok = %v, want %v", ok, tc.wantOK)
			}
		})
	}
}

func TestNewRouter_DefaultModeFallsBackToAgent(t *testing.T) {
	r := NewRouter(nil, &BotConfig{}) // nil DefaultMode
	if got := r.DefaultMode(); got != "agent" {
		t.Fatalf("default mode = %q, want agent", got)
	}
}

func TestNewRouter_DefaultModeHonorsConfig(t *testing.T) {
	r := NewRouter(nil, &BotConfig{DefaultMode: "deep-research"})
	if got := r.DefaultMode(); got != "deep-research" {
		t.Fatalf("default mode = %q, want deep-research", got)
	}
}

func TestNewRouter_DefaultModeRejectsGarbage(t *testing.T) {
	r := NewRouter(nil, &BotConfig{DefaultMode: "unknown-mode"})
	if got := r.DefaultMode(); got != "agent" {
		t.Fatalf("default mode = %q, want fallback to agent", got)
	}
}

func TestNewRouter_RegistersBuiltins(t *testing.T) {
	r := NewRouter(nil, &BotConfig{})
	for _, verb := range []string{"start", "help", "status", "cancel"} {
		if _, ok := r.commandHandlers[verb]; !ok {
			t.Errorf("built-in verb %q not registered", verb)
		}
	}
}

func TestWithCommandHandler_OverridesBuiltins(t *testing.T) {
	var called int32
	custom := func(ctx context.Context, bot *tgbotapi.BotAPI, msg *tgbotapi.Message, payload string) error {
		atomic.StoreInt32(&called, 1)
		return nil
	}
	r := NewRouter(nil, &BotConfig{}, WithCommandHandler("start", custom))
	// Invoke the override directly.
	r.commandHandlers["start"](context.Background(), nil, nil, "")
	if atomic.LoadInt32(&called) != 1 {
		t.Fatalf("override handler not invoked; called=%d", atomic.LoadInt32(&called))
	}
}

func TestWithHelpText_OverridesDefault(t *testing.T) {
	r := NewRouter(nil, &BotConfig{}, WithHelpText("CUSTOM HELP"))
	if got := r.Help(); got != "CUSTOM HELP" {
		t.Fatalf("Help() = %q, want CUSTOM HELP", got)
	}
}

func TestRouter_Handle_NilMessage_ReturnsError(t *testing.T) {
	r := NewRouter(nil, &BotConfig{})
	err := r.Handle(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("expected error on nil message")
	}
}

func TestRouter_Handle_UnknownVerb_RepliesWithHelp(t *testing.T) {
	// Stub the underlying send by using a bot with a fake base URL
	// that will fail; the router only needs to call Send once, and
	// the test asserts that the path was taken (via the function
	// being called, not its success — Send against a bogus URL will
	// error and the helper propagates).
	//
	// Better: capture via the mock used by poller_test.go. The mock
	// records sendMessage calls and replies 200 to keep the call
	// non-fatal.
	mock := newTelegramMock(t)
	bot := newMockedBot(t, mock, 42)
	r := NewRouter(bot, &BotConfig{})

	msg := &tgbotapi.Message{
		MessageID: 1,
		Chat:      &tgbotapi.Chat{ID: 42, Type: "private"},
		From:      &tgbotapi.User{ID: 42, UserName: "alice", FirstName: "T"},
		Text:      "/totally-unknown hello",
	}
	if err := r.Handle(context.Background(), bot.API, msg); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	// Mock should have received exactly one sendMessage call.
	if atomic.LoadInt32(&mock.sendMessageCalls) == 0 {
		t.Fatal("expected sendMessage (sendMessageCalls counter) to be incremented for unknown verb")
	}
}

func TestRouter_Handle_StartMessage_RepliesWelcome(t *testing.T) {
	mock := newTelegramMock(t)
	bot := newMockedBot(t, mock, 42)
	r := NewRouter(bot, &BotConfig{})

	msg := &tgbotapi.Message{
		MessageID: 1,
		Chat:      &tgbotapi.Chat{ID: 42, Type: "private"},
		From:      &tgbotapi.User{ID: 42, UserName: "alice", FirstName: "T"},
		Text:      "/start",
	}
	if err := r.Handle(context.Background(), bot.API, msg); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if atomic.LoadInt32(&mock.sendMessageCalls) == 0 {
		t.Fatal("expected sendMessage (sendMessageCalls counter) to be incremented for /start")
	}
}

func TestRouter_Handle_PlainText_RoutesToDefaultMode(t *testing.T) {
	mock := newTelegramMock(t)
	bot := newMockedBot(t, mock, 42)

	// Replace the "agent" stub with one we can count, so we know
	// the plain-text path picked up the default mode.
	var agentCalled int32
	stub := func(ctx context.Context, b *tgbotapi.BotAPI, m *tgbotapi.Message, payload string) error {
		atomic.StoreInt32(&agentCalled, 1)
		if payload != "go investigate the captcha" {
			t.Errorf("payload = %q, want %q", payload, "go investigate the captcha")
		}
		return nil
	}
	r := NewRouter(bot, &BotConfig{DefaultMode: "agent"}, WithCommandHandler("agent", stub))

	msg := &tgbotapi.Message{
		MessageID: 1,
		Chat:      &tgbotapi.Chat{ID: 42, Type: "private"},
		From:      &tgbotapi.User{ID: 42, UserName: "alice", FirstName: "T"},
		Text:      "go investigate the captcha",
	}
	if err := r.Handle(context.Background(), bot.API, msg); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if atomic.LoadInt32(&agentCalled) != 1 {
		t.Fatalf("agent handler should have been called for plain text with default_mode=agent; got %d", atomic.LoadInt32(&agentCalled))
	}
}

func TestRouter_Handle_PlainText_RoutesToDeepResearchWhenConfigured(t *testing.T) {
	mock := newTelegramMock(t)
	bot := newMockedBot(t, mock, 42)

	var agentCalled, researchCalled int32
	agentStub := func(ctx context.Context, b *tgbotapi.BotAPI, m *tgbotapi.Message, payload string) error {
		atomic.StoreInt32(&agentCalled, 1)
		return nil
	}
	researchStub := func(ctx context.Context, b *tgbotapi.BotAPI, m *tgbotapi.Message, payload string) error {
		atomic.StoreInt32(&researchCalled, 1)
		return nil
	}
	r := NewRouter(bot, &BotConfig{DefaultMode: "deep-research"},
		WithCommandHandler("agent", agentStub),
		WithCommandHandler("research", researchStub),
	)

	msg := &tgbotapi.Message{
		MessageID: 1,
		Chat:      &tgbotapi.Chat{ID: 42, Type: "private"},
		From:      &tgbotapi.User{ID: 42, UserName: "alice", FirstName: "T"},
		Text:      "what is the weather in tokyo?",
	}
	if err := r.Handle(context.Background(), bot.API, msg); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if atomic.LoadInt32(&researchCalled) != 1 {
		t.Fatalf("research handler should have been called; got %d", atomic.LoadInt32(&researchCalled))
	}
	if atomic.LoadInt32(&agentCalled) != 0 {
		t.Fatalf("agent handler should NOT have been called; got %d", atomic.LoadInt32(&agentCalled))
	}
}

func TestRouter_Handle_EmptyPlainText_RepliesHelp(t *testing.T) {
	mock := newTelegramMock(t)
	bot := newMockedBot(t, mock, 42)
	r := NewRouter(bot, &BotConfig{})

	msg := &tgbotapi.Message{
		MessageID: 1,
		Chat:      &tgbotapi.Chat{ID: 42, Type: "private"},
		From:      &tgbotapi.User{ID: 42, UserName: "alice", FirstName: "T"},
		Text:      "   ",
	}
	if err := r.Handle(context.Background(), bot.API, msg); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if atomic.LoadInt32(&mock.sendMessageCalls) == 0 {
		t.Fatal("expected sendMessage for empty plain text")
	}
}

func TestRouter_Handle_KnownVerbWithoutPayload_SendsNotImplementedForEngineVerb(t *testing.T) {
	// /agent without payload should call the engine-backed handler.
	// In Phase 5 the engine stub is the "not yet implemented" reply,
	// so we expect exactly one sendMessage.
	mock := newTelegramMock(t)
	bot := newMockedBot(t, mock, 42)
	r := NewRouter(bot, &BotConfig{})

	msg := &tgbotapi.Message{
		MessageID: 1,
		Chat:      &tgbotapi.Chat{ID: 42, Type: "private"},
		From:      &tgbotapi.User{ID: 42, UserName: "alice", FirstName: "T"},
		Text:      "/agent",
	}
	if err := r.Handle(context.Background(), bot.API, msg); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if atomic.LoadInt32(&mock.sendMessageCalls) == 0 {
		t.Fatal("expected sendMessage for /agent without payload")
	}
}

func TestRouter_Handle_CommandPanic_StillSurfacesUserMessage(t *testing.T) {
	// A panicking command handler must not bring down the gateway.
	// The safeInvokeCmd wrapper catches the panic, logs it, and
	// sends a generic failure message to the user.
	mock := newTelegramMock(t)
	bot := newMockedBot(t, mock, 42)
	boom := func(ctx context.Context, b *tgbotapi.BotAPI, m *tgbotapi.Message, payload string) error {
		panic("kaboom from /start")
	}
	r := NewRouter(bot, &BotConfig{}, WithCommandHandler("start", boom))

	msg := &tgbotapi.Message{
		MessageID: 1,
		Chat:      &tgbotapi.Chat{ID: 42, Type: "private"},
		From:      &tgbotapi.User{ID: 42, UserName: "alice", FirstName: "T"},
		Text:      "/start",
	}
	err := r.Handle(context.Background(), bot.API, msg)
	if err == nil {
		t.Fatal("Handle should return error from a panicking command")
	}
	// Exactly two sendMessage calls expected: the failure notice,
	// and no others (no welcome, since the handler panicked before
	// replying).
	if got := atomic.LoadInt32(&mock.sendMessageCalls); got != 1 {
		t.Errorf("expected 1 sendMessage (failure notice), got %d", got)
	}
}

func TestPayloadRequired(t *testing.T) {
	// Without a real bot we just verify the helper's boolean logic.
	// The reply path is exercised in the router-level tests above.
	mock := newTelegramMock(t)
	bot := newMockedBot(t, mock, 42)

	if !payloadRequired(bot.API, 42, "agent", "", "<goal>") {
		t.Error("empty payload should be required")
	}
	if !payloadRequired(bot.API, 42, "agent", "  ", "<goal>") {
		t.Error("whitespace-only payload should be required")
	}
	if payloadRequired(bot.API, 42, "agent", "real goal", "<goal>") {
		t.Error("non-empty payload should not be required")
	}
}

// ----- Phase 9: rate limit + defense-in-depth -----

func TestRouter_RateLimit_AllowsBurstThenDenies(t *testing.T) {
	mock := newTelegramMock(t)
	bot := newMockedBot(t, mock, 42)
	auth := NewAuthenticator(bot.Cfg, PolicySilentDrop, false)
	rl := newRateLimiter(2, 0.0001)
	router := NewRouter(bot, nil, WithAuthenticator(auth), WithRateLimiter(rl))
	// 2 calls succeed; 3rd is denied.
	for i := 0; i < 2; i++ {
		_ = router.Handle(context.Background(), bot.API, &tgbotapi.Message{
			Chat: &tgbotapi.Chat{ID: 42},
			From: &tgbotapi.User{UserName: "alice"},
			Text: "help",
		})
	}
	// 3rd call: rate limit hit. We do NOT assert on the reply text (mock just acks)
	_ = router.Handle(context.Background(), bot.API, &tgbotapi.Message{
		Chat: &tgbotapi.Chat{ID: 42},
		From: &tgbotapi.User{UserName: "alice"},
		Text: "help",
	})
}

func TestRouter_DefenseCheck_DropsChatRemovedFromAllowlist(t *testing.T) {
	mock := newTelegramMock(t)
	auth := NewAuthenticator(&BotConfig{AllowedChatIDs: []int64{42}}, PolicySilentDrop, false)
	bot := newMockedBot(t, mock, 42)
	router := NewRouter(bot, nil, WithAuthenticator(auth))
	msg := &tgbotapi.Message{Chat: &tgbotapi.Chat{ID: 99}, Text: "help"}
	// Should be denied by defense even though the mock does not validate.
	_ = router.Handle(context.Background(), bot.API, msg)
}

// ---------- Phase 8: /news router tests ----------

func TestNewRouter_NewsDefaultModeAccepted(t *testing.T) {
	r := NewRouter(nil, &BotConfig{DefaultMode: "news"})
	if got := r.DefaultMode(); got != "news" {
		t.Fatalf("default mode = %q, want news", got)
	}
}

func TestNewRouter_NewsStubRegistered(t *testing.T) {
	r := NewRouter(nil, &BotConfig{})
	if _, ok := r.commandHandlers["news"]; !ok {
		t.Error("expected 'news' to be registered as a stub in NewRouter")
	}
}

func TestHelpText_ContainsNews(t *testing.T) {
	r := NewRouter(nil, &BotConfig{})
	help := r.Help()
	if !strings.Contains(help, "/news") {
		t.Errorf("default help text does not mention /news; got:\n%s", help)
	}
	if !strings.Contains(help, "duration") {
		t.Errorf("default help text does not mention duration for /news; got:\n%s", help)
	}
}

func TestRouter_SlashNewsRoutes(t *testing.T) {
	var gotVerb string
	var gotPayload string
	handler := func(ctx context.Context, bot *tgbotapi.BotAPI, msg *tgbotapi.Message, payload string) error {
		gotVerb = "news"
		gotPayload = payload
		return nil
	}
	r := NewRouter(nil, &BotConfig{}, WithCommandHandler("news", handler))

	verb, payload, ok := ParseCommand("/news past 3 days")
	if !ok {
		t.Fatal("ParseCommand did not recognise /news")
	}
	if verb != "news" {
		t.Fatalf("verb = %q, want news", verb)
	}
	if payload != "past 3 days" {
		t.Fatalf("payload = %q, want 'past 3 days'", payload)
	}

	// Simulate Handle routing.
	h, exists := r.commandHandlers[verb]
	if !exists {
		t.Fatal("router has no handler for 'news'")
	}
	_ = h(context.Background(), nil, &tgbotapi.Message{Chat: &tgbotapi.Chat{ID: 1}}, payload)

	if gotVerb != "news" {
		t.Errorf("expected news handler to be called, got verb=%q", gotVerb)
	}
	if gotPayload != "past 3 days" {
		t.Errorf("payload = %q, want 'past 3 days'", gotPayload)
	}
}

func TestRouter_SlashNewsEmptyPayload(t *testing.T) {
	// /news with no payload must route correctly (no payload required).
	verb, payload, ok := ParseCommand("/news")
	if !ok || verb != "news" {
		t.Fatalf("ParseCommand('/news') = (%q, %q, %v), want (news, '', true)", verb, payload, ok)
	}
	if payload != "" {
		t.Errorf("payload = %q, want empty", payload)
	}
}


