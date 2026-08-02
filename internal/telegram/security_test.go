package telegram

import (
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// ----- SanitizeURL -----

func TestSanitizeURL_AcceptsPlainHTTPS(t *testing.T) {
	got := SanitizeURL("https://example.com/path?x=1")
	if got == "" {
		t.Fatal("expected plain https URL to be accepted")
	}
	if !strings.HasPrefix(got, "https://example.com") {
		t.Errorf("canonical URL lost its scheme/host: %q", got)
	}
}

func TestSanitizeURL_AcceptsPlainHTTP(t *testing.T) {
	got := SanitizeURL("http://example.com")
	if got == "" {
		t.Fatal("expected plain http URL to be accepted")
	}
}

func TestSanitizeURL_RejectsEmpty(t *testing.T) {
	if got := SanitizeURL(""); got != "" {
		t.Errorf("empty URL should be rejected; got %q", got)
	}
	if got := SanitizeURL("   "); got != "" {
		t.Errorf("whitespace URL should be rejected; got %q", got)
	}
}

func TestSanitizeURL_RejectsNonHTTPSchemes(t *testing.T) {
	cases := []string{
		"file:///etc/passwd",
		"gopher://example.com/_",
		"javascript:alert(1)",
		"data:text/html,<script>alert(1)</script>",
		"ftp://example.com/file",
		"ssh://user@example.com",
	}
	for _, raw := range cases {
		if got := SanitizeURL(raw); got != "" {
			t.Errorf("scheme %q should be rejected; got %q", raw, got)
		}
	}
}

func TestSanitizeURL_RejectsLoopback(t *testing.T) {
	cases := []string{
		"http://127.0.0.1/x",
		"http://127.0.0.1:8080/admin",
		"http://localhost/x",
		"http://[::1]/x",
	}
	for _, raw := range cases {
		if got := SanitizeURL(raw); got != "" {
			t.Errorf("loopback %q should be rejected; got %q", raw, got)
		}
	}
}

func TestSanitizeURL_RejectsPrivateRFC1918(t *testing.T) {
	cases := []string{
		"http://10.0.0.1/x",
		"http://10.255.255.255/x",
		"http://172.16.0.1/x",
		"http://192.168.1.1/admin",
		"http://192.168.0.1:8080/",
	}
	for _, raw := range cases {
		if got := SanitizeURL(raw); got != "" {
			t.Errorf("private %q should be rejected; got %q", raw, got)
		}
	}
}

func TestSanitizeURL_RejectsLinkLocalAndMetadata(t *testing.T) {
	cases := []string{
		"http://169.254.169.254/latest/meta-data/", // AWS / GCP / Azure metadata
		"http://169.254.0.1/",
		"http://[fe80::1]/",
	}
	for _, raw := range cases {
		if got := SanitizeURL(raw); got != "" {
			t.Errorf("link-local / metadata %q should be rejected; got %q", raw, got)
		}
	}
}

func TestSanitizeURL_RejectsCGNAT(t *testing.T) {
	cases := []string{
		"http://100.64.0.1/x",
		"http://100.127.255.255/x",
	}
	for _, raw := range cases {
		if got := SanitizeURL(raw); got != "" {
			t.Errorf("CGNAT %q should be rejected; got %q", raw, got)
		}
	}
}

func TestSanitizeURL_RejectsZeroAndReserved(t *testing.T) {
	cases := []string{
		"http://0.0.0.0/x",
		"http://255.255.255.255/x",
		"http://240.0.0.1/x",
	}
	for _, raw := range cases {
		if got := SanitizeURL(raw); got != "" {
			t.Errorf("reserved %q should be rejected; got %q", raw, got)
		}
	}
}

func TestSanitizeURL_RejectsIPv6ULA(t *testing.T) {
	cases := []string{
		"http://[fc00::1]/x",
		"http://[fd12:3456::1]/x",
	}
	for _, raw := range cases {
		if got := SanitizeURL(raw); got != "" {
			t.Errorf("IPv6 ULA %q should be rejected; got %q", raw, got)
		}
	}
}

func TestSanitizeURL_RejectsUnresolvable(t *testing.T) {
	// We use a clearly-invalid TLD so the resolver fails fast
	// (the .invalid TLD is reserved by RFC 2606 to NEVER
	// resolve). A live DNS lookup that hangs on misconfigured
	// test boxes is the failure mode we want to avoid, so we
	// keep the timeout implicit in the resolver's own
	// behaviour.
	if got := SanitizeURL("https://nonexistent.invalid/"); got != "" {
		t.Errorf("unresolvable host should be rejected; got %q", got)
	}
}

func TestSanitizeURL_StripsUserInfo(t *testing.T) {
	got := SanitizeURL("https://user:pass@example.com/path")
	if got == "" {
		t.Fatal("expected URL to be accepted; the only issue is the user:pass")
	}
	if strings.Contains(got, "user:pass") || strings.Contains(got, "@example.com") {
		t.Errorf("user info not stripped: %q", got)
	}
}

func TestSanitizeURL_DropsFragment(t *testing.T) {
	got := SanitizeURL("https://example.com/path#fragment")
	if strings.Contains(got, "#") {
		t.Errorf("fragment not stripped: %q", got)
	}
}

func TestSanitizeURL_AcceptsLiteralPublicIP(t *testing.T) {
	// 8.8.8.8 is a public Google DNS resolver — used here only
	// to exercise the "literal IP, not blocked" path. The test
	// does not actually dial it.
	if got := SanitizeURL("https://8.8.8.8/dns-query"); got == "" {
		t.Errorf("public IP should be accepted")
	}
}

// ----- isBlockedIP -----

func TestIsBlockedIP_KnownRanges(t *testing.T) {
	blocked := []string{
		"127.0.0.1",
		"10.0.0.1",
		"172.16.0.1",
		"192.168.0.1",
		"169.254.169.254",
		"100.64.0.1",
		"0.0.0.0",
		"255.255.255.255",
		"::1",
		"fe80::1",
		"fc00::1",
	}
	for _, raw := range blocked {
		ip := parseIPForTest(raw)
		if !isBlockedIP(ip) {
			t.Errorf("expected %s to be blocked", raw)
		}
	}
}

func TestIsBlockedIP_PublicRanges(t *testing.T) {
	allowed := []string{
		"8.8.8.8",
		"1.1.1.1",
		"2001:4860:4860::8888",
	}
	for _, raw := range allowed {
		ip := parseIPForTest(raw)
		if isBlockedIP(ip) {
			t.Errorf("expected %s to be allowed", raw)
		}
	}
}

// parseIPForTest is a tiny shim so the table-driven tests above can
// hand us raw IP strings.
func parseIPForTest(s string) net.IP {
	return net.ParseIP(s)
}

// ----- rateLimiter -----

func TestRateLimiter_AllowsConfiguredBurst(t *testing.T) {
	// Pin the clock to a fixed time so refill is exactly 0
	// between calls. Without this, wall-clock progression
	// during the test could refill tokens and let the 4th
	// call succeed.
	rl := newRateLimiter(3, 0.0001)
	rl.nowFn = func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }
	chatID := int64(100)
	for i := 0; i < 3; i++ {
		if !rl.Allow(chatID) {
			t.Fatalf("Allow #%d should have succeeded (burst=3)", i+1)
		}
	}
	// 4th call must be denied.
	if rl.Allow(chatID) {
		t.Fatal("4th call should have been denied (burst exhausted)")
	}
}

