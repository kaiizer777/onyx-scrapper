> [!IMPORTANT]
> **HIGH PRIORITY PROJECT — DEVELOPMENT GUIDELINES**
> - **Full Focus & Quality #1:** Code quality and clean architecture are priority #1. Never hurry—take your time to do things right.
> - **Zero Mess Policy:** Work with full focus. Do not make any mess, leave dead code, or use sloppy workarounds.
> - **Disciplined Execution:** Focus on craftsmanship, clarity, robust testing, and clean design at every step.

# Onyx Scrapper — v1 Build Workflow

**Stack:** Go · go-rod + go-rod/stealth · colly · SQLite · MiMo V2.5
**Budget:** $0/month
**Scope:** Personal-use AI web agent — semantic scraping, structured extraction, agentic multi-step search, local searchable knowledge base.

---

## Progress Summary (Steps 0-10)
| Step | Focus | Status |
|---|---|---|
| 0 | Project Skeleton & Config (MiMo client) | [x] |
| 1 | Static HTML Fetch + Parse (colly) | [x] |
| 2 | Headless Browser Layer (go-rod) | [x] |
| 3 | Semantic Element Finder (AI descriptors) | [x] |
| 4 | Structured Data Extraction (HTML → JSON) | [x] |
| 5 | Local Storage Layer (SQLite FTS5) | [x] |
| 6 | Basic Agent Loop (ReAct, multi-step) | [x] |
| 7 | Stealth Hardening & Rate Limiting | [x] |
| 8 | Free-Tier Fallback Layer (ScraperAPI) | [x] |
| 9 | CLI + Minimal HTTP API (localhost:9090) | [x] |
| 10| Scheduling + Polish (schedule.yaml, logs) | [x] |

---

## Step 11 — Self-Hosted Search Engine (SearXNG)
- [x] Run SearXNG via Docker Compose locally (port 8888, JSON enabled).
- [x] Build `internal/search/searxng.go` to hit SearXNG.
- [x] Wire into agent (`web_search` tool) and `/search` HTTP endpoint.

## Step 12 — Site & Sitemap Crawling (Multi-Page Discovery)
- [x] Build `internal/crawl/discover.go` for `/robots.txt` and `sitemap.xml`.
- [x] Fallback to breadth-first `<a href>` crawl via colly.
- [x] Add URL queue (`internal/crawl/queue.go`) with status tracking.

## Step 13 — Concurrent Browser Pool (Real Parallelism)
- [x] Build `internal/browser/pool.go` worker pool of N go-rod instances.
- [x] Feed URLs from Step 12 queue into pool.
- [x] Tune pool size (e.g. `runtime.NumCPU()`) and respect rate limits.

---
## What v1 Deliberately Excludes (future v2+)
- Residential proxy rotation (paid), CAPTCHA solving, Distributed scaling, Web UI, Local model hosting.
| [x] | 14 | Deep Research Mode | Orchestrator/Worker parallel research |