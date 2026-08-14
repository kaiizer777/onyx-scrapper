package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/kaiizer777/onyx-scrapper/internal/agent"
	"github.com/kaiizer777/onyx-scrapper/internal/api"
	"github.com/kaiizer777/onyx-scrapper/internal/browser"
	"github.com/kaiizer777/onyx-scrapper/internal/config"
	"github.com/kaiizer777/onyx-scrapper/internal/crawl"
	"github.com/kaiizer777/onyx-scrapper/internal/discovery"
	"github.com/kaiizer777/onyx-scrapper/internal/extract"
	"github.com/kaiizer777/onyx-scrapper/internal/llm"
	"github.com/kaiizer777/onyx-scrapper/internal/research"
	"github.com/kaiizer777/onyx-scrapper/internal/scheduler"
	"github.com/kaiizer777/onyx-scrapper/internal/search"
	"github.com/kaiizer777/onyx-scrapper/internal/store"
	"github.com/kaiizer777/onyx-scrapper/internal/teacher"
	"github.com/kaiizer777/onyx-scrapper/internal/telegram"
	"github.com/kaiizer777/onyx-scrapper/internal/timecontext"
)

const defaultDBPath = "data/onyx.db"

func main() {
	var overrideDate string
	var newArgs []string
	for i := 0; i < len(os.Args); i++ {
		arg := os.Args[i]
		if arg == "--override-date" && i+1 < len(os.Args) {
			overrideDate = os.Args[i+1]
			i++
		} else if strings.HasPrefix(arg, "--override-date=") {
			overrideDate = strings.TrimPrefix(arg, "--override-date=")
		} else {
			newArgs = append(newArgs, arg)
		}
	}
	os.Args = newArgs

	if overrideDate != "" {
		if t, err := time.Parse("2006-01-02", overrideDate); err == nil {
			timecontext.SetOverrideDate(t)
			// Print directly as slog is not set up yet
			fmt.Printf("[DEBUG] TESTING OVERRIDE: System date mocked to %s\n", t.Format("2006-01-02"))
		} else {
			fmt.Printf("Invalid --override-date format %q, expected YYYY-MM-DD\n", overrideDate)
			os.Exit(1)
		}
	}

	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	jsonOutput := hasJSONFlag(os.Args)
	if jsonOutput {
		// Route structured logs to stderr so stdout remains clean JSON for piping
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
	} else {
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))
	}

	// Load config to seed env vars if present
	if cfg, err := config.LoadConfig("config.yaml"); err == nil {
		if key := cfg.GetScraperAPIKey(); key != "" {
			_ = os.Setenv("SCRAPERAPI_KEY", key)
		}
	}

	cmd := os.Args[1]
	ctx := context.Background()

	switch cmd {
	case "ping":
		cfg, err := config.LoadConfig("config.yaml")
		if err != nil {
			slog.Error("Failed to load config", "error", err)
			os.Exit(1)
		}

		client := llm.NewClient(cfg.ActiveProviderConfig())
		resp, err := client.Chat(ctx, []llm.Message{
			{Role: "user", Content: "say hello"},
		})
		if err != nil {
			slog.Error("Chat error", "error", err)
			os.Exit(1)
		}

		if jsonOutput {
			outputJSON(map[string]string{"status": "ok", "response": resp})
		} else {
			fmt.Println(resp)
		}

	case "fetch":
		if len(os.Args) < 3 {
			fmt.Println("Usage: onyx fetch <url> [--render] [--json]")
			os.Exit(1)
		}

		var targetURL string
		forceRender := false
		sourceOverride := "auto"

		for i := 2; i < len(os.Args); i++ {
			arg := os.Args[i]
			if arg == "--render" {
				forceRender = true
			} else if arg == "--json" || arg == "-j" {
				jsonOutput = true
			} else if arg == "--source" && i+1 < len(os.Args) {
				sourceOverride = os.Args[i+1]
				i++
			} else if strings.HasPrefix(arg, "--source=") {
				sourceOverride = strings.TrimPrefix(arg, "--source=")
			} else if targetURL == "" && !strings.HasPrefix(arg, "--") {
				targetURL = arg
			}
		}


		if targetURL == "" {
			fmt.Println("Usage: onyx fetch <url> [--render] [--json]")
			os.Exit(1)
		}

		slog.Debug("Fetch command parsed flags", "sourceOverride", sourceOverride, "jsonOutput", jsonOutput, "forceRender", forceRender)

		rawHTML, usedBrowser, err := extract.Fetch(ctx, targetURL, forceRender)
		if err != nil {
			slog.Error("Error fetching URL", "url", targetURL, "error", err)
			os.Exit(1)
		}

		cleanText, err := extract.CleanHTML(rawHTML)
		if err != nil {
			slog.Error("Error cleaning HTML", "error", err)
			os.Exit(1)
		}

		// Save to local SQLite storage
		st, err := store.NewStore(defaultDBPath)
		if err != nil {
			slog.Warn("Failed to open SQLite storage", "error", err)
		} else {
			defer st.Close()
			pageID, err := st.SavePage(targetURL, rawHTML, cleanText, "cli-fetch", "ok")
			if err != nil {
				slog.Warn("Failed to save page to storage", "error", err)
			} else {
				slog.Info("Saved page to database", "page_id", pageID, "url", targetURL)
			}
		}

		if jsonOutput {
			outputJSON(map[string]interface{}{
				"url":          targetURL,
				"used_browser": usedBrowser,
				"html_bytes":   len(rawHTML),
				"clean_text":   cleanText,
			})
		} else {
			if usedBrowser {
				slog.Info("Fetched via headless browser (go-rod/stealth)", "url", targetURL)
			} else {
				slog.Info("Fetched via static HTTP (colly)", "url", targetURL)
			}
			fmt.Println(cleanText)
		}

	case "find":
		if len(os.Args) < 4 {
			fmt.Println("Usage: onyx find <url> \"<description>\" [--render] [--json]")
			os.Exit(1)
		}

		var targetURL string
		var description string
		forceRender := false

		for _, arg := range os.Args[2:] {
			if arg == "--render" {
				forceRender = true
			} else if arg == "--json" || arg == "-j" {
				jsonOutput = true
			} else if targetURL == "" {
				targetURL = arg
			} else if description == "" {
				description = arg
			}
		}

		if targetURL == "" || description == "" {
			fmt.Println("Usage: onyx find <url> \"<description>\" [--render] [--json]")
			os.Exit(1)
		}

		cfg, err := config.LoadConfig("config.yaml")
		if err != nil {
			slog.Error("Failed to load config", "error", err)
			os.Exit(1)
		}

		client := llm.NewClient(cfg.ActiveProviderConfig())

		slog.Info("Fetching URL for semantic element location", "url", targetURL)
		rawHTML, usedBrowser, err := extract.Fetch(ctx, targetURL, forceRender)
		if err != nil {
			slog.Error("Error fetching URL", "url", targetURL, "error", err)
			os.Exit(1)
		}

		selector, err := extract.FindElement(ctx, client, rawHTML, description)
		if err != nil {
			slog.Error("Error finding element", "error", err)
			os.Exit(1)
		}

		matchCount := 0
		if !strings.HasPrefix(selector, "//") && !strings.HasPrefix(selector, "(") {
			doc, err := goquery.NewDocumentFromReader(strings.NewReader(rawHTML))
			if err == nil {
				matchCount = doc.Find(selector).Length()
			}
		}

		if jsonOutput {
			outputJSON(map[string]interface{}{
				"url":          targetURL,
				"description":  description,
				"selector":     selector,
				"used_browser": usedBrowser,
				"match_count":  matchCount,
			})
		} else {
			fmt.Printf("\n[RESULT] Selector found: %s\n", selector)
			if matchCount > 0 {
				fmt.Printf("[VERIFY] CSS selector matches %d element(s) on page\n", matchCount)
			}
		}

	case "extract":
		if len(os.Args) < 3 {
			fmt.Println("Usage: onyx extract <url> [--schema product|article|event|search-result-list|<json>] [--render] [--json]")
			os.Exit(1)
		}

		var targetURL string
		schemaName := "product"
		forceRender := false

		for i := 2; i < len(os.Args); i++ {
			arg := os.Args[i]
			if arg == "--render" {
				forceRender = true
			} else if arg == "--json" || arg == "-j" {
				jsonOutput = true
			} else if arg == "--schema" && i+1 < len(os.Args) {
				schemaName = os.Args[i+1]
				i++
			} else if strings.HasPrefix(arg, "--schema=") {
				schemaName = strings.TrimPrefix(arg, "--schema=")
			} else if targetURL == "" && !strings.HasPrefix(arg, "--") {
				targetURL = arg
			}
		}

		if targetURL == "" {
			fmt.Println("Usage: onyx extract <url> [--schema product|article|event|search-result-list|<json>] [--render] [--json]")
			os.Exit(1)
		}

		cfg, err := config.LoadConfig("config.yaml")
		if err != nil {
			slog.Error("Failed to load config", "error", err)
			os.Exit(1)
		}

		client := llm.NewClient(cfg.ActiveProviderConfig())

		slog.Info("Fetching page for structured extraction", "url", targetURL, "schema", schemaName)
		rawHTML, usedBrowser, err := extract.Fetch(ctx, targetURL, forceRender)
		if err != nil {
			slog.Error("Error fetching URL", "url", targetURL, "error", err)
			os.Exit(1)
		}

		cleanText, err := extract.CleanHTML(rawHTML)
		if err != nil {
			slog.Error("Error cleaning HTML", "error", err)
			os.Exit(1)
		}

		rawJSON, err := extract.ExtractJSON(ctx, client, rawHTML, schemaName)
		if err != nil {
			slog.Error("Error extracting structured JSON", "error", err)
			os.Exit(1)
		}

		// Save page and extraction to local SQLite storage
		st, err := store.NewStore(defaultDBPath)
		if err != nil {
			slog.Warn("Failed to open SQLite storage", "error", err)
		} else {
			defer st.Close()
			pageID, err := st.SavePage(targetURL, rawHTML, cleanText, "cli-extract", "ok")
			if err != nil {
				slog.Warn("Failed to save page to storage", "error", err)
			} else {
				extID, err := st.SaveExtraction(pageID, schemaName, string(rawJSON))
				if err != nil {
					slog.Warn("Failed to save extraction to storage", "error", err)
				} else {
					slog.Info("Saved page and extraction to database", "page_id", pageID, "extraction_id", extID)
				}
			}
		}

		if jsonOutput {
			var parsed interface{}
			_ = json.Unmarshal(rawJSON, &parsed)
			outputJSON(map[string]interface{}{
				"url":          targetURL,
				"schema":       schemaName,
				"used_browser": usedBrowser,
				"data":         parsed,
			})
		} else {
			var prettyJSON bytes.Buffer
			if err := json.Indent(&prettyJSON, rawJSON, "", "  "); err == nil {
				fmt.Println(prettyJSON.String())
			} else {
				fmt.Println(string(rawJSON))
			}
		}

	case "search":
		if len(os.Args) < 3 {
			fmt.Println("Usage: onyx search \"<query>\" [--json]")
			os.Exit(1)
		}

		var query string
		for _, arg := range os.Args[2:] {
			if arg == "--json" || arg == "-j" {
				jsonOutput = true
			} else if query == "" && !strings.HasPrefix(arg, "--") {
				query = arg
			}
		}

		if query == "" {
			fmt.Println("Usage: onyx search \"<query>\" [--json]")
			os.Exit(1)
		}

		st, err := store.NewStore(defaultDBPath)
		if err != nil {
			slog.Warn("Failed to open SQLite database", "error", err)
		} else {
			defer st.Close()
		}

		searchSvc := search.NewService(st)
		res, err := searchSvc.Search(ctx, query)
		if err != nil {
			slog.Error("Search failed", "query", query, "error", err)
			os.Exit(1)
		}

		if jsonOutput {
			outputJSON(res)
		} else {
			if len(res.Results) == 0 {
				fmt.Printf("No matching results found for query: %q\n", query)
				return
			}

			fmt.Printf("Found %d result(s) for query %q:\n\n", len(res.Results), query)
			for i, item := range res.Results {
				fmt.Printf("%d. %s\n   URL: %s\n   Snippet: %s\n\n", i+1, item.Title, item.URL, item.Snippet)
			}
		}


	case "agent":
		if len(os.Args) < 3 {
			fmt.Println("Usage: onyx agent \"<goal>\" [--no-entity-check] [--json]")
			os.Exit(1)
		}

		var goal string
		noEntityCheck := false

		for i := 2; i < len(os.Args); i++ {
			arg := os.Args[i]
			if arg == "--json" || arg == "-j" {
				jsonOutput = true
			} else if arg == "--no-entity-check" {
				noEntityCheck = true
			} else if goal == "" && !strings.HasPrefix(arg, "--") {
				goal = arg
			}
		}

		if goal == "" {
			fmt.Println("Usage: onyx agent \"<goal>\" [--json]")
			os.Exit(1)
		}

		cfg, err := config.LoadConfig("config.yaml")
		if err != nil {
			slog.Error("Failed to load config", "error", err)
			os.Exit(1)
		}

		if noEntityCheck {
			if cfg.Quality == nil {
				cfg.Quality = &config.QualityConfig{}
			}
			falseVal := false
			cfg.Quality.EntityFreshness.Enabled = &falseVal
		}

		client := llm.NewClient(cfg.ActiveProviderConfig())
		st, err := store.NewStore(defaultDBPath)
		if err != nil {
			slog.Error("Failed to open SQLite database", "error", err)
			os.Exit(1)
		}
		defer st.Close()

		ag := agent.NewAgent(client, st, agent.WithRegistry(buildRegistry(cfg)))
		slog.Info("Starting agent execution", "goal", goal)

		var stepLogs []map[string]interface{}

		run, err := ag.Run(ctx, goal, 0, func(stepNum int, thought, action, args, result string, err error) {
			statusStr := "success"
			errStr := ""
			if err != nil {
				statusStr = "failed"
				errStr = err.Error()
			}

			if jsonOutput {
				stepLogs = append(stepLogs, map[string]interface{}{
					"step":    stepNum,
					"action":  action,
					"thought": thought,
					"args":    args,
					"status":  statusStr,
					"error":   errStr,
				})
			} else {
				fmt.Printf("\n--- [STEP %d] --- Action: %s ---\n", stepNum, action)
				if thought != "" {
					fmt.Printf("  Thought: %s\n", thought)
				}
				if args != "" {
					fmt.Printf("  Args:    %s\n", args)
				}
				if err != nil {
					fmt.Printf("  Status:  [FAILED] Error: %v\n", err)
				} else {
					resPreview := result
					if len(resPreview) > 300 {
						resPreview = resPreview[:300] + "... [truncated]"
					}
					fmt.Printf("  Status:  [SUCCESS] Result:\n%s\n", resPreview)
				}
			}
		})

		if err != nil {
			if jsonOutput {
				outputJSON(map[string]interface{}{
					"status": "failed",
					"goal":   goal,
					"error":  err.Error(),
					"steps":  stepLogs,
				})
			} else {
				slog.Error("Agent run failed", "error", err)
			}
			os.Exit(1)
		}

		if jsonOutput {
			outputJSON(map[string]interface{}{
				"run_id":   run.ID,
				"status":   run.Status,
				"goal":     run.Goal,
				"result":   run.Result,
				"steps":    stepLogs,
			})
		} else {
			fmt.Printf("\n==================================================\n")
			fmt.Printf("AGENT RUN COMPLETE (Run ID: %d)\n", run.ID)
			fmt.Printf("Status: %s\n", run.Status)
			fmt.Printf("Final Result:\n%s\n", run.Result)
			fmt.Printf("==================================================\n")
		}

	case "deep-research":
		if len(os.Args) < 3 && !strings.Contains(strings.Join(os.Args, " "), "--resume") {
			fmt.Println("Usage: onyx deep-research \"<goal>\" [--resume <id>] [--no-entity-check] [--json]")
			os.Exit(1)
		}

		var goal string
		var resumeID int64
		var sourcesList string
		noEntityCheck := false
		qualityReport := false

		for i := 2; i < len(os.Args); i++ {
			arg := os.Args[i]
			if arg == "--json" || arg == "-j" {
				jsonOutput = true
			} else if (arg == "--resume") && i+1 < len(os.Args) {
				if n, err := strconv.ParseInt(os.Args[i+1], 10, 64); err == nil && n > 0 {
					resumeID = n
				}
				i++
			} else if strings.HasPrefix(arg, "--resume=") {
				val := strings.TrimPrefix(arg, "--resume=")
				if n, err := strconv.ParseInt(val, 10, 64); err == nil && n > 0 {
					resumeID = n
				}
			} else if arg == "--no-entity-check" {
				noEntityCheck = true
			} else if arg == "--quality-report" {
				qualityReport = true
			} else if arg == "--sources" && i+1 < len(os.Args) {
				sourcesList = os.Args[i+1]
				i++
			} else if strings.HasPrefix(arg, "--sources=") {
				sourcesList = strings.TrimPrefix(arg, "--sources=")
			} else if goal == "" && !strings.HasPrefix(arg, "--") {
				goal = arg
			}
		}

		if goal == "" && resumeID == 0 {
			fmt.Println("Usage: onyx deep-research \"<goal>\" [--resume <id>] [--json]")
			os.Exit(1)
		}

		slog.Debug("Deep-research command parsed flags", "sourcesList", sourcesList, "resumeID", resumeID)

		cfg, err := config.LoadConfig("config.yaml")
		if err != nil {
			slog.Error("Failed to load config", "error", err)
			os.Exit(1)
		}

		if noEntityCheck {
			if cfg.Quality == nil {
				cfg.Quality = &config.QualityConfig{}
			}
			falseVal := false
			cfg.Quality.EntityFreshness.Enabled = &falseVal
		}

		client := llm.NewClient(cfg.ActiveProviderConfig())
		st, err := store.NewStore(defaultDBPath)
		if err != nil {
			slog.Error("Failed to open SQLite database", "error", err)
			os.Exit(1)
		}
		defer st.Close()
		registry := buildRegistry(cfg)

		orchestrator := research.NewOrchestrator(client, st, registry, cfg)
		slog.Info("Starting deep research", "goal", goal, "resume_id", resumeID)

		opts := research.Options{
			ResumeRunID: resumeID,
		}

		run, err := orchestrator.Run(ctx, goal, opts)
		if err != nil {
			if jsonOutput {
				outputJSON(map[string]interface{}{
					"status": "failed",
					"goal":   goal,
					"error":  err.Error(),
				})
			} else {
				slog.Error("Deep research failed", "error", err)
			}
			os.Exit(1)
		}

		var budgetStats map[string]interface{}
		if qualityReport {
			budget := orchestrator.GetQualityBudget()
			if budget != nil {
				curr, max := budget.Stats()
				budgetStats = map[string]interface{}{
					"extra_calls_used": curr,
					"extra_calls_max":  max,
				}
			}
		}

		if jsonOutput {
			out := map[string]interface{}{
				"run_id": run.ID,
				"status": run.Status,
				"goal":   run.Goal,
				"report": run.ReportMD,
			}
			if budgetStats != nil {
				out["quality_budget"] = budgetStats
			}
			outputJSON(out)
		} else {
			fmt.Printf("\n==================================================\n")
			fmt.Printf("DEEP RESEARCH COMPLETE (Run ID: %d)\n", run.ID)
			fmt.Printf("Status: %s\n", run.Status)
			if budgetStats != nil {
				b, _ := json.MarshalIndent(budgetStats, "", "  ")
				fmt.Printf("Quality Budget:\n%s\n", string(b))
			}
			fmt.Printf("Final Report:\n\n%s\n", run.ReportMD)
			fmt.Printf("==================================================\n")
		}

	case "schedule":
		configFile := "schedule.yaml"
		for i := 2; i < len(os.Args); i++ {
			arg := os.Args[i]
			if (arg == "--config" || arg == "-c") && i+1 < len(os.Args) {
				configFile = os.Args[i+1]
				i++
			} else if strings.HasPrefix(arg, "--config=") {
				configFile = strings.TrimPrefix(arg, "--config=")
			} else if arg == "--json" || arg == "-j" {
				jsonOutput = true
			}
		}

		// Load config.yaml first.
		cfg, _ := config.LoadConfig("config.yaml")
		var client *llm.Client
		if cfg != nil {
			client = llm.NewClient(cfg.ActiveProviderConfig())
		}

		schedCfg, err := scheduler.LoadConfig(configFile)
		if err != nil {
			slog.Error("Failed to load schedule config", "file", configFile, "error", err)
			os.Exit(1)
		}

		st, err := store.NewStore(defaultDBPath)
		if err != nil {
			slog.Warn("Failed to open SQLite database for scheduler", "error", err)
		} else {
			defer st.Close()
		}

		// Scheduler options.
		apiKey := os.Getenv("SCRAPERAPI_KEY")
		var schedOpts []scheduler.Option
		schedOpts = append(schedOpts, scheduler.WithAPIKey(apiKey))

		sched := scheduler.NewScheduler(schedCfg, st, client, schedOpts...)

		stopChan := make(chan os.Signal, 1)
		signal.Notify(stopChan, os.Interrupt, syscall.SIGTERM)

		schedCtx, cancel := context.WithCancel(ctx)
		defer cancel()

		slog.Info("Starting Onyx Scrapper scheduler daemon",
			"config", configFile,
			"scrape_jobs", len(schedCfg.Jobs),
		)
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"status":     "started",
				"config":     configFile,
				"scrape_jobs": len(schedCfg.Jobs),
			})
		}

		go func() {
			if err := sched.Start(schedCtx); err != nil {
				slog.Error("Scheduler error", "error", err)
			}
		}()

		<-stopChan
		slog.Info("Shutdown signal received, stopping scheduler...")
		cancel()
		slog.Info("Scheduler daemon stopped gracefully.")

	case "serve":
		port := 9090
		withTelegram := false
		for i := 2; i < len(os.Args); i++ {
			arg := os.Args[i]
			if (arg == "--port" || arg == "-p") && i+1 < len(os.Args) {
				if p, err := strconv.Atoi(os.Args[i+1]); err == nil && p > 0 {
					port = p
				}
				i++
			} else if strings.HasPrefix(arg, "--port=") {
				if p, err := strconv.Atoi(strings.TrimPrefix(arg, "--port=")); err == nil && p > 0 {
					port = p
				}
			} else if arg == "--with-telegram" {
				withTelegram = true
			}
		}

		cfg, err := config.LoadConfig("config.yaml")
		if err != nil {
			slog.Warn("config.yaml not loaded", "error", err)
		}

		var client *llm.Client
		if cfg != nil {
			client = llm.NewClient(cfg.ActiveProviderConfig())
		}

		st, err := store.NewStore(defaultDBPath)
		if err != nil {
			slog.Warn("Failed to open SQLite storage for API server", "error", err)
		} else {
			defer st.Close()
		}

		registry := buildRegistry(cfg)

		// Optional: when the Telegram gateway is enabled and configured
		// for webhook mode, mount its handler on the existing serve mux
		// at /telegram/webhook. This keeps the gateway on a single port
		// with the rest of the HTTP API, sharing the same TLS-terminating
		// reverse proxy in front.
		var serverOpts []api.Option
		teacherOrch := teacher.NewOrchestrator(client, st, registry, cfg)
		serverOpts = append(serverOpts,
			api.WithPort(port),
			api.WithLLMClient(client),
			api.WithStore(st),
			api.WithRegistry(registry),
			api.WithTeacherOrchestrator(teacherOrch),
		)

		var tgPoller *telegram.Poller
		var pollerCtx context.Context
		var pollerCancel context.CancelFunc

		if cfg != nil && (cfg.IsTelegramEnabled() || withTelegram) {
			tgBot, tgErr := telegram.NewBot(ctx, cfg.GetTelegramBotToken(), telegramCfgFromConfig(cfg))
			if tgErr != nil {
				slog.Warn("Telegram bot init failed; gateway will NOT be mounted/started", "error", tgErr)
			} else {
				tgBotCfg := telegramCfgFromConfig(cfg)
				tgAuth := telegram.NewAuthenticator(tgBotCfg, telegram.PolicySilentDrop, false)
				tgRouter, _ := buildTelegramRouter(ctx, tgBot, tgBotCfg, cfg, st)
				
				if strings.EqualFold(tgBotCfg.Mode, "webhook") {
					tgSecret := ""
					if cfg.Telegram != nil {
						tgSecret = cfg.Telegram.Webhook.SecretToken
					}
					tgHandler := telegram.NewWebhookHandler(tgBot, tgAuth, tgRouter.Handle, tgSecret)
					serverOpts = append(serverOpts, api.WithTelegramWebhook(tgHandler))
					slog.Info("Telegram webhook mounted on serve mux",
						"path", "/telegram/webhook",
						"public_url", cfg.Telegram.Webhook.PublicURL,
					)
				} else {
					pollerCtx, pollerCancel = context.WithCancel(ctx)
					tgPoller = telegram.NewPoller(tgBot, tgAuth, tgRouter.Handle)
					if _, err := telegram.ReconcileMode(ctx, tgBot.API, "polling"); err != nil {
						slog.Warn("ReconcileMode on polling start reported a non-fatal error", "error", err)
					}
					slog.Info("Telegram polling gateway initialized for serve daemon")
				}
			}
		}

		srv := api.NewServer(serverOpts...)

		slog.Info("Starting Onyx Scrapper HTTP API Server", "port", port, "url", fmt.Sprintf("http://localhost:%d", port))

		stopChan := make(chan os.Signal, 1)
		signal.Notify(stopChan, os.Interrupt, syscall.SIGTERM)

		go func() {
			if err := srv.Start(); err != nil && err != http.ErrServerClosed {
				slog.Error("API Server failure", "error", err)
			}
		}()

		if tgPoller != nil {
			go func() {
				if err := tgPoller.Run(pollerCtx); err != nil {
					slog.Error("Telegram poller failure", "error", err)
				}
			}()
		}

		<-stopChan
		slog.Info("Shutting down Onyx API Server...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			slog.Error("Server shutdown error", "error", err)
		} else {
			slog.Info("Onyx API Server stopped cleanly")
		}
		if pollerCancel != nil {
			pollerCancel()
			slog.Info("Telegram polling gateway stopped cleanly")
		}

	case "test-teacher":
		runTestTeacher(ctx, jsonOutput)

	case "test-stealth":
		targetURL := "https://bot.sannysoft.com/"
		slog.Info("Running stealth verification", "url", targetURL)

		htmlContent, err := browser.FetchRendered(ctx, targetURL, 45*time.Second)
		if err != nil {
			slog.Error("Stealth test failed", "error", err)
			os.Exit(1)
		}

		doc, err := goquery.NewDocumentFromReader(strings.NewReader(htmlContent))
		if err != nil {
			slog.Error("Failed to parse response HTML", "error", err)
			os.Exit(1)
		}

		passed := 0
		failed := 0
		var checkResults []map[string]string

		doc.Find("tr").Each(func(i int, tr *goquery.Selection) {
			tds := tr.Find("td")
			if tds.Length() >= 2 {
				name := strings.TrimSpace(tds.Eq(0).Text())
				val := strings.TrimSpace(tds.Eq(1).Text())
				if name != "" && val != "" {
					classAttr, _ := tds.Eq(1).Attr("class")
					status := "[OK]"
					if strings.Contains(classAttr, "fail") || strings.Contains(strings.ToLower(val), "failed") {
						status = "[FAIL]"
						failed++
					} else if strings.Contains(classAttr, "pass") || strings.Contains(strings.ToLower(val), "passed") || strings.Contains(strings.ToLower(val), "present") {
						status = "[PASS]"
						passed++
					}
					checkResults = append(checkResults, map[string]string{
						"name":   name,
						"status": status,
						"result": val,
					})
				}
			}
		})

		if jsonOutput {
			outputJSON(map[string]interface{}{
				"target_url": targetURL,
				"total":      passed + failed,
				"passed":     passed,
				"failed":     failed,
				"checks":     checkResults,
			})
		} else {
			fmt.Println("\n==================================================")
			fmt.Println("STEALTH VERIFICATION RESULTS (bot.sannysoft.com)")
			fmt.Println("==================================================")
			for _, item := range checkResults {
				fmt.Printf("%-35s %-10s %s\n", item["name"], item["status"], item["result"])
			}
			fmt.Println("==================================================")
			fmt.Printf("Summary: %d check(s) evaluated. Pass: %d, Fail: %d\n", passed+failed, passed, failed)
			if failed == 0 {
				fmt.Println("STATUS: STEALTH VERIFICATION PASSED — NO RED FLAGS DETECTED!")
			} else {
				fmt.Printf("STATUS: ATTENTION — %d DETECTIONS FOUND\n", failed)
			}
			fmt.Println("==================================================")
		}

	case "test-fallback":
		if len(os.Args) < 3 {
			fmt.Println("Usage: onyx test-fallback <url> [--render] [--json]")
			os.Exit(1)
		}

		targetURL := os.Args[2]
		forceRender := false
		for _, arg := range os.Args[2:] {
			if arg == "--render" {
				forceRender = true
			} else if arg == "--json" || arg == "-j" {
				jsonOutput = true
			}
		}

		cfg, err := config.LoadConfig("config.yaml")
		if err != nil {
			slog.Warn("Failed to load config, continuing with env vars only", "error", err)
		}

		registry := buildRegistry(cfg)
		
		slog.Info("Testing fallback layer", "url", targetURL, "render", forceRender)
		browser.DefaultCircuitBreaker.RecordFailure(targetURL)
		browser.DefaultCircuitBreaker.RecordFailure(targetURL)

		res, err := registry.Fetch(ctx, targetURL, discovery.FetchOptions{ForceRender: forceRender})
		if err != nil {
			slog.Error("Test fallback fetch failed", "error", err)
			os.Exit(1)
		}

		if jsonOutput {
			outputJSON(map[string]interface{}{
				"url":          targetURL,
				"provider":     res.Provider,
				"html_bytes":   len(res.RawHTML),
				"clean_text":   res.CleanText,
			})
		} else {
			slog.Info("Test fallback success", "provider", res.Provider, "html_bytes", len(res.RawHTML))
			fmt.Println("\nClean Content Snippet:\n", res.CleanText[:min(500, len(res.CleanText))])
		}

	case "crawl":
		if len(os.Args) < 3 {
			fmt.Println("Usage: onyx crawl <url|domain> [--max-pages N] [--max-depth N] [--workers N] [--schema name] [--render] [--json]")
			os.Exit(1)
		}

		var startURL string
		maxPages := 50
		maxDepth := 3
		workersCount := 5
		schemaName := ""
		forceRender := false

		for i := 2; i < len(os.Args); i++ {
			arg := os.Args[i]
			if arg == "--render" {
				forceRender = true
			} else if arg == "--json" || arg == "-j" {
				jsonOutput = true
			} else if (arg == "--max-pages" || arg == "-m") && i+1 < len(os.Args) {
				if n, err := strconv.Atoi(os.Args[i+1]); err == nil && n > 0 {
					maxPages = n
				}
				i++
			} else if strings.HasPrefix(arg, "--max-pages=") {
				val := strings.TrimPrefix(arg, "--max-pages=")
				if n, err := strconv.Atoi(val); err == nil && n > 0 {
					maxPages = n
				}
			} else if (arg == "--max-depth" || arg == "-d") && i+1 < len(os.Args) {
				if n, err := strconv.Atoi(os.Args[i+1]); err == nil && n > 0 {
					maxDepth = n
				}
				i++
			} else if strings.HasPrefix(arg, "--max-depth=") {
				val := strings.TrimPrefix(arg, "--max-depth=")
				if n, err := strconv.Atoi(val); err == nil && n > 0 {
					maxDepth = n
				}
			} else if (arg == "--workers" || arg == "-w") && i+1 < len(os.Args) {
				if n, err := strconv.Atoi(os.Args[i+1]); err == nil && n > 0 {
					workersCount = n
				}
				i++
			} else if strings.HasPrefix(arg, "--workers=") {
				val := strings.TrimPrefix(arg, "--workers=")
				if n, err := strconv.Atoi(val); err == nil && n > 0 {
					workersCount = n
				}
			} else if arg == "--schema" && i+1 < len(os.Args) {
				schemaName = os.Args[i+1]
				i++
			} else if strings.HasPrefix(arg, "--schema=") {
				schemaName = strings.TrimPrefix(arg, "--schema=")
			} else if startURL == "" && !strings.HasPrefix(arg, "--") {
				startURL = arg
			}
		}

		if startURL == "" {
			fmt.Println("Usage: onyx crawl <url|domain> [--max-pages N] [--max-depth N] [--workers N] [--schema name] [--render] [--json]")
			os.Exit(1)
		}

		if !strings.HasPrefix(startURL, "http://") && !strings.HasPrefix(startURL, "https://") {
			startURL = "https://" + startURL
		}

		cfg, _ := config.LoadConfig("config.yaml")
		var client *llm.Client
		if cfg != nil {
			client = llm.NewClient(cfg.ActiveProviderConfig())
		}

		st, err := store.NewStore(defaultDBPath)
		if err != nil {
			slog.Warn("Failed to open SQLite database", "error", err)
		} else {
			defer st.Close()
		}

		crawler := crawl.NewCrawler()
		slog.Info("Starting site crawl", "url", startURL, "max_pages", maxPages, "max_depth", maxDepth, "workers", workersCount)

		res, err := crawler.Crawl(ctx, crawl.CrawlOptions{
			StartURL:  startURL,
			MaxPages:  maxPages,
			MaxDepth:  maxDepth,
			Workers:   workersCount,
			Render:    forceRender,
			Schema:    schemaName,
			Store:     st,
			LLMClient: client,
			OnPageCrawled: func(pageURL string, count int, err error) {
				if !jsonOutput {
					if err != nil {
						fmt.Printf("  [%d] [FAILED] %s (Error: %v)\n", count, pageURL, err)
					} else {
						fmt.Printf("  [%d] [CRAWLED] %s\n", count, pageURL)
					}
				}
			},
		})

		if err != nil {
			slog.Error("Crawl execution failed", "error", err)
			os.Exit(1)
		}

		if jsonOutput {
			outputJSON(res)
		} else {
			fmt.Printf("\n==================================================\n")
			fmt.Printf("CRAWL COMPLETE — %s\n", startURL)
			fmt.Printf("==================================================\n")
			fmt.Printf("Target Host:      %s\n", res.TargetHost)
			fmt.Printf("Workers Used:     %d\n", res.WorkersUsed)
			fmt.Printf("Total Discovered: %d\n", res.TotalDiscovered)
			fmt.Printf("Total Crawled:    %d\n", res.TotalCrawled)
			fmt.Printf("Total Saved DB:   %d\n", res.TotalSaved)
			fmt.Printf("Total Failed:     %d\n", res.TotalFailed)
			fmt.Printf("Duration:         %d ms\n", res.DurationMs)
			fmt.Printf("==================================================\n")
		}

	case "telegram":
		if len(os.Args) < 3 {
			fmt.Println("Usage: onyx telegram <start|status|set-webhook|delete-webhook> [--json]")
			os.Exit(1)
		}
		switch os.Args[2] {
		case "start":
			runTelegramStart(ctx, jsonOutput)
		case "status":
			runTelegramStatus(ctx, jsonOutput)
		case "set-webhook":
			runTelegramSetWebhook(ctx, jsonOutput)
		case "delete-webhook":
			runTelegramDeleteWebhook(ctx, jsonOutput)
		default:
			fmt.Printf("Unknown telegram subcommand: %s\n", os.Args[2])
			fmt.Println("Usage: onyx telegram <start|status|set-webhook|delete-webhook> [--json]")
			os.Exit(1)
		}

	case "telegram-auth":
		// Bootstrap helper: wait for the operator to send /start to the
		// bot, capture their chat_id, and write it into the
		// allowed_chat_ids list in config.yaml. No auth required (this
		// command *is* the way you get onto the allowlist).
		runTelegramAuthBootstrap(ctx, jsonOutput)

	default:
		fmt.Printf("Unknown command: %s\n", cmd)
		printUsage()
		os.Exit(1)
	}
}

