# WORK.md — Onyx Scrapper: Discovery Layer Upgrade

> Implementation prompt for adding TinyFish + Jina as parallel discovery/fallback providers into the existing Go codebase, without breaking current SearXNG/Colly/go-rod/ScraperAPI behavior.

Paste this whole file into Claude Code (or your coding agent of choice) inside the `onyx-scrapper` repo root. It has full context to work autonomously.

---

## Context

Repo: `onyx-scrapper` (Go 1.21+, Colly, go-rod/stealth, SQLite/FTS5, SearXNG, MiMo V2.5).
Current discovery path: CLI/HTTP → SearXNG (search) → Colly/go-rod (fetch) → ScraperAPI (fallback) → MiMo (extract) → SQLite.

Goal: add **TinyFish** (Search + Fetch) and **Jina Reader/Search/Reranker** as additional providers in the discovery/fetch/rerank stages, used by both the ReAct agent and the Deep Research Orchestrator, fully config-gated and degrading gracefully to current behavior if disabled or unreachable.

Do not touch: MiMo extraction logic, the Deep Research sub-question decomposition logic, the scheduler, or the FTS5 schema for `pages_fts`.

---

## Task 1 — Provider interface

Create `internal/discovery/provider.go` defining a common interface so every search/fetch source is swappable:

```go
type SearchProvider interface {
    Name() string
    Search(ctx context.Context, query string, limit int) ([]SearchResult, error)
}

type FetchProvider interface {
    Name() string
    Fetch(ctx context.Context, url string, opts FetchOptions) (*PageContent, error)
}

type SearchResult struct {
    URL, Title, Snippet string
    Provider             string
}

type PageContent struct {
    URL, CleanText, RawHTML string
    Provider                 string
    FetchedAt                time.Time
}
```

Existing SearXNG search and Colly/go-rod fetch logic must be wrapped to implement these interfaces (`SearXNGProvider`, `CollyProvider`, `RodProvider`) — no behavior change, just an adapter layer.

---

## Task 2 — TinyFish provider

Create `internal/discovery/tinyfish.go`:

- `TinyFishProvider` implements both `SearchProvider` and `FetchProvider`.
- Search: `GET https://api.search.tinyfish.ai?query={q}` header `X-API-Key`.
- Fetch: `POST https://api.fetch.tinyfish.ai` body `{"urls": [url]}` header `X-API-Key`.
- Read key from `config.tinyfish.api_key`; if empty or `enabled: false`, provider is skipped at registration (not called with empty key).
- Timeout: 15s. On non-200 or timeout, return a typed `ErrProviderUnavailable` — never panic, never block other providers.
- Add minimal response struct matching TinyFish's JSON shape; unit test with a mocked HTTP client (`httptest.Server`).

---

## Task 3 — Jina provider

Create `internal/discovery/jina.go`:

- `JinaProvider` implements `SearchProvider` (via `s.jina.ai`) and `FetchProvider` (via `r.jina.ai`).
- Search: `GET https://s.jina.ai/{urlencoded_query}`, optional header `Authorization: Bearer {key}` if `config.jina.api_key` set, else keyless.
- Fetch: `GET https://r.jina.ai/{target_url}`, same optional auth header.
- Respect keyless rate limit: add a token-bucket limiter defaulting to 18 RPM when no key is set (safety margin under the ~20 RPM keyless cap), unlimited-ish (500 RPM bucket) when a key is present.
- Same graceful-failure contract as Task 2.

Create `internal/discovery/jina_rerank.go`:

- `Rerank(ctx, query string, docs []string) ([]RankedDoc, error)` calling Jina's reranker endpoint.
- Gated by `config.jina.reranker_enabled`. If disabled, callers must skip reranking entirely (no-op passthrough preserving original order) — do not fail the pipeline if this returns an error either; log and fall through unranked.

---

## Task 4 — Registry, fan-out, and dedup

Create `internal/discovery/registry.go`:

