# Onyx Scrapper

> **A self-hosted, fully free AI web agent that searches, browses, and extracts structured data autonomously — no subscriptions, no API markup, no limits.**
> *Go · go-rod/stealth · Colly · SQLite (FTS5) · SearXNG · TinyFish · Jina Reader · MiMo V2.5 — $0/month, forever.*

---

## 🚀 Overview

**Onyx Scrapper** is a zero-subscription ($0/month operational budget), high-performance web agent and deep-research engine. It combines static/headless scraping, an autonomous ReAct agent, and a parallel **Deep Research Orchestrator** with a multi-provider discovery layer — so research quality no longer depends on a single search backend.

---

## 🏗️ System Architecture

```
                         +---------------------------------+
                         |     CLI (`onyx`) / HTTP API      |
                         |       (Port: localhost:9090)     |
                         +-----------------+-----------------+
                                           |
   +---------------+---------------+---------------+---------------+
   |               |               |               |               |
+--v---+     +-----v-----+   +-----v-----+   +-----v-----+   +-----v-----+
|Static|     |  Headless  |   |Autonomous |   |   Deep    |   | Scheduler |
|Fetch |     |  Browser   |   |ReAct Agent|   | Research  |   |  Daemon   |
|Colly |     | Pool(rod)  |   |   Loop    |   |Orchestrat.|   |(ticker)   |
+--+---+     +-----+-----+   +-----+-----+   +-----+-----+   +-----------+
   |               |               |               |
   +---------------+---------------+---------------+
                                   |
                 +-----------------v------------------+
                 |     Discovery & Retrieval Layer     |
                 |  SearXNG · TinyFish Search/Fetch ·  |
                 |     Jina Reader (r./s.jina.ai)      |
                 +-----------------+-------------------+
                                   |
                 +-----------------v------------------+
                 | Stealth, Rate-Limiting & Fallback  |
                 |  (ScraperAPI + TinyFish Circuit      |
                 |          Breaker Chain)              |
                 +-----------------+-------------------+
                                   |
                 +-----------------v------------------+
                 |   Structured Extraction (MiMo)     |
                 |  Semantic Element Finder · Reranker|
                 +-----------------+-------------------+
                                   |
                 +-----------------v------------------+
                 |  Local SQLite Storage (FTS5 Index) |
                 |   data/onyx.db (Findings, Runs)    |
                 +-------------------------------------+
```

> **Discovery layer note:** SearXNG remains the default, unlimited, self-hosted search backend. TinyFish (free Search + Fetch API, no card required) and Jina Reader (`r.jina.ai` / `s.jina.ai`, free/no-key tier) are added as **parallel sources and fallbacks** — not replacements. If one is rate-limited or a target blocks it, Onyx routes to the next without operator intervention. All rendering, stealth, extraction, storage, orchestration, and scheduling logic remains implemented independently in Onyx's own codebase.

---

## ✨ Features

- **⚡ Dual Fetching Engine**: Fast static HTTP scraping via `Colly` with automatic fallback to `go-rod` stealth headless Chromium for JS-rendered SPAs.
- **🥷 Stealth Hardening**: Randomized user-agents, viewports, headers, human-like delays, per-domain rate limiting, `robots.txt` compliance.
- **🛡️ Multi-Tier Circuit Breaker**: Per-domain failure tracking that auto-routes blocked requests through TinyFish Fetch first, then ScraperAPI's free-tier endpoint as final fallback — maximizing success rate before any paid credit is touched.
- **🌐 Multi-Provider Discovery**: SearXNG (primary, unlimited) + TinyFish Search (free, agent-native, includes news/image/local results) + Jina `s.jina.ai` (query→clean-content in one call) queried in parallel; results deduped by URL before entering the research pipeline.
- **📖 Token-Efficient Extraction**: Jina Reader (`r.jina.ai`) as a lightweight fallback fetcher that strips nav/ads/scripts server-side, reducing context bloat before content reaches MiMo.
- **🎯 Semantic Element Finder**: AI-powered DOM locator using natural language instead of brittle CSS selectors.
- **📊 Structured JSON Extraction**: Schema-driven extraction (articles, products, events, custom schemas) via MiMo V2.5.
- **🤖 ReAct Agent Loop**: Autonomous multi-step agent — plans, navigates, finds elements, types, clicks, extracts — from a plain-English goal.
- **🧠 Deep Research Mode**: Multi-threaded orchestrator that decomposes a query into sub-questions, spawns parallel worker agents against the full discovery layer, and compiles a cited report. Includes a web UI.
- **⚖️ Finding Confidence & Reranking**: Optional Jina Reranker pass (free tier, 100 RPM) scores retrieved chunks for relevance before synthesis, and cross-source claims are flagged when confidence disagrees across `findings` rows.
- **🔍 Local Knowledge Lake (FTS5)**: Embedded SQLite with full-text search across `pages`, `extractions`, `agent_runs`, `agent_steps`, `research_runs`, `research_subquestions`, `findings`.
- **⏰ Ticker Scheduler Daemon**: Background recurring scraper driven by `schedule.yaml` for unattended monitoring.
- **🌐 Harness-Friendly HTTP API**: Stateless JSON endpoints (`localhost:9090`) for external agentic tools.
- **📄 Structured Logging (`log/slog`)**: Standardized logging with `--json` machine-readable output across all CLI commands.