func hasJSONFlag(args []string) bool {
	for _, arg := range args {
		if arg == "--json" || arg == "-j" {
			return true
		}
	}
	return false
}

func outputJSON(data interface{}) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(data)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func printUsage() {
	fmt.Println("Usage: onyx <command> [args...]")
	fmt.Println("\nCommands:")
	fmt.Println("  ping [--json]                                      Ping LLM API to check connection")
	fmt.Println("  fetch <url> [--render] [--json]                    Fetch and clean HTML content from URL")
	fmt.Println("  find <url> \"<desc>\" [--render] [--json]            Find CSS/XPath selector for target description")
	fmt.Println("  extract <url> [--schema <name|json>] [--render]   Extract structured JSON from page")
	fmt.Println("  crawl <url> [--max-pages N] [--workers N]          Discover & ingest full sites/sitemaps concurrently into SQLite")
	fmt.Println("  search \"<query>\" [--json]                          Full-text search saved pages in SQLite")
	fmt.Println("  agent \"<goal>\" [--json]                            Execute autonomous multi-step web task (hard cap: 40 steps)")
	fmt.Println("  news [--window <phrase>] [--field <name>] [--json] Run profile-driven news digest fetch & summarization")
	fmt.Println("  schedule [--config schedule.yaml] [--json]         Start background ticker scheduler daemon")
	fmt.Println("  serve [--port 9090]                                Start local HTTP API server (localhost:9090)")
	fmt.Println("  test-stealth [--json]                              Verify browser stealth signals on bot.sannysoft.com")
	fmt.Println("  test-fallback <url> [--render] [--json]            Verify circuit breaker & ScraperAPI fallback routing")
	fmt.Println("  telegram <start|status> [--json]                   Run the Telegram chat gateway (polling or webhook)")
	fmt.Println("  telegram-auth [--timeout 60s] [--json]             Bootstrap allowlist by capturing your chat_id")
}


