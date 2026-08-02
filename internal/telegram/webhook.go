package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// telegramSecretHeader is the HTTP header Telegram sends with every
// webhook POST when the operator has configured a secret_token via
// the setWebhook API. Spec: https://core.telegram.org/bots/api#setwebhook
const telegramSecretHeader = "X-Telegram-Bot-Api-Secret-Token"

// WebhookHandler is the HTTP handler mounted by the gateway in webhook mode.
// It performs the three things Telegram requires of every inbound POST:
//
//  1. Validate the X-Telegram-Bot-Api-Secret-Token header (403 on mismatch).
//  2. Decode the body as a single tgbotapi.Update.
//  3. Hand the update through the same auth -> classify -> router pipeline
//     that the long-poller uses, so the two ingestion modes are behaviourally
//     identical downstream of the wire format.
//
// The handler is safe to mount on the existing onyx serve mux at
// /telegram/webhook — no second listening port is required.
//
// secretTokens supports zero, one, or many tokens. Multiple tokens
// enable zero-downtime rotation: configure a new token on Telegram's
// side BEFORE removing the old one from config; the handler accepts
// either. The first token in the slice is the "current" one and is
// what the gateway reports as the active token; the others are
// accepted-but-deprecated. An empty list disables validation
// entirely (NOT recommended).
type WebhookHandler struct {
	bot         *Bot
	auth        *Authenticator
	handler     HandlerFunc
	secretTokens []string
	// requireSecret, when true and the secret-token list is
	// non-empty, causes requests with no X-Telegram-Bot-Api-Secret-Token
	// header to be rejected (403) instead of falling through. This
	// is the Phase-9 security hardening: the header is mandatory
	// whenever a secret is configured. Set via RequireSecret().
	requireSecret bool
	// maxBodyBytes caps inbound bodies at a sane Telegram-update size.
	// The largest realistic update is on the order of tens of KB even
	// with very large caption text; 1 MiB is generous while still
	// defending against memory abuse.
	maxBodyBytes int64
}

// NewWebhookHandler builds the handler. secretToken is the single
// active token; it is compared with constant-time equality against
// the X-Telegram-Bot-Api-Secret-Token header. An empty secretToken
// disables validation entirely (NOT recommended — anyone who finds
// the URL can POST to the bot) but is supported so the operator can
// temporarily debug in a private network.
//
// For zero-downtime rotation, call NewWebhookHandler with the new
// token only, then AddLegacyToken() to also accept the old one for
// the duration of the rotation window.
func NewWebhookHandler(bot *Bot, auth *Authenticator, handler HandlerFunc, secretToken string) *WebhookHandler {
	if handler == nil {
		handler = defaultHandler
	}
	tokens := []string{}
	require := false
	if secretToken != "" {
		tokens = append(tokens, secretToken)
		// Phase 9: if a secret is configured, the header is
		// mandatory. An empty secretToken keeps the legacy
		// "no validation" behaviour for backwards compat —
		// operators who want that stricter posture without a
		// secret (i.e. "reject everyone") can call
		// RequireSecret(true) explicitly.
		require = true
	}
	return &WebhookHandler{
		bot:           bot,
		auth:          auth,
		handler:       handler,
		secretTokens:  tokens,
		requireSecret: require,
		maxBodyBytes:  1 << 20,
	}
}

// AddLegacyToken adds an additional token that the handler will
// accept (constant-time matched) alongside the primary. This is the
// rotation primitive: configure a new token on Telegram's side,
// update the gateway config so NewWebhookHandler is called with the
// new primary, then add the old one via this method. Once the
// operator confirms Telegram is sending the new token, remove the
// legacy entry by rebuilding the handler.
func (h *WebhookHandler) AddLegacyToken(legacy string) {
	if legacy == "" {
		return
	}
	for _, t := range h.secretTokens {
		if t == legacy {
			return
		}
	}
	h.secretTokens = append(h.secretTokens, legacy)
}

// RequireSecret flips the Phase-9 "missing header = 403" rule. The
// default is to require a secret whenever any tokens are configured.
// Operators who deliberately want a tokenless webhook (LAN debug)
// can call this with false to opt out.
func (h *WebhookHandler) RequireSecret(require bool) {
	h.requireSecret = require
}

