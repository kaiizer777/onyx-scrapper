# ──────────────────────────────────────────────────────────────────────────────
# Stage 1: Build
# ──────────────────────────────────────────────────────────────────────────────
FROM golang:1.24-bookworm AS builder

# modernc.org/sqlite is pure-Go — no CGO required.
ENV CGO_ENABLED=0
ENV GOOS=linux
ENV GOARCH=amd64
# Allow the Go toolchain to auto-download the version specified in go.mod
# (required if go.mod declares a newer patch version than the base image ships).
ENV GOTOOLCHAIN=auto

WORKDIR /src

# Layer-cache: download dependencies before copying source so this layer
# is only invalidated when go.mod / go.sum change, not on every source edit.
COPY go.mod go.sum ./
RUN go mod download

# Copy the rest of the source and build.
COPY . .
RUN go build -trimpath -ldflags="-s -w" -o /onyx ./cmd/onyx

# ──────────────────────────────────────────────────────────────────────────────
# Stage 2: Runtime
# ──────────────────────────────────────────────────────────────────────────────
# Debian bookworm-slim: chosen over Alpine because Chromium in the Debian repo
# is glibc-linked, which is consistent with the Go binary and avoids musl compat
# issues. go-rod's fallback auto-download is also glibc-based.
FROM debian:bookworm-slim AS runtime

# Install Chromium and required shared libs/fonts.
# fonts-liberation: prevents "No fonts found" crash in headless Chromium.
# wget: used by HEALTHCHECK.
RUN apt-get update && apt-get install -y --no-install-recommends \
        chromium \
        wget \
        ca-certificates \
        fonts-liberation \
        libglib2.0-0 \
        libnss3 \
        libatk1.0-0 \
        libatk-bridge2.0-0 \
        libx11-6 \
        libxcomposite1 \
        libxdamage1 \
        libxext6 \
        libxfixes3 \
        libxrandr2 \
        libgbm1 \
        libxkbcommon0 \
        libpango-1.0-0 \
        libcairo2 \
        libasound2 \
    && rm -rf /var/lib/apt/lists/*

# Tell go-rod's launcher to use the system Chromium binary.
# go-rod reads the ROD env var (uppercase, case-sensitive on Linux).
# Setting bin= disables the auto-downloader; no_sandbox is required for
# non-root users without the SYS_ADMIN capability.
ENV ROD="bin=/usr/bin/chromium no_sandbox"

# App-level flag read by browser.go / pool.go (belt-and-suspenders).
ENV CHROMIUM_NO_SANDBOX=1

# Create a dedicated non-root user.
RUN groupadd --gid 1001 onyx && \
    useradd --uid 1001 --gid onyx --shell /bin/sh --create-home onyx

# Create and own the data directory (SQLite DB lives here).
RUN mkdir -p /app/data && chown -R onyx:onyx /app

WORKDIR /app

COPY --from=builder --chown=onyx:onyx /onyx /app/onyx

# Mount point for SQLite persistence. Named volumes survive docker compose down.
VOLUME ["/app/data"]

EXPOSE 9090

USER onyx

ENTRYPOINT ["/app/onyx"]
CMD ["serve", "--port", "9090"]

# /health is registered by api/server.go on the HTTP mux.
HEALTHCHECK --interval=30s --timeout=10s --start-period=15s --retries=3 \
    CMD wget -qO- http://localhost:9090/health > /dev/null || exit 1