func buildRegistry(cfg *config.Config) *discovery.Registry {
	searxngURL := os.Getenv("SEARXNG_URL")
	if searxngURL == "" {
		searxngURL = "http://localhost:8080"
	}
	var searchProviders []discovery.SearchProvider
	searchProviders = append(searchProviders, discovery.NewSearXNGProvider(search.NewSearXNGClient(searxngURL, &http.Client{})))
	
	fetchProviders := make(map[string]discovery.FetchProvider)
	fetchProviders["colly"] = discovery.NewCollyProvider()
	fetchProviders["rod"] = discovery.NewRodProvider(nil)
	
	apiKey := ""
	var jinaReranker *discovery.JinaReranker
	if cfg != nil {
		apiKey = cfg.GetScraperAPIKey()
		if cfg.TinyFish != nil && (cfg.TinyFish.Enabled == nil || *cfg.TinyFish.Enabled) && cfg.TinyFish.APIKey != "" {
			tfProvider := discovery.NewTinyFishProvider(cfg.TinyFish.APIKey)
			fetchProviders["tinyfish"] = tfProvider
			searchProviders = append(searchProviders, tfProvider)
		}
		if cfg.Jina != nil && (cfg.Jina.Enabled == nil || *cfg.Jina.Enabled) {
			jinaProv := discovery.NewJinaProvider(cfg.Jina.APIKey)
			fetchProviders["jina"] = jinaProv
			searchProviders = append(searchProviders, jinaProv)
			if cfg.Jina.RerankerEnabled == nil || *cfg.Jina.RerankerEnabled {
				jinaReranker = discovery.NewJinaReranker(cfg.Jina.APIKey, true)
			}
		}
	} else {
		apiKey = os.Getenv("SCRAPERAPI_KEY")
		jinaProv := discovery.NewJinaProvider("")
		fetchProviders["jina"] = jinaProv
		searchProviders = append(searchProviders, jinaProv)
		jinaReranker = discovery.NewJinaReranker("", true)
	}
	
	if apiKey != "" {
		fetchProviders["scraperapi"] = discovery.NewScraperAPIProvider(apiKey)
	}
	
	var fetchPriority []string
	if cfg != nil && cfg.Discovery != nil && len(cfg.Discovery.FetchPriority) > 0 {
		fetchPriority = cfg.Discovery.FetchPriority
	}

	return discovery.NewRegistry(searchProviders, fetchProviders, fetchPriority, jinaReranker)
}