---

## 📦 Installation & Setup

### Prerequisites
- **Go**: 1.21+ (tested on Go 1.25)
- **Chromium / Chrome**: installed on host (auto-launched by `go-rod`)
- **Docker / Docker Compose**: required for local self-hosted SearXNG

### Self-Hosted Search Engine Setup (SearXNG)
```bash
docker-compose up -d
```
Runs on `http://localhost:8888` with JSON output enabled.

### Installation
*(Built and tested on Windows. Use `.\onyx.exe` for Windows, `./onyx` for Linux/Mac.)*
```bash
git clone https://github.com/kaiizer777/onyx-scrapper.git
cd onyx-scrapper
go build -o onyx.exe ./cmd/onyx
```

### Configuration
Create `config.yaml` or `.env`:

```yaml
opencode_zen:
  base_url: https://opencode.ai/zen/v1
  api_key: YOUR_API_KEY
  default_model: mimo-v2.5-free

# Discovery layer — all optional, all free-tier
tinyfish:
  api_key: "YOUR_TINYFISH_API_KEY"    # free, no card — agent.tinyfish.ai/sign-up
  enabled: true

jina:
  api_key: ""                          # optional; works keyless at lower rate limit
  reader_base: "https://r.jina.ai"
  search_base: "https://s.jina.ai"
  reranker_enabled: true

scraperapi_key: "YOUR_SCRAPERAPI_KEY"  # optional, last-resort fallback
```

---

## 💻 CLI Usage Reference

### 1. Ping LLM Connection
```bash
.\onyx.exe ping
.\onyx.exe ping --json
```

### 2. Fetch & Clean Web Content
```bash
.\onyx.exe fetch https://example.com
.\onyx.exe fetch https://example.com --render
.\onyx.exe fetch https://example.com --source jina    # force Jina Reader path
```

### 3. Locate DOM Element via Natural Language
```bash
.\onyx.exe find https://example.com "the main login button"
.\onyx.exe find https://example.com "search input box" --render --json
```

### 4. Extract Structured Data
```bash
.\onyx.exe extract https://example.com/product --schema product
.\onyx.exe extract https://news.ycombinator.com --schema article --json
```

### 5. Full-Text Search Saved Scrapes
```bash
.\onyx.exe search "golang scraping"
.\onyx.exe search "artificial intelligence" --json
```

### 6. Autonomous ReAct Agent
```bash
.\onyx.exe agent "go to news.ycombinator.com, find top story, and extract title"
.\onyx.exe agent "search for books on example.com" --max-steps 10 --json
```

### 7. Deep Research
```bash
.\onyx.exe research "latest advancements in solid state batteries"
.\onyx.exe research "compare EU vs US AI regulation" --max-subquestions 6 --sources searxng,tinyfish,jina --json
```

### 8. Crawl Site or Sitemap
```bash
.\onyx.exe crawl example.com --max-pages 50
```

### 9. Background Scheduler Daemon
```bash
.\onyx.exe schedule --config schedule.yaml
```

### 10. HTTP API Server
```bash
.\onyx.exe serve --port 9090
```

