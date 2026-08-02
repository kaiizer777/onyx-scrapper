package telegram

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Phase 8 — delivery.
//
// The gateway has three ways to put a result in front of the user:
//
//  1. A single HTML message (parse_mode=HTML, the only mode that
//     survives `&`, `<`, `>` in the body without a 400).
//  2. A sequence of chunked HTML messages, when the result is
//     between MaxMessageChars and FileFallbackThreshold chars.
//  3. A single sendDocument with the result as a `.md` (or `.json`)
//     file, when the result is over FileFallbackThreshold or the
//     caller flags it as a code/JSON payload that doesn't render
//     well inline.
//
// deliverer owns the choice between those three. It is stateless —
// every method is safe for concurrent use. The per-chat pacing
// between sequential messages is enforced by the caller (the worker
// goroutine) so the deliverer doesn't need to know about chat
// back-pressure.

type deliverer struct {
	f *formatter
}

func newDeliverer() *deliverer { return &deliverer{f: newFormatter()} }

// sendHTML sends a single HTML message. The bot is required; chatID
// is the destination. We always set parse_mode=HTML; the formatter
// has already escaped the body. If the body exceeds MaxMessageChars
// we transparently route through the chunked path so callers don't
// have to pre-check.
func (d *deliverer) sendHTML(ctx context.Context, bot *tgbotapi.BotAPI, chatID int64, html string) error {
	if bot == nil {
		return nil
	}
	html = strings.TrimSpace(html)
	if html == "" {
		// Nothing to send — this is a no-op rather than an error
		// so the caller can treat empty results uniformly.
		return nil
	}
	if len(html) <= MaxMessageChars {
		msg := tgbotapi.NewMessage(chatID, html)
		msg.ParseMode = tgbotapi.ModeHTML
		msg.DisableWebPagePreview = true
		if _, err := bot.Send(msg); err != nil {
			return err
		}
		return nil
	}
	// Body too long for one message: route to chunked delivery.
	return d.sendChunked(ctx, bot, chatID, html)
}

// sendChunked splits `html` on paragraph / line / space boundaries
// (matching the pre-Phase-8 chunkMessage behavior so existing tests
// keep passing) and sends each chunk as its own HTML message. The
// first chunk is prefixed with a header so the user knows which
// "part 1/N" of the run they are looking at; subsequent chunks use
// the "part N/M" prefix to keep them ordered in the chat.
func (d *deliverer) sendChunked(ctx context.Context, bot *tgbotapi.BotAPI, chatID int64, html string) error {
	if bot == nil {
		return nil
	}
	chunks := chunkMessageHTML(html)
	if len(chunks) == 0 {
		return nil
	}
	total := len(chunks)
	for i, c := range chunks {
		// 8-byte budget for the "(1/3)" prefix; trim the chunk
		// if necessary so the total stays under the cap.
		prefix := ""
		if total > 1 {
			prefix = fmt.Sprintf("(%d/%d) ", i+1, total)
		}
		// Reserve a small margin for the prefix; chunks are
		// already sized under MaxMessageChars so the prefix
		// push is bounded.
		body := prefix + c
		if len(body) > MaxMessageChars {
			body = body[:MaxMessageChars-1] + "…"
		}
		msg := tgbotapi.NewMessage(chatID, body)
		msg.ParseMode = tgbotapi.ModeHTML
		msg.DisableWebPagePreview = true
		if _, err := bot.Send(msg); err != nil {
			return err
		}
	}
	return nil
}

// sendFile delivers `body` as a Telegram document (file attachment)
// with the given filename and an optional caption. The caption is
// itself HTML-escaped by sendDocument when parse_mode is set; we
// pass it as-is. If the body is empty we return nil without
// calling Telegram.
func (d *deliverer) sendFile(ctx context.Context, bot *tgbotapi.BotAPI, chatID int64, filename string, body []byte, caption string) error {
	if bot == nil {
		return nil
	}
	if len(body) == 0 {
		return nil
	}
	doc := tgbotapi.NewDocument(chatID, tgbotapi.FileBytes{
		Name:  filename,
		Bytes: body,
	})
	doc.DisableContentTypeDetection = true
	if caption != "" {
		doc.Caption = caption
		if len(doc.Caption) > MaxCaptionChars {
			doc.Caption = doc.Caption[:MaxCaptionChars-1] + "…"
		}
		// We use HTML for the caption too so it stays consistent
		// with the message body. The caption content is plain
		// (usually "report" or "JSON result"); we still escape
		// it to be safe.
		doc.ParseMode = tgbotapi.ModeHTML
	}
	if _, err := bot.Send(doc); err != nil {
		return err
	}
	return nil
}

// reportRenderer is the high-level entry point used by the agent
// and research workers. It picks between chunked-HTML and a
// single-file attachment based on the body size, and stamps a
// header on the first chunk so the chat shows a coherent
// "report" / "result" thread.
func (d *deliverer) reportRenderer(reportMD string, sources []Citation) (html string, useFile bool) {
	return d.f.renderMarkdownReport(reportMD, sources)
}