// ----------------------------------------------------------------------------
// Telegram gateway CLI glue
// ----------------------------------------------------------------------------

// telegramCfgFromConfig projects config.TelegramConfig into the trimmed
// BotConfig the gateway needs. Pure projection — no IO.
func telegramCfgFromConfig(cfg *config.Config) *telegram.BotConfig {
	if cfg == nil || cfg.Telegram == nil {
		return &telegram.BotConfig{Mode: "polling"}
	}
	bc := &telegram.BotConfig{
		Mode:                  cfg.Telegram.Mode,
		AllowedChatIDs:        cfg.Telegram.AllowedChatIDs,
		AllowedUsernames:      cfg.Telegram.AllowedUsernames,
		DefaultMode:           cfg.Telegram.DefaultMode,
		MaxConcurrentSessions: cfg.Telegram.MaxConcurrentSessions,
		TypingIndicator:       cfg.Telegram.TypingIndicator != nil && *cfg.Telegram.TypingIndicator,
		WebhookPublicURL:      cfg.Telegram.Webhook.PublicURL,
		WebhookListenAddr:     cfg.Telegram.Webhook.ListenAddr,
		WebhookSecretToken:    cfg.Telegram.Webhook.SecretToken,
	}
	if bc.Mode == "" {
		bc.Mode = "polling"
	}
	return bc
}

