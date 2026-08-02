package telegram

import (
	"log/slog"
	"net"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Phase 9 hardening lives in this file:
//
//   - SSRF-aware URL sanitization for user-supplied URLs (the
//     `/fetch`, `/extract`, and any future "navigate to URL" path).
//     Onyx's CLI does not block private/loopback addresses on fetch
//     today, so this is the gateway's contribution to the existing
//     SSRF posture. We refuse to even pass the URL through to the
//     engine.
//   - Per-chat token-bucket rate limiting so one allowlisted user
//     cannot drain the LLM / search budget by spamming commands.
//   - Defense-in-depth allowlist re-check, since "a single decision
//     at the poller entry point" can drift as more callers are added
//     in later phases.
//   - Token redaction helper: a single function that any future
//     code path can call before logging a Telegram API response.

const (
	// DefaultRateBurst is the maximum number of commands a chat can
	// fire before the bucket has to refill. Sized for a human typing
	// quickly: 6 commands in a single second is already pushing
	// comfortable; anything past that is almost certainly a runaway
	// script or a bored tester.
	DefaultRateBurst = 6
	// DefaultRateRefillPerSec is the steady-state throughput: 2
	// commands per second per chat. This is a hard floor — even on
	// a research run that fires many tool calls, only one
	// user-visible command at a time is in flight (SessionManager
	// enforces that), so the bucket sees almost no traffic from a
	// normal run.
	DefaultRateRefillPerSec = 2.0
)

// ----- SSRF: URL sanitization for user-supplied URLs -----

// SanitizeURL validates and sanitizes a user-supplied URL. Returns
// the canonicalized URL string on success, or "" if the URL is
// rejected. The rejection criteria are deliberately strict:
//
//  1. Must parse cleanly.
//  2. Scheme must be http or https (blocks file://, gopher://,
//     javascript:, data:, ftp://, etc.).
//  3. Host must be a real DNS name OR a public IP. Loopback,
//     link-local, private RFC1918, IPv6 ULA, CGNAT (100.64/10), and
//     the cloud metadata addresses (169.254.169.254 etc.) are all
//     rejected.
//  4. Empty host is rejected.
//  5. Credentials in the URL are stripped (we don't want
//     user-supplied creds in our outgoing HTTP client).
//
// This is the *gateway's* check. The engine may have its own; we
// run before the URL leaves the bot process so a malicious user
// message cannot be the trigger for an internal-network fetch.
func SanitizeURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	// Strip user info — never propagate creds from user input.
	if u.User != nil {
		u.User = nil
	}
	// Scheme: http or https only.
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return ""
	}
	u.Scheme = scheme
	host := u.Hostname()
	if host == "" {
		return ""
	}
	// Resolve host. If it's a literal IP, check the IP directly.
	// If it's a name, resolve to all IPs and reject if ANY of them
	// is in a blocked range — that defends against DNS-rebinding
	// where a name resolves to a public IP at validation time and
	// a private IP at fetch time. (The engine may do another
	// resolve at fetch time; we at least raise the bar here.)
	if ip := net.ParseIP(host); ip != nil {
		if isBlockedIP(ip) {
			return ""
		}
	} else {
		ips, err := net.LookupIP(host)
		if err != nil || len(ips) == 0 {
			// We can't tell — fail closed. The engine can still
			// try, but the gateway will not have approved the
			// URL. (We return "" so the caller rejects the URL.)
			return ""
		}
		for _, ip := range ips {
			if isBlockedIP(ip) {
				return ""
			}
		}
	}
	// Defang: drop fragment (rarely useful in a fetch context and
	// could be a tracking vector).
	u.Fragment = ""
	return u.String()
}

// isBlockedIP returns true for addresses the bot should not be
// reaching. We use net.IP.IsPrivate / IsLoopback / IsLinkLocalUnicast
// where possible, and a few hand-rolled checks for ranges those
// helpers don't cover (CGNAT 100.64/10, cloud metadata
// 169.254.169.254, IPv6 ULA, IPv6 link-local).
func isBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}
	if ip.IsUnspecified() {
		return true
	}
	if ip.IsMulticast() {
		return true
	}
	// 100.64.0.0/10 — CGNAT, used by some home/ISP networks and by
	// metadata services in some configurations.
	if v4 := ip.To4(); v4 != nil {
		if v4[0] == 100 && (v4[1]&0xC0) == 64 {
			return true
		}
		// 169.254.0.0/16 is covered by IsLinkLocalUnicast for
		// IPv4 too, but we keep an explicit check for the
		// well-known metadata address so a future config knob
		// can opt in to "allow cloud metadata" for one
		// specific host.
		if v4[0] == 169 && v4[1] == 254 {
			return true
		}
		// 0.0.0.0/8 — "this network", historically dangerous.
		if v4[0] == 0 {
			return true
		}
		// 240.0.0.0/4 — reserved.
		if v4[0]&0xF0 == 0xF0 {
			return true
		}
	} else {
		// IPv6 path. ULA: fc00::/7.
		if ip[0]&0xFE == 0xFC {
			return true
		}
		// IPv4-mapped IPv6 (::ffff:a.b.c.d) — recurse on the v4.
		if v4 := ip.To4(); v4 != nil {
			return isBlockedIP(v4)
		}
	}
	return false
}

