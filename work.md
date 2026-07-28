> [!IMPORTANT]
> **HIGH PRIORITY PROJECT — DEVELOPMENT GUIDELINES**
> - **Full Focus & Quality #1:** Code quality and clean architecture are priority #1. Never hurry—take your time to do things right.
> - **Zero Mess Policy:** Work with full focus. Do not make any mess, leave dead code, or use sloppy workarounds.
> - **Disciplined Execution:** Focus on craftsmanship, clarity, robust testing, and clean design at every step.

# Onyx Scrapper — v1 Build Workflow

**Stack:** Go · go-rod + go-rod/stealth · colly · SQLite · MiMo V2.5 (OpenAI-compatible API)
**Budget:** $0/month
**Scope:** Personal-use AI web agent — semantic scraping, structured extraction, agentic multi-step search, local searchable knowledge base.

Each step below = one coding session. Don't skip ahead — each step produces a working, testable increment.

---

## - [x] Step 0 — Project Skeleton & Config

**Goal:** Repo structure, Go module, config loading, MiMo API client that returns "Hello" successfully.

- `go mod init github.com/yourname/onyx-scrapper`
- Folder structure:
  ```
  /cmd/onyx/main.go
  /internal/config/
  /internal/llm/
  /internal/browser/
  /internal/extract/
  /internal/store/
  /internal/agent/
  /internal/api/
  /data/ (sqlite db lives here, gitignored)
  .env
  ```
- Config via `.env` (use `godotenv`): `MIMO_API_KEY`, `MIMO_BASE_URL=https://api.xiaomimimo.com/v1`, `MIMO_MODEL=mimo-v2.5-pro`
- Build `internal/llm/client.go`: thin OpenAI-compatible client (net/http, no SDK needed) with a `Chat(messages []Message) (string, error)` function.
- **Test:** CLI command that sends "say hello" to MiMo and prints the response.

**Done when:** `go run cmd/onyx/main.go ping` prints a real MiMo response.

---

## - [x] Step 1 — Static HTML Fetch + Parse Pipeline

**Goal:** Fetch a plain (non-JS-heavy) URL and extract clean readable text — no browser yet.

- Add `colly` for HTTP-only fetching (fast path, use before falling back to browser).
- Add `goquery` for DOM traversal/cleanup: strip `<script>`, `<style>`, `<nav>`, `<footer>`, ads/boilerplate.
- Write `internal/extract/clean.go`: `CleanHTML(raw string) string` → returns readable text + preserves headings/links structurally (markdown-ish).
- **Test:** fetch Wikipedia or a blog post, output clean text to terminal.

**Done when:** you can run `onyx fetch <url>` and get clean, LLM-ready text (not raw HTML soup).

---

## - [x] Step 2 — Headless Browser Layer (go-rod)

**Goal:** Render JS-heavy pages using a real headless Chromium instance, with stealth patches applied by default.

- Add `go-rod/rod` + `go-rod/stealth`.
- Build `internal/browser/browser.go`: `NewStealthPage() *rod.Page` — launches Chromium via `stealth.MustPage`, disables `navigator.webdriver`, sets realistic viewport + user-agent.
- Wrap with timeout/context handling and `defer browser.MustClose()` cleanup (rod does this well natively).
- Fallback logic: try colly (Step 1) first → if page looks empty/JS-rendered (e.g. `<div id="root"></div>` with no content), fall back to go-rod.
- **Test:** scrape a JS-rendered SPA page (e.g. a React-based site) and get real content.

**Done when:** `onyx fetch <url> --render` works on JS-heavy pages that Step 1 fails on.

---

## - [x] Step 3 — Semantic Element Finder (the TinyFish trick)

**Goal:** Instead of hardcoded CSS selectors, describe what you want in plain English and let MiMo locate it in the DOM.

- Build `internal/extract/semantic.go`: `FindElement(pageHTML string, description string) (selector string, err error)`.
  - Strip HTML down (Step 1's cleaner, but keep tag structure + attributes this time — don't fully flatten to text).
  - Prompt MiMo: "Given this simplified DOM, return the CSS selector or XPath for: {description}. Respond with only the selector."
  - Feed the returned selector into go-rod's `page.MustElement(selector)`.