### 11. Stealth & Fallback Verification
```bash
.\onyx.exe test-stealth
.\onyx.exe test-fallback https://example.com
```

---

## 🌐 HTTP API Reference (`localhost:9090`)

| Endpoint | Method | Description | Request Example |
|---|---|---|---|
| `/ping` or `/health` | GET | Healthcheck | `curl http://localhost:9090/ping` |
| `/search` | GET/POST | Query local database | `curl "http://localhost:9090/search?q=golang"` |
| `/fetch` | POST | Fetch & clean URL content | `curl -X POST http://localhost:9090/fetch -d '{"url":"https://example.com"}'` |
| `/extract` | POST | Extract structured JSON | `curl -X POST http://localhost:9090/extract -d '{"url":"https://example.com","schema":"article"}'` |
| `/agent` | POST | Trigger autonomous agent | `curl -X POST http://localhost:9090/agent -d '{"goal":"extract title from https://example.com"}'` |
| `/deep-research` | POST | Trigger deep research | `curl -X POST http://localhost:9090/deep-research -d '{"query":"latest advancements in solid state batteries"}'` |
| `/crawl` | POST | Start background crawl | `curl -X POST http://localhost:9090/crawl -d '{"url":"https://example.com"}'` |
| `/ui/research` | GET | Deep Research Web UI | Open `http://localhost:9090/ui/research` |

---

## ⏰ Schedule Configuration (`schedule.yaml`)

```yaml
jobs:
  - name: "Hacker News Front Page"
    url: "https://news.ycombinator.com"
    interval: "1h"
    render: false
    schema: "article"

  - name: "Example Product Monitor"
    url: "https://example.com/product"
    interval: "6h"
    render: true
    schema: "product"
```

---

## 🗄️ Database Schema (`data/onyx.db`)

- `pages`: `id`, `url`, `fetched_at`, `raw_html`, `clean_text`, `source_provider`
- `extractions`: `id`, `page_id`, `schema_name`, `data_json`, `created_at`
- `pages_fts`: Virtual FTS5 search index (`url`, `clean_text`)
- `agent_runs`: `id`, `goal`, `status`, `result`, `started_at`, `completed_at`
- `agent_steps`: `id`, `run_id`, `step_number`, `action`, `args`, `thought`, `result`, `error`, `created_at`
- `research_runs`: `id`, `goal`, `status`, `started_at`, `completed_at`, `report_md`
- `research_subquestions`: `id`, `run_id`, `question`, `status`
- `findings`: `id`, `subquestion_id`, `claim`, `source_url`, `source_provider`, `confidence`, `created_at`

---

## 🧭 Discovery Layer — Provider Roles

| Provider | Role | Cost | Notes |
|---|---|---|---|
| **SearXNG** | Primary search, unlimited | $0 (self-hosted) | No external rate limits; default for all discovery |
| **TinyFish Search** | Parallel discovery source | $0 (free tier, no card) | Adds news/image/local coverage SearXNG's indexed engines may miss |
| **TinyFish Fetch** | Stealth fetch fallback (before ScraperAPI) | $0 (free tier) | Full-browser render, strips boilerplate before content hits context |
| **Jina `s.jina.ai`** | Query → clean content, single call | $0 (keyless, rate-limited) | Fastest path for quick lookups inside agent steps |
| **Jina `r.jina.ai`** | URL → clean markdown fallback | $0 (keyless, rate-limited) | Backup extractor when Colly/go-rod are blocked |
| **Jina Reranker** | Relevance scoring pre-synthesis | $0 (free tier, 100 RPM) | Improves signal-to-noise across multi-source findings |
| **ScraperAPI** | Last-resort fallback | $0 (free tier) | Only invoked after TinyFish Fetch fails |

All providers are opt-in via `config.yaml`; disabling any of them degrades gracefully back to SearXNG + go-rod/Colly only.

---

## 🛠️ Known Issues Fixed During Testing

- **Cloudflare Detection False-Positives:** Detection now requires actual challenge-page signatures and short body length, not just vendor name matches.
- **Bare Domain Handling:** Bare domains (e.g. `example.com`) auto-prepend `https://` instead of crashing.

---

## 🧪 Testing

```bash
go test -v ./...
```