// ServeHTTP implements http.Handler. It is intentionally side-effect-free
// for the auth + parse failures: we return non-2xx so Telegram retries
// per its retry policy, but we never echo the raw error or body to the
// caller (the caller is Telegram, not a human).
func (h *WebhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Telegram's webhook spec: POST only. Anything else gets 405.
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Secret-token check FIRST so an unauthenticated caller cannot
	// burn CPU on a JSON decode. constantTimeCompare defends against
	// timing oracles; both sides must be the same length for that to
	// be meaningful, so we pad/check explicitly. We support multiple
	// tokens for zero-downtime rotation — the first token is the
	// "current" one (used in subsequent SetWebhook calls), the rest
	// are accepted-but-deprecated until the operator removes them.
	if len(h.secretTokens) > 0 {
		got := r.Header.Get(telegramSecretHeader)
		// Phase 9: if a secret is configured, the header is
		// mandatory. A missing header is treated identically
		// to a wrong-length header — we don't distinguish, so a
		// caller can't probe by varying header presence.
		if got == "" {
			slog.WarnContext(ctx, "telegram.webhook.secret_missing",
				slog.String("remote", r.RemoteAddr),
			)
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		matched := false
		for _, t := range h.secretTokens {
			if constantTimeEqual(got, t) {
				matched = true
				break
			}
		}
		if !matched {
			slog.WarnContext(ctx, "telegram.webhook.secret_mismatch",
				slog.String("remote", r.RemoteAddr),
				slog.Int("got_len", len(got)),
				slog.Int("configured_tokens", len(h.secretTokens)),
			)
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		// If the request matched a non-primary token, log it at
		// info level so the operator can see rotation is still
		// in progress and decide when to drop the legacy.
		if !constantTimeEqual(got, h.secretTokens[0]) {
			slog.InfoContext(ctx, "telegram.webhook.legacy_token_used",
				slog.String("remote", r.RemoteAddr),
			)
		}
	} else if h.requireSecret {
		// No tokens configured but requireSecret is on — treat
		// every request as forbidden. This is the
		// fail-closed-by-default posture; operators who want a
		// public webhook must explicitly call RequireSecret(false).
		slog.WarnContext(ctx, "telegram.webhook.no_secret_configured",
			slog.String("remote", r.RemoteAddr),
		)
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	// Body size guard.
	r.Body = http.MaxBytesReader(w, r.Body, h.maxBodyBytes)
	defer r.Body.Close()

	var upd tgbotapi.Update
	// Note: we deliberately do NOT use DisallowUnknownFields. Telegram
	// adds new fields to the Update payload on occasion, and a strict
	// decoder would reject legitimate forward-compatible updates. The
	// library's own HandleUpdate is similarly lenient.
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&upd); err != nil {
		slog.WarnContext(ctx, "telegram.webhook.decode_failed",
			slog.String("error", err.Error()),
		)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// Auth gate (same middleware as the poller).
	switch h.auth.Check(h.bot.API, &upd) {
	case AuthSilentDrop, AuthReplyDeny:
		// Acknowledge to Telegram so it does NOT retry — we received
		// the update, we just don't want to process it. 200 + empty
		// body is the canonical "thanks, I got it" response.
		w.WriteHeader(http.StatusOK)
		return
	}

	// Non-text content: photos, stickers, etc. — reply "unsupported"
	// through the bot (so the operator sees the bot is alive) AND
	// acknowledge to Telegram.
	msg := upd.Message
	if msg == nil {
		// edited_message / callback_query — not in v1 scope.
		w.WriteHeader(http.StatusOK)
		return
	}
	if !isTextOnlyMessage(msg) {
		notice := tgbotapi.NewMessage(msg.Chat.ID, "unsupported input: v1 of the Telegram gateway only handles text commands. Try /help.")
		if _, err := h.bot.API.Send(notice); err != nil {
			slog.WarnContext(ctx, "telegram.webhook.unsupported_reply_failed",
				slog.Int64("chat_id", msg.Chat.ID),
				slog.String("error", err.Error()),
			)
		}
		w.WriteHeader(http.StatusOK)
		return
	}

	// Dispatch through the router. safeInvoke catches panics so a
	// single bad command never 500s the webhook (which would cause
	// Telegram to retry forever).
	if err := safeInvoke(ctx, h.bot.API, msg, h.handler); err != nil {
		slog.ErrorContext(ctx, "telegram.webhook.handler_error",
			slog.Int64("chat_id", msg.Chat.ID),
			slog.String("error", err.Error()),
		)
		// Still 200 — we accepted the update, the handler is the
		// router's problem to surface. Retrying would just hit the
		// same error.
	}
	w.WriteHeader(http.StatusOK)
}

// SetWebhook registers the webhook with Telegram, including the secret
// token. It is a thin convenience over the library's WebhookConfig.
//
// publicURL is the full HTTPS URL Telegram should POST to, e.g.
// "https://onyx.example.com/telegram/webhook". maxConnections caps the
// number of simultaneous HTTPS connections Telegram will open; the
// default of 40 matches Telegram's recommended value.
//
// Note: v5.5.1 of go-telegram-bot-api does not expose `secret_token` on
// WebhookConfig, so we drop down to the lower-level MakeRequest API to
// pass it through. When/if the library gains first-class support, this
// helper can be simplified to use NewWebhook + Request.
func SetWebhook(ctx context.Context, api *tgbotapi.BotAPI, publicURL string, secretToken string, maxConnections int) error {
	if publicURL == "" {
		return errors.New("telegram webhook: public_url is empty")
	}
	if !strings.HasPrefix(strings.ToLower(publicURL), "https://") {
		return fmt.Errorf("telegram webhook: public_url must be https://, got %q", publicURL)
	}
	if _, err := url.Parse(publicURL); err != nil {
		return fmt.Errorf("telegram webhook: public_url is not a valid URL: %w", err)
	}
	if maxConnections <= 0 {
		maxConnections = 40
	}

	params := tgbotapi.Params{
		"url":             publicURL,
		"max_connections": strconv.Itoa(maxConnections),
		"allowed_updates": `["message"]`,
	}
	if secretToken != "" {
		params["secret_token"] = secretToken
	}

	if _, err := api.MakeRequest("setWebhook", params); err != nil {
		return fmt.Errorf("telegram webhook: setWebhook request: %w", err)
	}
	slog.InfoContext(ctx, "telegram.webhook.set",
		slog.String("public_url", publicURL),
		slog.Int("max_connections", maxConnections),
		slog.Bool("secret_configured", secretToken != ""),
	)
	return nil
}

// DeleteWebhook removes any webhook registered on Telegram's side. It
// is required when switching from webhook to polling, otherwise both
// sides fight and Telegram returns 409 Conflict on getUpdates.
func DeleteWebhook(ctx context.Context, api *tgbotapi.BotAPI, dropPending bool) error {
	cfg := tgbotapi.DeleteWebhookConfig{DropPendingUpdates: dropPending}
	if _, err := api.Request(cfg); err != nil {
		return fmt.Errorf("telegram webhook: deleteWebhook: %w", err)
	}
	slog.InfoContext(ctx, "telegram.webhook.deleted", slog.Bool("dropped_pending", dropPending))
	return nil
}

// WebhookInfo is a friendly projection of tgbotapi.WebhookInfo with the
// fields Onyx operators care about.
type WebhookInfo struct {
	URL                string
	HasCustomCert      bool
	PendingUpdateCount int
	LastErrorDate      int
	LastErrorMessage   string
}

// GetWebhookInfo fetches the current webhook state. Onyx logs the
// pending-update count and last error so an operator can see delivery
// health at a glance.
func GetWebhookInfo(ctx context.Context, api *tgbotapi.BotAPI) (WebhookInfo, error) {
	raw, err := api.GetWebhookInfo()
	if err != nil {
		return WebhookInfo{}, fmt.Errorf("telegram webhook: getWebhookInfo: %w", err)
	}
	info := WebhookInfo{
		URL:                raw.URL,
		HasCustomCert:      raw.HasCustomCertificate,
		PendingUpdateCount: raw.PendingUpdateCount,
		LastErrorDate:      raw.LastErrorDate,
		LastErrorMessage:   raw.LastErrorMessage,
	}
	if raw.LastErrorMessage != "" {
		slog.WarnContext(ctx, "telegram.webhook.last_error",
			slog.String("message", raw.LastErrorMessage),
			slog.Int("error_date_unix", raw.LastErrorDate),
			slog.Int("pending_update_count", raw.PendingUpdateCount),
		)
	} else if raw.PendingUpdateCount > 0 {
		slog.InfoContext(ctx, "telegram.webhook.pending_updates",
			slog.Int("pending_update_count", raw.PendingUpdateCount),
		)
	}
	return info, nil
}

// ReconcileMode ensures Telegram's side and Onyx's side agree on
// polling-vs-webhook. If mode is "polling" and Telegram reports an
// active webhook, we delete it. If mode is "webhook" and there is no
// webhook set, the operator is expected to call SetWebhook explicitly
// (we do NOT do it for them — registering a public URL is an explicit
// ops decision).
//
// Returns the WebhookInfo observed on Telegram's side so the caller
// can log it.
func ReconcileMode(ctx context.Context, api *tgbotapi.BotAPI, mode string) (WebhookInfo, error) {
	info, err := GetWebhookInfo(ctx, api)
	if err != nil {
		return WebhookInfo{}, err
	}
	if strings.EqualFold(mode, "polling") && info.URL != "" {
		// Sleep briefly so a concurrent webhook-mode restart has a
		// chance to land before we nuke it. 200ms is invisible to
		// operators but gives the other goroutine a window.
		select {
		case <-ctx.Done():
			return info, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
		if err := DeleteWebhook(ctx, api, false); err != nil {
			return info, err
		}
		info.URL = ""
	}
	return info, nil
}

// constantTimeEqual is a tiny wrapper around crypto/subtle that lives
// in this file so we don't drag a new import into the package's
// surface. Returns false for length mismatches without leaking length
// info beyond the obvious (we are not defending against a side-channel
// in the bot's own logs).
func constantTimeEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := 0; i < len(a); i++ {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}