// scheduleTelegramGate adapts *config.Config to the small
// scheduler.TelegramGate interface. It exists so the scheduler package
// does not have to import internal/config — LoadConfig only needs the
// two answers (enabled? chat allowed?), and we can answer both from
// any reasonable config source.
type scheduleTelegramGate struct {
	cfg *config.Config
}

func (g *scheduleTelegramGate) Enabled() bool {
	return g.cfg != nil && g.cfg.IsTelegramEnabled()
}

func (g *scheduleTelegramGate) IsAllowedChatID(chatID int64) bool {
	if g.cfg == nil || g.cfg.Telegram == nil {
		return false
	}
	for _, id := range g.cfg.Telegram.AllowedChatIDs {
		if id == chatID {
			return true
		}
	}
	return false
}


// buildTelegramRouter constructs the Phase-6/7/8 wired Router + SessionManager
// in one place, so the polling (`telegram start`) and webhook (`serve`
// mux) code paths share identical behaviour. st may be nil — the gateway
// tolerates that and falls back to "no persistence" (the chat still
// works, /status just can't read prior runs).
func buildTelegramRouter(ctx context.Context, bot *telegram.Bot, botCfg *telegram.BotConfig, cfg *config.Config, st *store.Store) (*telegram.Router, *telegram.SessionManager) {
	sm := telegram.NewSessionManager(bot.API, st, botCfg.MaxConcurrentSessions)

	// Build the engine-backed runners. We close over `ctx` and the
	// shared LLM client / registry so each Telegram run reuses the
	// same engine instances the CLI uses. This is the Phase-6
	// requirement: "reuses Onyx's existing engine" — the only new
	// surface is the session manager that owns cancel + persistence.
	agentRun := func(ctx context.Context, goal string, runID int64, cb agent.StepCallback) (*store.AgentRun, error) {
		client := llm.NewClient(cfg.ActiveProviderConfig())
		ag := agent.NewAgent(client, st,
			agent.WithRegistry(buildRegistry(cfg)),
		)
		return ag.Run(ctx, goal, runID, cb)
	}
	researchRun := func(ctx context.Context, goal string, runID int64) (*store.ResearchRun, error) {
		client := llm.NewClient(cfg.ActiveProviderConfig())
		registry := buildRegistry(cfg)
		orchestrator := research.NewOrchestrator(client, st, registry, cfg)
		opts := research.Options{
			MaxReflectionRounds: 5,
			ResumeRunID:         runID,
		}
		return orchestrator.Run(ctx, goal, opts)
	}

	router := telegram.NewRouter(bot, botCfg,
		telegram.WithBackends(&telegram.EngineBackends{
			Agent:    agentRun,
			Research: researchRun,
			Sessions: sm,
		}),
		telegram.WithSessionManager(sm),
		telegram.WithStore(st),
	)
	return router, sm
}