- Add a token-budget guard: truncate/pre-filter HTML before sending to MiMo (strip inline styles, svg paths, data URIs) — keeps requests fast even on "unlimited" credits.
- **Test:** "find the search box" / "find the price element" on 2-3 different real sites — confirm it correctly returns usable selectors.

**Done when:** semantic lookup correctly identifies elements on sites it's never seen, without you writing a single CSS selector by hand.

---

## - [x] Step 4 — Structured Data Extraction (HTML → JSON)

**Goal:** Turn a page (or page fragment) into structured JSON via MiMo, with a schema you define per task.

- Build `internal/extract/structured.go`: `ExtractJSON(content string, schema string) (json.RawMessage, error)`.
  - Prompt pattern: "Extract the following as valid JSON matching this schema: {schema}. Only output JSON, no prose."
  - Parse response; on JSON parse failure, retry once with an error-correction prompt ("Your last output was invalid JSON: {error}. Fix it.").
- Define reusable schema templates (Go structs + JSON schema strings) for common cases: article, product, event, search-result-list.
- **Test:** point at a product page → get back `{name, price, availability}` as real JSON.

**Done when:** you can extract arbitrary structured data from any page by just changing the schema string, zero code changes.

---

## - [x] Step 5 — Local Storage Layer (SQLite)

**Goal:** Persist every scrape + extraction so nothing is wasted — build your personal data lake.

- Add `mattn/go-sqlite3` (or `modernc.org/sqlite` for pure-Go, no CGO — recommended if you want easy cross-compilation).
- Schema (`internal/store/schema.sql`):
  ```sql
  CREATE TABLE pages (
    id INTEGER PRIMARY KEY,
    url TEXT UNIQUE,
    fetched_at DATETIME,
    raw_html TEXT,
    clean_text TEXT
  );
  CREATE TABLE extractions (
    id INTEGER PRIMARY KEY,
    page_id INTEGER,
    schema_name TEXT,
    data_json TEXT,
    created_at DATETIME,
    FOREIGN KEY(page_id) REFERENCES pages(id)
  );
  CREATE VIRTUAL TABLE pages_fts USING fts5(url, clean_text, content='pages');
  ```
- Use SQLite FTS5 for full-text search — free, built-in, no external search engine needed.
- Build `internal/store/store.go`: `SavePage`, `SaveExtraction`, `SearchPages(query string)`.
- **Test:** scrape 5 pages, then run a full-text search across them from the CLI.

**Done when:** `onyx search "keyword"` returns matching past scrapes instantly from local SQLite.

---

## - [x] Step 6 — Basic Agent Loop (Multi-Step Reasoning)

**Goal:** The actual "agent" part — given a goal in plain English, MiMo plans steps (navigate → find → click → extract) and your code executes them.

- Build `internal/agent/agent.go` with a simple ReAct-style loop:
  1. System prompt defines available tools: `navigate(url)`, `find_element(description)`, `click(selector)`, `type(selector, text)`, `extract(schema)`, `done(result)`.
  2. Loop: send current state (last action result + page summary) to MiMo → parse its next tool call (use JSON mode / function-calling style prompting) → execute → repeat until `done`.
  3. Hard cap on steps (e.g. 15) to prevent runaway loops/token burn.
- Log every step to SQLite (new `agent_runs` + `agent_steps` tables) for debugging/replay.
- **Test:** give it a goal like "go to X site, search for Y, extract the first 3 results" and watch it execute autonomously.

**Done when:** you can hand it one sentence and it completes a multi-step task without you scripting each step.

---

## - [x] Step 7 — Stealth Hardening & Rate Limiting

**Goal:** Reduce block rate on your own IP using free code-level tricks (no paid proxy yet).

- Randomize: user-agent per session, viewport size, timezone/locale headers.
- Add human-like delays: random sleep (300–1500ms) between actions instead of instant clicks.
- Add per-domain rate limiting (`golang.org/x/time/rate`) — e.g. max 1 request per domain per 2 seconds.
- Respect `robots.txt` (add a simple checker — good practice, avoids unnecessary blocks/legal grey zones).
- Verify stealth is working: run against `bot.sannysoft.com` and confirm no red flags.
- **Test:** run a 20-page scrape session against a moderately protected site and check block/success rate.

**Done when:** stealth checks pass and you're not getting blocked on normal (non-Cloudflare-hardcore) sites.