- `Registry.Search(ctx, query string) []SearchResult` — fans out to all *enabled* `SearchProvider`s concurrently (`errgroup`, per-provider timeout), merges results, **dedupes by normalized URL** (strip trailing slash, query-string tracking params, lowercase host), preserves first-seen provider tag for attribution.
- `Registry.Fetch(ctx, url string) (*PageContent, error)` — tries providers **in priority order**, not fan-out (fetch is expensive/rate-limited): 
  1. Colly (static, cheapest)
  2. go-rod (if Colly detects JS-required page, per existing logic — unchanged)
  3. **TinyFish Fetch** (new — inserted before ScraperAPI)
  4. Jina Reader (new — inserted before ScraperAPI, after TinyFish)
  5. ScraperAPI (existing, last resort)
- Order must be config-overridable via `config.discovery.fetch_priority: [colly, rod, tinyfish, jina, scraperapi]`.
- Wire this into the existing circuit breaker: each provider failure still increments the per-domain failure counter that already exists; do not create a second breaker.

---

## Task 5 — Wire into ReAct agent and Deep Research Orchestrator

- Find the current call sites where the agent loop and the research worker call SearXNG search / Colly-or-rod fetch directly.
- Replace direct calls with `Registry.Search(...)` / `Registry.Fetch(...)`.
- In the Deep Research worker, after gathering findings for a sub-question and before writing to `findings`, call `Rerank()` on the candidate text chunks and keep only the top-K (config: `research.rerank_top_k`, default 8) before passing to MiMo for claim extraction. This directly improves report quality without touching the decomposition/orchestration logic.
- Add `source_provider` column population (already present in schema per README) on both `pages` and `findings` inserts — this was previously unpopulated or hardcoded; now set from `PageContent.Provider` / `SearchResult.Provider`.

---

## Task 6 — Config + CLI flags

- Extend `config.yaml` loader to parse the `tinyfish:` and `jina:` blocks shown in the README. All fields optional; missing block = provider disabled.
- Add `--source` flag to `fetch` command (values: `auto|colly|rod|tinyfish|jina|scraperapi`) to force a specific provider for debugging.
- Add `--sources` flag to `research` command (comma list) to restrict which search providers are used for that run.
- `test-fallback` command: extend to report which provider actually served the request (currently likely only reports success/fail — add provider name to output).

---

## Task 7 — Tests

- Unit tests per provider with `httptest` mocks (success, 429, timeout, malformed JSON).
- Registry test: verify dedup logic with overlapping URLs across two mock providers.
- Registry test: verify fetch priority order and that a failing higher-priority provider correctly falls through to the next.
- Integration-style test (build-tagged `integration`, skipped by default) hitting real TinyFish/Jina keyless endpoints, for manual verification only.

Run `go test -v ./...` and ensure all existing tests still pass unmodified — this is an additive change, existing SearXNG/Colly/go-rod/ScraperAPI paths must behave identically when TinyFish/Jina are disabled in config.

---

## Acceptance Criteria

- [ ] `config.yaml` with no `tinyfish`/`jina` blocks → identical behavior to pre-upgrade Onyx (regression check).
- [ ] `config.yaml` with only `tinyfish.api_key` set → TinyFish participates in search fan-out and sits in fetch fallback chain before ScraperAPI.
- [ ] Jina works with **zero config** (keyless) the moment `jina.enabled` isn't explicitly `false`.
- [ ] Killing network access to TinyFish/Jina (or invalid key) never crashes the agent or research run — it just logs and continues with remaining providers.
- [ ] `research_runs` reports now cite multiple `source_provider` values when discovery layer is fully enabled (verify via a live `.\onyx.exe research "..." --json` run).
- [ ] `go vet ./...` and `go test -v ./...` clean.

---

## Order of execution

1. Task 1 (interfaces) → 2 & 3 (providers, can be done in parallel) → 4 (registry) → 6 (config/flags) → 5 (wiring) → 7 (tests) → verify acceptance criteria.