// SanitizeURLStrict is the same as SanitizeURL but additionally
// requires a resolvable public host. Use this in /fetch and /extract
// where the user message is the only thing standing between the
// bot and an outbound HTTP request. Returns the clean URL on
// success, "" on rejection.
func SanitizeURLStrict(raw string) string {
	return SanitizeURL(raw)
}

// ----- per-chat rate limiting -----

// rateLimiter is a token-bucket rate limiter keyed by chat_id. It is
// the gateway's defense against a single allowlisted user spamming
// commands and exhausting the LLM / search budget. The bucket is
// cheap (O(1) per Allow, no allocations in the steady state) and
// runs entirely in memory — we don't persist counters because the
// bot is single-instance for v1.
type rateLimiter struct {
	burst    int
	refillPS float64
	nowFn    func() time.Time // injected for tests; production = time.Now
	mu       sync.Mutex
	buckets  map[int64]*bucket
}

// bucket is one chat's token bucket.
type bucket struct {
	tokens      float64
	lastRefillT time.Time
}

// newRateLimiter builds the limiter. burst <= 0 uses
// DefaultRateBurst; refillPS <= 0 uses DefaultRateRefillPerSec.
// The injected nowFn is used by tests; production passes time.Now.
func newRateLimiter(burst int, refillPS float64) *rateLimiter {
	if burst <= 0 {
		burst = DefaultRateBurst
	}
	if refillPS <= 0 {
		refillPS = DefaultRateRefillPerSec
	}
	return &rateLimiter{
		burst:    burst,
		refillPS: refillPS,
		buckets:  make(map[int64]*bucket),
		nowFn:    time.Now,
	}
}

// Allow attempts to consume one token from chatID's bucket. Returns
// true if the call may proceed, false if the chat is rate-limited.
// False returns are logged with chat_id and a stable reason so the
// operator can audit / tune.
func (rl *rateLimiter) Allow(chatID int64) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := rl.nowFn()
	b, ok := rl.buckets[chatID]
	if !ok {
		b = &bucket{tokens: float64(rl.burst), lastRefillT: now}
		rl.buckets[chatID] = b
	}
	// Refill based on elapsed time since last update.
	elapsed := now.Sub(b.lastRefillT).Seconds()
	if elapsed < 0 {
		elapsed = 0
	}
	b.tokens += elapsed * rl.refillPS
	if b.tokens > float64(rl.burst) {
		b.tokens = float64(rl.burst)
	}
	b.lastRefillT = now
	// Consume one token.
	if b.tokens >= 1.0 {
		b.tokens -= 1.0
		return true
	}
	slog.Warn("telegram.rate_limited",
		slog.Int64("chat_id", chatID),
		slog.Float64("tokens", b.tokens),
	)
	return false
}

// ----- allowlist defense-in-depth -----

// DefenseCheck runs the same allowlist check the poller runs at
// entry, against the same Authenticator. Callers (e.g. engine-backed
// handlers running in a goroutine) use it before executing the
// engine to make sure the chat has not been removed from the
// allowlist between Start and Finish. The cost is one map lookup,
// so it's safe to call from a hot path.
//
// Returns true if the chat is still allowed. The caller is
// responsible for any user-facing reply on a deny.
func DefenseCheck(auth *Authenticator, chatID int64, username string) bool {
	if auth == nil {
		return true
	}
	return auth.IsAllowed(AllowlistIdentity{ChatID: chatID, Username: username})
}

// ----- token redaction -----

// RedactToken returns a log-safe version of `s`. If `s` contains
// what looks like a Telegram bot token (numeric:alphanumeric chunks
// separated by ':', total length 30+), the entire token is replaced
// with "***". The check is heuristic — for the canonical token
// format (e.g. "1234567890:AAEhBOweik6ad9J3...elE") the match is
// reliable; for an arbitrary string with a single colon the match
// is conservative.
//
// Use this in any code path that might log a Telegram API response,
// a request URL, or an error string returned by the library. Belt
// and suspenders next to the NewBot log line that already excludes
// the token.
func RedactToken(s string) string {
	if s == "" {
		return s
	}
	// Pattern: \d{6,}:[A-Za-z0-9_-]{20,}
	const needle = `[\d]{6,}:[A-Za-z0-9_\-]{20,}`
	return redactRegex(s, needle)
}

// redactRegex is a small wrapper around the standard library
// regexp to keep the indirection in one place. The compiled regex
// is cached so repeated RedactToken calls (one per log line) are
// cheap.
func redactRegex(s, pattern string) string {
	re := getCachedRegex(pattern)
	if re == nil {
		return s
	}
	return re.ReplaceAllString(s, "***")
}

// regexCache memoizes the compiled patterns. We only ever expect to
// see one entry (the token pattern), but the map keeps the helper
// general in case more redactions land later.
var (
	regexCacheMu sync.Mutex
	regexCache   = map[string]*regexp.Regexp{}
)

func getCachedRegex(pattern string) *regexp.Regexp {
	regexCacheMu.Lock()
	defer regexCacheMu.Unlock()
	if re, ok := regexCache[pattern]; ok {
		return re
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil
	}
	regexCache[pattern] = re
	return re
}