func runTelegramStart(ctx context.Context, jsonOutput bool) {
	cfg, err := config.LoadConfig("config.yaml")
	if err != nil {
		slog.Error("Failed to load config", "error", err)
		os.Exit(1)
	}
	if !cfg.IsTelegramEnabled() {
		slog.Error("Telegram gateway is disabled. Set telegram.enabled: true in config.yaml (and provide TELEGRAM_BOT_TOKEN).")
		os.Exit(1)
	}

	token := cfg.GetTelegramBotToken()
	if token == "" {
		slog.Error("TELEGRAM_BOT_TOKEN is not set and config has no bot_token.")
		os.Exit(1)
	}

	botCfg := telegramCfgFromConfig(cfg)
	bot, err := telegram.NewBot(ctx, token, botCfg)
	if err != nil {
		slog.Error("Failed to initialize Telegram bot", "error", err)
		os.Exit(1)
	}

	// Phase 6/7: open the SQLite store and build the wired router
	// (SessionManager + engine-backed handlers + persisted /status).
	// If the store open fails we still start the gateway — it just
	// runs without persistence and /status degrades gracefully.
	st, storeErr := store.NewStore(defaultDBPath)
	if storeErr != nil {
		slog.Warn("Telegram gateway: failed to open SQLite store; session persistence disabled", "error", storeErr)
		st = nil
	}
	if st != nil {
		defer st.Close()
	}

	auth := telegram.NewAuthenticator(botCfg, telegram.PolicySilentDrop, false)
	router, _ := buildTelegramRouter(ctx, bot, botCfg, cfg, st)

	// Phase 4/5 mutual-exclusivity guard. In polling mode we delete
	// any pre-existing webhook on Telegram's side; in webhook mode we
	// register our URL so Telegram starts POSTing to it. The actual
	// HTTP reception in webhook mode is owned by `onyx serve`, which
	// mounts the WebhookHandler at /telegram/webhook via
	// api.WithTelegramWebhook. This keeps the gateway on a single
	// shared port with the rest of the HTTP API.
	if strings.EqualFold(botCfg.Mode, "webhook") {
		if botCfg.WebhookPublicURL == "" {
			slog.Error("telegram.mode=webhook requires telegram.webhook.public_url in config")
			os.Exit(1)
		}
		if err := telegram.SetWebhook(ctx, bot.API, botCfg.WebhookPublicURL, botCfg.WebhookSecretToken, 40); err != nil {
			slog.Error("SetWebhook failed", "error", err)
			os.Exit(1)
		}
	} else {
		// Polling mode: proactively delete any stale webhook so
		// getUpdates does not 409.
		if _, err := telegram.ReconcileMode(ctx, bot.API, "polling"); err != nil {
			slog.Warn("ReconcileMode on polling start reported a non-fatal error", "error", err)
		}
	}

	mode := strings.ToLower(strings.TrimSpace(botCfg.Mode))
	if mode == "" {
		mode = "polling"
	}
	if jsonOutput {
		outputJSON(map[string]interface{}{
			"status":    "starting",
			"bot":       bot.Self.UserName,
			"mode":      mode,
			"allowlist": len(botCfg.AllowedChatIDs) + len(botCfg.AllowedUsernames),
		})
	}

	stopChan := make(chan os.Signal, 1)
	signal.Notify(stopChan, os.Interrupt, syscall.SIGTERM)
	pollerCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	if mode == "webhook" {
		// Webhook mode: no inbound loop here. The HTTP server lives
		// in `onyx serve`. We just wait for SIGINT and on shutdown
		// call DeleteWebhook so a follow-up `telegram start` in
		// polling mode does not 409.
		fmt.Println("Webhook mode: this process is registering the webhook and idling.")
		fmt.Println("Inbound POSTs are served by `onyx serve` at " + botCfg.WebhookPublicURL + ".")
		fmt.Println("Run `onyx serve` in another process to actually accept updates.")
		<-stopChan
		slog.Info("Shutdown signal received, deregistering webhook and exiting")
		if err := telegram.DeleteWebhook(pollerCtx, bot.API, false); err != nil {
			slog.Warn("DeleteWebhook on shutdown failed", "error", err)
		}
		cancel()
		return
	}

	poller := telegram.NewPoller(bot, auth, router.Handle)
	done := make(chan error, 1)
	go func() { done <- poller.Run(pollerCtx) }()

	select {
	case sig := <-stopChan:
		slog.Info("Shutdown signal received, stopping telegram gateway", "signal", sig.String())
		cancel()
	case err := <-done:
		if err != nil {
			slog.Error("Telegram gateway exited with error", "error", err)
			os.Exit(1)
		}
		return
	}

	select {
	case <-done:
		slog.Info("Telegram gateway stopped cleanly")
	case <-time.After(10 * time.Second):
		slog.Warn("Telegram gateway did not stop within 10s; exiting anyway")
	}
}

