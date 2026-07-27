# Onyx Scrapper

> **Personal AI Web Agent, Structured Scraper & Autonomous Search Engine**  
> *Built with Go, go-rod/stealth, Colly, SQLite (FTS5), and MiMo V2.5 LLM.*

---

## 🚀 Overview

**Onyx Scrapper** is a zero-subscription ($0/month operational budget), high-performance web agent and web scraping engine designed for structured extraction, autonomous multi-step browser tasks, local full-text search, and automated recurring scraping workflows.

---

## 🏗️ System Architecture

```
                       +-------------------------------+
                       |    CLI (`onyx`) / HTTP API    |
                       |      (Port: localhost:9090)   |
                       +---------------+---------------+
                                       |
       +-------------------------------+-------------------------------+
       |                               |                               |
+------v------+                 +------v------+                 +------v------+
| Static Fetch|                 |  Headless   |                 | Autonomous  |
|   (Colly)   |                 | Browser Pool|                 | ReAct Agent |
|             |                 | (go-rod)    |                 |   Loop      |
+------+------+                 +------+------+                 +------+------+
       |                               |                               |
       +-------------------------------+-------------------------------+
                                       |
                     +-----------------v-----------------+
                     | Stealth, Rate-Limiting & Fallback |
                     |   (ScraperAPI Circuit Breaker)    |
                     +-----------------+-----------------+
                                       |
                     +-----------------v-----------------+
                     |   Structured Extraction (MiMo)    |
                     |  & Semantic Element Finder        |
                     +-----------------+-----------------+
                                       |
                     +-----------------v-----------------+
                     | Local SQLite Storage (FTS5 Index) |
                     | data/onyx.db (Pages, Extractions) |
                     +-----------------------------------+
```

---

## ✨ Features

- **⚡ Dual Fetching Engine**: Fast static HTTP scraping via `Colly` with automatic fallback to `go-rod` stealth headless Chromium for JavaScript-rendered SPAs.
- **🥷 Stealth Hardening**: Evasion techniques for bot detection (randomized user-agents, viewports, headers, human-like action delays, per-domain rate limiting, and `robots.txt` compliance).
- **🛡️ Circuit Breaker & Free Fallback**: Per-domain failure tracking that auto-routes blocked requests through ScraperAPI's free-tier endpoint.
- **🎯 Semantic Element Finder**: AI-powered DOM locator using natural language descriptions instead of manual, brittle CSS selectors.
- **📊 Structured JSON Extraction**: Schema-driven extraction (articles, products, events, or custom JSON schemas) powered by MiMo V2.5.
- **🤖 ReAct Agent Loop**: Autonomous multi-step agent capable of planning, navigating, finding elements, typing, clicking, and extracting data to fulfill plain-English goals.
- **🔍 Local Knowledge Lake (FTS5)**: Embedded SQLite database with full-text search indexing (`pages`, `extractions`, `agent_runs`, `agent_steps`).
- **⏰ Ticker Scheduler Daemon**: Background recurring scraper driven by `schedule.yaml` for unattended monitoring.
- **🌐 Harness-Friendly HTTP API**: Stateless JSON endpoints (`http://localhost:9090`) designed to interface cleanly with external agentic tools.
- **📄 Structured Logging (`log/slog`)**: Standardized logging with support for `--json` machine-readable output across CLI commands.

---

## 📦 Installation & Setup

### Prerequisites
- **Go**: 1.21+ (tested on Go 1.25)
- **Chromium / Chrome**: Installed on host machine (auto-launched by `go-rod`).
- **Docker / Docker Compose**: Required for local self-hosted SearXNG aggregator.

### Self-Hosted Search Engine Setup (SearXNG)
To enable unlimited local web search capabilities for the CLI, HTTP API, and ReAct agent:
```bash
docker-compose up -d
```
SearXNG will run on `http://localhost:8888` with JSON format output enabled.

### Installation
```bash
git clone https://github.com/kaiizer777/onyx-scrapper.git
cd onyx-scrapper
go build -o onyx ./cmd/onyx
```

### Configuration
Create `config.yaml` or `.env` in the root directory:

```yaml
opencode_zen:
  base_url: "https://api.xiaomimimo.com/v1"
  api_key: "YOUR_MIMO_API_KEY"
  model: "mimo-v2.5-pro"

scraperapi_key: "YOUR_SCRAPERAPI_KEY" # Optional free-tier fallback key
```

---

## 💻 CLI Usage Reference

### 1. Ping LLM Connection
```bash
onyx ping
onyx ping --json
```

### 2. Fetch & Clean Web Content
```bash
onyx fetch https://example.com
onyx fetch https://example.com --render
onyx fetch https://example.com --json
```

### 3. Locate DOM Element via Natural Language
```bash
onyx find https://example.com "the main login button"
onyx find https://example.com "search input box" --render --json
```

### 4. Extract Structured Data
```bash
onyx extract https://example.com/product --schema product
onyx extract https://news.ycombinator.com --schema article --json
```

### 5. Full-Text Search Saved Scrapes
```bash
onyx search "golang scraping"
onyx search "artificial intelligence" --json
```

### 6. Autonomous ReAct Agent
```bash
onyx agent "go to news.ycombinator.com, find top story, and extract title"
onyx agent "search for books on example.com" --max-steps 10 --json
```

### 7. Background Scheduler Daemon
```bash
onyx schedule --config schedule.yaml
```

### 8. HTTP API Server
```bash
onyx serve --port 9090
```

### 9. Stealth & Fallback Verification
```bash
onyx test-stealth
onyx test-fallback https://example.com
```

---

## 🌐 HTTP API Reference (`localhost:9090`)

| Endpoint | Method | Description | Request Example |
|---|---|---|---|
| `/ping` | GET | Healthcheck endpoint | `curl http://localhost:9090/ping` |
| `/search` | GET/POST | Query local database | `curl "http://localhost:9090/search?q=golang"` |
| `/fetch` | POST | Fetch & clean URL content | `curl -X POST http://localhost:9090/fetch -H "Content-Type: application/json" -d '{"url":"https://example.com"}'` |
| `/extract` | POST | Extract structured JSON | `curl -X POST http://localhost:9090/extract -H "Content-Type: application/json" -d '{"url":"https://example.com","schema":"article"}'` |
| `/agent` | POST | Trigger autonomous agent | `curl -X POST http://localhost:9090/agent -H "Content-Type: application/json" -d '{"goal":"extract title from https://example.com"}'` |

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

- `pages`: `id`, `url`, `fetched_at`, `raw_html`, `clean_text`
- `extractions`: `id`, `page_id`, `schema_name`, `data_json`, `created_at`
- `pages_fts`: Virtual FTS5 search index (`url`, `clean_text`)
- `agent_runs`: `id`, `goal`, `status`, `result`, `started_at`, `completed_at`
- `agent_steps`: `id`, `run_id`, `step_number`, `action`, `args`, `thought`, `result`, `error`, `created_at`

---

## 🧪 Testing

Run all unit tests across internal packages:
```bash
go test -v ./...
```