func TestRateLimiter_RefillsOverTime(t *testing.T) {
	// 2 tokens per second, starting burst 2.
	rl := newRateLimiter(2, 2.0)
	chatID := int64(200)

	// Pin the clock to a known time. The limiter uses
	// lastRefillT (a time.Time) for its deltas, so we move
	// the clock by 1s to add 2 tokens back.
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rl.nowFn = func() time.Time { return now }

	// Burn the burst.
	if !rl.Allow(chatID) {
		t.Fatal("first Allow should succeed")
	}
	if !rl.Allow(chatID) {
		t.Fatal("second Allow should succeed")
	}
	if rl.Allow(chatID) {
		t.Fatal("third Allow should fail (bucket empty)")
	}

	// Move clock forward 1s; refill rate is 2/s so we get 2 tokens.
	now = now.Add(1 * time.Second)
	if !rl.Allow(chatID) {
		t.Fatal("after 1s refill, Allow should succeed")
	}
	if !rl.Allow(chatID) {
		t.Fatal("after 1s refill, second Allow should succeed")
	}
	if rl.Allow(chatID) {
		t.Fatal("third Allow after refill should fail again")
	}
}

func TestRateLimiter_IndependentPerChat(t *testing.T) {
	rl := newRateLimiter(1, 0.0001)
	chatA, chatB := int64(300), int64(301)
	if !rl.Allow(chatA) {
		t.Fatal("chatA first Allow should succeed")
	}
	// chatA is now empty, but chatB is fresh.
	if !rl.Allow(chatB) {
		t.Fatal("chatB first Allow should succeed (independent bucket)")
	}
	// chatA is still empty.
	if rl.Allow(chatA) {
		t.Fatal("chatA second Allow should fail (no refill yet)")
	}
}

func TestRateLimiter_DefaultsWhenZeroArgs(t *testing.T) {
	rl := newRateLimiter(0, 0) // both should fall back to defaults
	if rl.burst != DefaultRateBurst {
		t.Errorf("expected default burst %d, got %d", DefaultRateBurst, rl.burst)
	}
	if rl.refillPS != DefaultRateRefillPerSec {
		t.Errorf("expected default refill %v, got %v", DefaultRateRefillPerSec, rl.refillPS)
	}
}