func runTelegramStatus(ctx context.Context, jsonOutput bool) {
	cfg, err := config.LoadConfig("config.yaml")
	if err != nil {
		slog.Error("Failed to load config", "error", err)
		os.Exit(1)
	}
	enabled := cfg.IsTelegramEnabled()
	mode := ""
	tokenSet := cfg.GetTelegramBotToken() != ""
	if cfg.Telegram != nil {
		mode = cfg.Telegram.Mode
		if mode == "" {
			mode = "polling"
		}
	}

	botIdentity := "unknown"
	var webhookInfo *telegram.WebhookInfo
	if tokenSet {
		if bot, err := telegram.NewBot(ctx, cfg.GetTelegramBotToken(), telegramCfgFromConfig(cfg)); err == nil {
			botIdentity = bot.Self.UserName
			if strings.EqualFold(mode, "webhook") {
				if info, err := telegram.GetWebhookInfo(ctx, bot.API); err == nil {
					webhookInfo = &info
				}
			}
		}
	}

	activeSessions := 0
	st, err := store.NewStore(defaultDBPath)
	if err == nil {
		defer st.Close()
		if count, err := st.GetActiveTelegramSessionCount(); err == nil {
			activeSessions = count
		}
	}

	if jsonOutput {
		out := map[string]interface{}{
			"enabled":             enabled,
			"mode":                mode,
			"token_configured":    tokenSet,
			"allowlist_chat_ids":  len(cfg.Telegram.AllowedChatIDs),
			"allowlist_usernames": len(cfg.Telegram.AllowedUsernames),
			"bot_identity":        botIdentity,
			"active_sessions":     activeSessions,
		}
		if webhookInfo != nil {
			out["webhook_info"] = webhookInfo
		}
		outputJSON(out)
		return
	}
	fmt.Println("Telegram gateway status")
	fmt.Printf("  enabled:            %v\n", enabled)
	fmt.Printf("  mode:               %s\n", mode)
	fmt.Printf("  token configured:   %v\n", tokenSet)
	fmt.Printf("  bot identity:       %s\n", botIdentity)
	fmt.Printf("  active sessions:    %d\n", activeSessions)
	if cfg.Telegram != nil {
		fmt.Printf("  allowlist chat_ids: %d\n", len(cfg.Telegram.AllowedChatIDs))
		fmt.Printf("  allowlist usernames:%d\n", len(cfg.Telegram.AllowedUsernames))
	}
	if webhookInfo != nil {
		fmt.Printf("  webhook pending:    %d\n", webhookInfo.PendingUpdateCount)
		if webhookInfo.LastErrorMessage != "" {
			fmt.Printf("  webhook error:      %s\n", webhookInfo.LastErrorMessage)
		}
	}
}

