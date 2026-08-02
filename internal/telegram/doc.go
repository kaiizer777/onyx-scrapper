// Package telegram implements the Telegram chat gateway for Onyx Scrapper.
//
// The gateway reuses Onyx's existing agent, deep-research, fetch, extract,
// and search engines as a thin Telegram front-end — no duplicated business
// logic, no new LLM dependencies.
//
// Phase 0 + Phase 1 (config & secrets) are in place. Phase 2 will land the
// bot bootstrap and auth middleware here.
package telegram

// blank import kept in build.go so `go mod tidy` retains the
// go-telegram-bot-api/v5 module until Phase 2 wires the real gateway.
var _ = struct{}{}