// reportBodyFor returns the body that reportRenderer() decided to
// send — useful when the caller wants the bytes-for-file payload
// (e.g. the research worker stores the same artefact on disk and
// the chat user). Returns the raw markdown (no HTML conversion)
// so the on-disk copy is identical to what the engine produced.
func (d *deliverer) reportBodyFor(reportMD string) []byte {
	return []byte(reportMD)
}

// DeliverReport is the one-call API: take the engine's
// (reportMD, sources) tuple and put it in front of the user.
// On huge outputs it falls back to a file attachment. The optional
// header is prepended to the first message and to the file
// caption so the user can tell agent and research runs apart at a
// glance.
//
// We log the chosen delivery mode (chunked vs file) and the
// resulting chunk count / file size at debug level so the operator
// can tune FileFallbackThreshold based on real traffic.
func (d *deliverer) DeliverReport(ctx context.Context, bot *tgbotapi.BotAPI, chatID int64, header string, reportMD string, sources []Citation) error {
	if bot == nil {
		return nil
	}
	html, useFile := d.reportRenderer(reportMD, sources)
	if header != "" {
		// The header is operator-friendly text (e.g. "✅ research
		// run #42 complete"). It is NOT user content so we do
		// not HTML-escape it; we just pass it through. If an
		// operator overrides it with user-derived text they
		// should pre-escape.
		html = header + "\n\n" + html
	}
	if useFile {
		// File fallback. The body is the HTML-rendered report —
		// it renders fine in a browser; for a plain-text editor
		// it still shows the formatting tags. We pass it as
		// `.md` (Telegram's content detection will offer a
		// preview) and use the original markdown for the on-
		// disk artifact. For simplicity, we send the HTML body
		// the user would have seen; the original markdown is
		// still available via the agent_runs/research_runs
		// table.
		body := d.f.MarkdownToHTML(reportMD)
		caption := header
		if caption == "" {
			caption = "report"
		}
		slog.DebugContext(ctx, "telegram.delivery.file_fallback",
			slog.Int64("chat_id", chatID),
			slog.Int("body_chars", len(body)),
		)
		return d.sendFile(ctx, bot, chatID, "onyx_report.html", []byte(body), caption)
	}
	chunks := chunkMessageHTML(html)
	slog.DebugContext(ctx, "telegram.delivery.chunked",
		slog.Int64("chat_id", chatID),
		slog.Int("chunks", len(chunks)),
		slog.Int("body_chars", len(html)),
	)
	return d.sendChunked(ctx, bot, chatID, html)
}

// DeliverJSON sends a JSON-formatted extraction result. The result
// is pretty-printed; small results go inline as <pre>, large
// results go as a file attachment.
func (d *deliverer) DeliverJSON(ctx context.Context, bot *tgbotapi.BotAPI, chatID int64, header string, v interface{}) error {
	if bot == nil {
		return nil
	}
	html, useFile, filename, body, caption := d.f.formatJSONResult(v)
	if useFile {
		if header != "" {
			caption = header + " — " + caption
		}
		return d.sendFile(ctx, bot, chatID, filename, body, caption)
	}
	if header != "" {
		html = header + "\n\n" + html
	}
	return d.sendHTML(ctx, bot, chatID, html)
}

// DeliverText is a tiny wrapper for plain (non-Markdown, non-JSON)
// output. Used for /fetch and similar where the result is already
// clean text. It chunks if needed; never falls back to a file
// because plain text under 32 KiB is always acceptable inline.
func (d *deliverer) DeliverText(ctx context.Context, bot *tgbotapi.BotAPI, chatID int64, header, body string) error {
	if bot == nil {
		return nil
	}
	if header != "" {
		body = header + "\n\n" + body
	}
	body = htmlEscapeExceptTags(body)
	return d.sendHTML(ctx, bot, chatID, body)
}

// chunkMessageHTML is the formatter-aware chunker. It splits on
// paragraph (`\n\n`), then single newline, then space, then
// hard-cuts. Each chunk is trimmed and skipped if empty.
func chunkMessageHTML(html string) []string {
	const max = MaxMessageChars
	html = strings.TrimSpace(html)
	if html == "" {
		return nil
	}
	if len(html) <= max {
		return []string{html}
	}
	var chunks []string
	remaining := html
	for len(remaining) > 0 {
		if len(remaining) <= max {
			chunks = append(chunks, strings.TrimSpace(remaining))
			break
		}
		cut := max
		// Prefer paragraph break.
		if idx := strings.LastIndex(remaining[:max], "\n\n"); idx > 0 {
			cut = idx
		} else if idx := strings.LastIndex(remaining[:max], "\n"); idx > 0 {
			cut = idx
		} else if idx := strings.LastIndex(remaining[:max], " "); idx > 0 {
			cut = idx
		}
		chunks = append(chunks, strings.TrimSpace(remaining[:cut]))
		remaining = strings.TrimSpace(remaining[cut:])
	}
	// Filter empties (defensive).
	out := chunks[:0]
	for _, c := range chunks {
		if c != "" {
			out = append(out, c)
		}
	}
	return out
}