func runTelegramSetWebhook(ctx context.Context, jsonOutput bool) {
	cfg, err := config.LoadConfig("config.yaml")
	if err != nil {
		slog.Error("Failed to load config", "error", err)
		os.Exit(1)
	}
	token := cfg.GetTelegramBotToken()
	if token == "" {
		slog.Error("TELEGRAM_BOT_TOKEN is not set.")
		os.Exit(1)
	}
	if cfg.Telegram == nil || cfg.Telegram.Webhook.PublicURL == "" {
		slog.Error("telegram.webhook.public_url is missing from config.yaml.")
		os.Exit(1)
	}
	
	botCfg := telegramCfgFromConfig(cfg)
	bot, err := telegram.NewBot(ctx, token, botCfg)
	if err != nil {
		slog.Error("Failed to initialize Telegram bot", "error", err)
		os.Exit(1)
	}
	
	if err := telegram.SetWebhook(ctx, bot.API, botCfg.WebhookPublicURL, botCfg.WebhookSecretToken, 40); err != nil {
		slog.Error("Failed to set webhook", "error", err)
		os.Exit(1)
	}
	
	if jsonOutput {
		outputJSON(map[string]string{"status": "ok", "message": "webhook set"})
	} else {
		fmt.Println("Webhook set successfully.")
	}
}

func runTelegramDeleteWebhook(ctx context.Context, jsonOutput bool) {
	cfg, err := config.LoadConfig("config.yaml")
	if err != nil {
		slog.Error("Failed to load config", "error", err)
		os.Exit(1)
	}
	token := cfg.GetTelegramBotToken()
	if token == "" {
		slog.Error("TELEGRAM_BOT_TOKEN is not set.")
		os.Exit(1)
	}
	botCfg := telegramCfgFromConfig(cfg)
	bot, err := telegram.NewBot(ctx, token, botCfg)
	if err != nil {
		slog.Error("Failed to initialize Telegram bot", "error", err)
		os.Exit(1)
	}
	
	if err := telegram.DeleteWebhook(ctx, bot.API, false); err != nil {
		slog.Error("Failed to delete webhook", "error", err)
		os.Exit(1)
	}
	
	if jsonOutput {
		outputJSON(map[string]string{"status": "ok", "message": "webhook deleted"})
	} else {
		fmt.Println("Webhook deleted successfully.")
	}
}

func runTelegramAuthBootstrap(ctx context.Context, jsonOutput bool) {
	cfg, err := config.LoadConfig("config.yaml")
	if err != nil {
		slog.Error("Failed to load config", "error", err)
		os.Exit(1)
	}
	token := cfg.GetTelegramBotToken()
	if token == "" {
		slog.Error("TELEGRAM_BOT_TOKEN is not set; cannot run auth bootstrap.")
		os.Exit(1)
	}

	// Parse --timeout flag.
	timeout := 60 * time.Second
	for i := 2; i < len(os.Args); i++ {
		arg := os.Args[i]
		if arg == "--timeout" && i+1 < len(os.Args) {
			if d, err := time.ParseDuration(os.Args[i+1]); err == nil {
				timeout = d
			}
			i++
		} else if strings.HasPrefix(arg, "--timeout=") {
			if d, err := time.ParseDuration(strings.TrimPrefix(arg, "--timeout=")); err == nil {
				timeout = d
			}
		}
	}

	slog.Info("Telegram auth-bootstrap: send /start to your bot now", "timeout", timeout)
	result, err := telegram.AuthBootstrap(ctx, token, timeout)
	if err != nil {
		slog.Error("Auth bootstrap failed", "error", err)
		os.Exit(1)
	}

	// Add the captured chat_id to allowed_chat_ids (de-duplicated).
	if cfg.Telegram == nil {
		cfg.Telegram = &config.TelegramConfig{}
	}
	already := false
	for _, id := range cfg.Telegram.AllowedChatIDs {
		if id == result.ChatID {
			already = true
			break
		}
	}
	if !already {
		cfg.Telegram.AllowedChatIDs = append(cfg.Telegram.AllowedChatIDs, result.ChatID)
		// Auto-enable so the operator does not have to hand-edit YAML.
		t := true
		cfg.Telegram.Enabled = &t
		if err := config.SaveConfig("config.yaml", cfg); err != nil {
			slog.Error("Failed to write config.yaml", "error", err)
			os.Exit(1)
		}
	}

	if jsonOutput {
		outputJSON(map[string]interface{}{
			"status":           "captured",
			"chat_id":          result.ChatID,
			"username":         result.Username,
			"already_allow":    already,
			"config_persisted": !already,
		})
		return
	}
	fmt.Printf("Captured chat_id=%d (username=%q)\n", result.ChatID, result.Username)
	if already {
		fmt.Println("Chat ID was already on the allowlist — no changes written.")
	} else {
		fmt.Println("Wrote chat_id into config.yaml allowed_chat_ids and set enabled: true.")
	}
}

func runTestTeacher(ctx context.Context, jsonOutput bool) {
	topic := "Transformer Attention"
	for i := 2; i < len(os.Args); i++ {
		arg := os.Args[i]
		if arg == "--topic" && i+1 < len(os.Args) {
			topic = os.Args[i+1]
			i++
		} else if strings.HasPrefix(arg, "--topic=") {
			topic = strings.TrimPrefix(arg, "--topic=")
		} else if !strings.HasPrefix(arg, "-") && topic == "Transformer Attention" {
			topic = arg
		}
	}

	cfg, err := config.LoadConfig("config.yaml")
	if err != nil {
		slog.Error("Failed to load config", "error", err)
		os.Exit(1)
	}

	st, err := store.NewStore("onyx.db")
	if err != nil {
		slog.Error("Failed to initialize store", "error", err)
		os.Exit(1)
	}
	defer st.Close()

	llmClient := llm.NewClient(cfg.ActiveProviderConfig())
	registry := buildRegistry(cfg)
	teacherOrch := teacher.NewOrchestrator(llmClient, st, registry, cfg)

	slog.Info("Starting headless Teacher agent smoke test", "topic", topic)

	// Step 1: Create Run & Clarify
	run, err := teacherOrch.Store().CreateRun(topic)
	if err != nil {
		slog.Error("Failed to create teacher run", "error", err)
		os.Exit(1)
	}

	// Step 2: Clarification turn with start now override
	clarResult, err := teacherOrch.ClarificationTurn(ctx, run.ID, "__start_now__")
	if err != nil {
		slog.Error("Clarification turn error", "error", err)
		os.Exit(1)
	}
	slog.Info("Clarification finished", "status", clarResult.Status)

	// Step 3: Run pipeline
	slog.Info("Executing report generation pipeline...")
	teacherRun, err := teacherOrch.GenerateReport(ctx, run.ID)
	if err != nil {
		slog.Error("Pipeline generation failed", "error", err)
		os.Exit(1)
	}

	sections, _ := teacherOrch.Store().GetSectionsForRun(run.ID)
	slog.Info("Teacher pipeline smoke test completed successfully",
		"run_id", teacherRun.ID,
		"sections_count", len(sections),
		"report_bytes", len(teacherRun.ReportMD),
		"status", teacherRun.Status,
	)

	if jsonOutput {
		outputJSON(map[string]interface{}{
			"status":         "ok",
			"run_id":         teacherRun.ID,
			"topic":          topic,
			"sections_count": len(sections),
			"report_len":     len(teacherRun.ReportMD),
			"report_md":      teacherRun.ReportMD,
		})
	} else {
		fmt.Printf("\n=== Teacher Report Generated (%d bytes, %d sections) ===\n\n", len(teacherRun.ReportMD), len(sections))
		if len(teacherRun.ReportMD) > 600 {
			fmt.Println(teacherRun.ReportMD[:600] + "\n\n...[truncated]...")
		} else {
			fmt.Println(teacherRun.ReportMD)
		}
	}
}