func TestRateLimiter_ConcurrentSafety(t *testing.T) {
	rl := newRateLimiter(100, 100.0)
	chatID := int64(999)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rl.Allow(chatID)
		}()
	}
	wg.Wait()
	// We just need to not race; the bucket may be empty or not
	// after 50 concurrent calls, but the function must not
	// deadlock or panic. This is a smoke test, not a strict
	// count.
}

// ----- token redaction -----

func TestRedactToken_RemovesCanonicalToken(t *testing.T) {
	s := "request failed: bot token 1234567890:AAEhBOweik6ad9J3SomethingSomething_elE is invalid"
	got := RedactToken(s)
	if strings.Contains(got, "AAEhBOweik6ad9J3") {
		t.Errorf("token not redacted: %q", got)
	}
	if !strings.Contains(got, "***") {
		t.Errorf("expected *** placeholder; got %q", got)
	}
	// The rest of the message should be preserved.
	if !strings.Contains(got, "request failed") {
		t.Errorf("surrounding text lost: %q", got)
	}
}

func TestRedactToken_HandlesEmpty(t *testing.T) {
	if got := RedactToken(""); got != "" {
		t.Errorf("empty input should return empty; got %q", got)
	}
}

func TestRedactToken_NoFalsePositiveOnShortStrings(t *testing.T) {
	// A "1:1" string should not match (too short).
	if got := RedactToken("see 1:1 docs"); got != "see 1:1 docs" {
		t.Errorf("short colon-string should be left alone; got %q", got)
	}
}

func TestRedactToken_RedactsMultipleOccurrences(t *testing.T) {
	s := "first 1234567890:AAEhBOweik6ad9J3abcdefghij_klmN and second 9876543210:BBEhBOweik6ad9J3zyxwvu_first"
	got := RedactToken(s)
	if strings.Contains(got, "AAEhBOweik6ad9J3") || strings.Contains(got, "BBEhBOweik6ad9J3") {
		t.Errorf("one of the tokens not redacted: %q", got)
	}
}

func TestRedactToken_HandlesInErrorMessage(t *testing.T) {
	// This is the actual use case for shortUserError: an
	// error string from the Telegram library that may
	// include the token in the message.
	err := &testErr{msg: "Unauthorized: 1234567890:AAEhBOweik6ad9J3SomethingElse12345xY"}
	got := RedactToken(err.Error())
	if strings.Contains(got, "AAEhBOweik6ad9J3") {
		t.Errorf("error message not redacted: %q", got)
	}
}

type testErr struct{ msg string }

func (e *testErr) Error() string { return e.msg }

// ----- DefenseCheck + IsAllowed -----

func TestDefenseCheck_AllowsChatInList(t *testing.T) {
	auth := NewAuthenticator(&BotConfig{AllowedChatIDs: []int64{42}}, PolicySilentDrop, false)
	if !DefenseCheck(auth, 42, "alice") {
		t.Error("DefenseCheck should allow chat 42")
	}
}

func TestDefenseCheck_RejectsChatNotInList(t *testing.T) {
	auth := NewAuthenticator(&BotConfig{AllowedChatIDs: []int64{42}}, PolicySilentDrop, false)
	if DefenseCheck(auth, 99, "eve") {
		t.Error("DefenseCheck should reject chat 99")
	}
}

func TestDefenseCheck_NilAuthReturnsTrue(t *testing.T) {
	// With no authenticator wired, the defense check is a
	// no-op (returns true). This is the "feature not enabled"
	// path so existing builds that don't wire auth don't
	// silently break.
	if !DefenseCheck(nil, 42, "alice") {
		t.Error("nil auth should return true (defense not enabled)")
	}
}

func TestAuthenticator_IsAllowed_UsernameCaseInsensitive(t *testing.T) {
	a := NewAuthenticator(&BotConfig{AllowedUsernames: []string{"alice"}}, PolicySilentDrop, false)
	if !a.IsAllowed(AllowlistIdentity{ChatID: 1, Username: "ALICE"}) {
		t.Error("username match should be case-insensitive")
	}
	if a.IsAllowed(AllowlistIdentity{ChatID: 1, Username: "eve"}) {
		t.Error("unknown username should be denied")
	}
}

func TestAuthenticator_IsAllowed_EmptyAllowlist_RespectsOptIn(t *testing.T) {
	closed := NewAuthenticator(&BotConfig{}, PolicySilentDrop, false)
	if closed.IsAllowed(AllowlistIdentity{ChatID: 1}) {
		t.Error("fail-closed: empty allowlist must deny without opt-in")
	}
	open := NewAuthenticator(&BotConfig{}, PolicySilentDrop, true)
	if !open.IsAllowed(AllowlistIdentity{ChatID: 1}) {
		t.Error("opt-in: empty allowlist must allow with allowEmptyList=true")
	}
}
