# Onyx Scrapper

Onyx Scrapper is a powerful, self-hosted web scraper and agent framework. It uses headless Chromium via `go-rod` to render javascript-heavy pages and `SearXNG` for private, rate-limit-free web searching.

## Quick Start (Docker)

The easiest way to run Onyx Scrapper is using Docker Compose. This ensures both Onyx and its companion SearXNG service are configured perfectly.

### Prerequisites
- Docker and Docker Compose installed
- `git` installed

### 1. Setup

Clone the repository and prepare the configuration:

```bash
git clone https://github.com/kaiizer777/onyx-scrapper.git
cd onyx-scrapper

# Create the environment config file
cp .env.example .env
```

### 2. Configuration (Optional)

Open `.env` in a text editor to add any API keys you might need.

- **`SCRAPERAPI_KEY`**: (Optional) Fallback provider for bypassing heavy anti-bot protections.
- **`TELEGRAM_BOT_TOKEN`**: (Optional) If you want to control Onyx via Telegram.
- **`ONYX_PORT`**: Defaults to `9090`. Change this if you have port conflicts.

### 3. Start the Services

Run the following command to download and start both services in the background:

```bash
docker compose up -d
```

### 4. Verification

Wait about 15-30 seconds for the containers to fully initialize, then verify the web interface is accessible:

- Open your browser to: [http://localhost:9090](http://localhost:9090)
- To check the health of the internal search integration, visit: [http://localhost:9090/health/searx](http://localhost:9090/health/searx)

### 5. Stopping

To stop the services while preserving your SQLite database and downloaded data:

```bash
docker compose down
```

### Data Persistence

All your application data (like the SQLite database) is safely stored in a Docker named volume called `onyx-data`. It will automatically persist across container restarts and upgrades.