---

## - [x] Step 8 — Free-Tier Fallback Layer (the "remaining 10%")

**Goal:** When your own IP gets blocked, fall back to free-tier external APIs instead of failing.

- Add `internal/browser/fallback.go`: if direct fetch/render fails (403/429/CAPTCHA detected), retry once via ScraperAPI free tier (1,000 credits/month, no card).
- Config: `SCRAPERAPI_KEY` (optional — feature degrades gracefully if unset).
- Simple circuit breaker: track failure count per domain, auto-switch that domain to fallback mode after N failures.
- **Test:** deliberately hit a site that blocks your IP, confirm fallback kicks in and succeeds.

**Done when:** blocked sites automatically route through the free fallback without manual intervention.

---

## - [x] Step 9 — CLI + Minimal HTTP API

**Goal:** Make Onyx Scrapper usable as a daily tool, not just a script you edit — and callable as a plain web-search API from any other agentic harness/TUI.

- CLI commands (via `cobra` or plain `flag`/`os.Args` switch):
  - `onyx fetch <url> [--render]`
  - `onyx extract <url> --schema product`
  - `onyx agent "<goal in plain english>"`
  - `onyx search "<query>"`
- Minimal local HTTP API (`internal/api/server.go`, `net/http`, no framework needed) exposing the same actions as JSON endpoints on **`localhost:9090`** (not 8080 — reserved for SearXNG in Step 11, avoids port collision).
- **Design `/search` as a stateless, harness-friendly endpoint** — no auth, no session, just request in → JSON out, so any external agentic harness can call it exactly like it would call Google/Bing/Serper's search API:
  ```
  GET/POST http://localhost:9090/search?q=<query>
  ```
  Response schema (keep this stable — it's the external contract):
  ```json
  {
    "query": "your search query",
    "results": [
      { "title": "...", "url": "...", "snippet": "..." },
      { "title": "...", "url": "...", "snippet": "..." }
    ]
  }
  ```
  Internally this endpoint just calls Step 11's SearXNG wrapper and reshapes the response — keep it thin.
- Other endpoints follow the same input→JSON-out pattern: `/fetch`, `/extract`, `/agent`.
- **Test:** run all four CLI commands end-to-end; hit each HTTP endpoint with `curl`, and separately confirm `curl http://localhost:9090/search?q=test` returns clean JSON with no extra ceremony (this is the exact call another harness will make).

**Done when:** Onyx Scrapper feels like a real personal tool you'd actually reach for, and `/search` works as a drop-in web-search API call for any other agent/harness pointed at `localhost:9090`.

---

## - [x] Step 10 — Scheduling + Polish

**Goal:** Automate recurring scrapes and clean up for daily use.

- Add a `schedule.yaml` (site + interval + schema) and a simple ticker-based scheduler goroutine that runs jobs and saves results.
- Add structured logging (`log/slog`) across all packages — replace stray `fmt.Println` debug calls.
- Add `.gitignore` for `/data`, `.env`; write a proper `README.md` (setup, usage, architecture diagram in words).
- Optional: simple `--json` output flag on all commands for scripting/piping.

**Done when:** you can set-and-forget a recurring scrape job and trust the tool to run unattended.

---

## - [x] Step 11 — Self-Hosted Search Engine (SearXNG)

**Goal:** Give Onyx real web *search* (not just single-URL fetch) — your own private, unlimited, unrated Google/Bing/DuckDuckGo aggregator, wired up as Onyx's `/search` endpoint from Step 9.

- Run SearXNG via Docker Compose locally (official image, `docker.io/searxng/searxng`, 512MB RAM is enough). Map it to **port `8888`** (not `8080`) to avoid clashing with Onyx's own API (`9090`) or common local dev defaults. It aggregates 70+ upstream engines and returns JSON — <cite index="39-1">it has no crawler and no index of its own; it fires your query at a configurable fan of upstream engines in parallel, then scores, merges, and de-duplicates everything into one ranked page</cite>.
- Enable JSON output format in `settings.yml` (`formats: [html, json]`).
- Build `internal/search/searxng.go`: `Search(query string) ([]SearchResult, error)` — hits `http://localhost:8888/search?q=...&format=json`.
- Wire into two places:
  1. The agent (Step 6): add a new tool `web_search(query)` the agent can call before navigating.
  2. Step 9's `/search` HTTP endpoint — this is the function that endpoint actually calls under the hood.
- **Test:** `onyx agent "search for the top 3 Go web scraping libraries and summarize each"` — should call `web_search`, then fetch/extract from top results automatically. Also re-confirm `curl http://localhost:9090/search?q=test` still returns clean JSON now that it's backed by a real engine instead of a stub.

**Done when:** the agent can search the open web on its own, with zero API limits, entirely on your machine — and any external harness calling Onyx's `/search` API gets the same real results.

---

## - [x] Step 12 — Site & Sitemap Crawling (Multi-Page Discovery)

**Goal:** Go from "one URL at a time" to "discover and crawl an entire site" automatically.

- Build `internal/crawl/discover.go`: fetch `/robots.txt`, parse `Sitemap:` directives, fetch and parse `sitemap.xml` (and sitemap indexes) to get a full URL list for a domain.
- Fallback: if no sitemap, do a breadth-first crawl following same-domain `<a href>` links up to a configurable depth (use `colly`'s built-in `OnHTML("a[href]")` + visited-set dedup — colly already handles this cleanly).
- Respect `robots.txt` disallow rules (reuse Step 7's checker).
- Add `internal/crawl/queue.go`: simple in-memory or SQLite-backed URL queue with status (`pending`, `done`, `failed`) so a crawl can resume after interruption.
- **Test:** `onyx crawl <domain> --max-pages 50` — pulls a real sitemap or link-graph and scrapes/stores each page via the Step 1–5 pipeline.

**Done when:** you can point Onyx at a domain and it discovers + ingests dozens of pages without you listing URLs by hand.

---

## - [x] Step 13 — Concurrent Browser Pool (Real Parallelism)

**Goal:** Scrape/crawl many pages at once instead of one Chromium tab at a time — pure Go concurrency, still $0.

- Build `internal/browser/pool.go`: a worker pool of N go-rod browser instances (or N pages on one shared browser — cheaper on RAM; prefer shared-browser-multi-page unless sites need isolated cookies/sessions).
- Feed URLs from Step 12's queue into the pool via a channel; workers pull, render, extract, store, mark done — classic Go fan-out/fan-in pattern.
- Tune pool size to your machine (e.g. `runtime.NumCPU()`-based default, override via config) — each headless Chromium tab costs real RAM, so cap it (start at 3-5 concurrent).
- Respect Step 7's per-domain rate limiter even under concurrency (limiter keyed by domain, not global).
- **Test:** crawl a 50-page site with `--workers 5` and confirm wall-clock time drops significantly vs. sequential.

**Done when:** multi-page jobs run visibly faster with controlled, safe concurrency.

---

## What v1 Deliberately Excludes (future v2+ ideas, not needed now)
- Residential proxy rotation (paid)
- CAPTCHA solving service integration (paid)
- Distributed/multi-machine scaling
- Web UI/dashboard
- Local model hosting (Ollama or otherwise) — everything stays cloud-side via MiMo, nothing runs locally beyond SearXNG/browser/DB

---

## Session Map Summary
| Status | Step | Session Focus | Output |
|---|---|---|---|
| [x] | 0 | Skeleton + MiMo client | Working API ping |
| [x] | 1 | Static fetch + clean | Readable text from URL |
| [x] | 2 | Headless browser | JS-rendered page support |
| [x] | 3 | Semantic element finder | AI-located selectors |
| [x] | 4 | Structured extraction | HTML → JSON |
| [x] | 5 | SQLite storage | Persistent, searchable data |
| [x] | 6 | Agent loop | Autonomous multi-step tasks |
| [x] | 7 | Stealth + rate limits | Lower block rate, free |
| [x] | 8 | Free-tier fallback | Graceful degradation on blocks |
| [x] | 9 | CLI + API | Real usable tool |
| [x] | 10 | Scheduling + polish | Daily-driver ready |
| [x] | 11 | SearXNG search | Real web search, unlimited, free |
| [x] | 12 | Site/sitemap crawling | Auto-discover & ingest full domains |
| [x] | 13 | Concurrent browser pool | Real parallel scraping |
| [x] | 14 | Deep Research Mode | Orchestrator/Worker parallel research |