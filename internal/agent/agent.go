package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"github.com/go-rod/stealth"
	browserpkg "github.com/kaiizer777/onyx-scrapper/internal/browser"
	"github.com/kaiizer777/onyx-scrapper/internal/extract"
	"github.com/kaiizer777/onyx-scrapper/internal/llm"
	stealthpkg "github.com/kaiizer777/onyx-scrapper/internal/stealth"
	"github.com/kaiizer777/onyx-scrapper/internal/store"
	discoverypkg "github.com/kaiizer777/onyx-scrapper/internal/discovery"
	"github.com/kaiizer777/onyx-scrapper/internal/quality"
)

const (
	DefaultMaxSteps = 35
	MaxSnippetLen   = 3000
)

// Agent represents a ReAct agent runner.
type Agent struct {
	client        *llm.Client
	store         *store.Store
	registry      *discoverypkg.Registry
	maxSteps      int
	subQuestionID int64
}

// Option configures Agent parameters.
type Option func(*Agent)

// WithMaxSteps sets custom maximum step limit for agent execution.
func WithMaxSteps(steps int) Option {
	return func(a *Agent) {
		if steps > 0 {
			a.maxSteps = steps
		}
	}
}

// WithSubQuestionID links this agent execution to a research subquestion.
func WithSubQuestionID(id int64) Option {
	return func(a *Agent) {
		a.subQuestionID = id
	}
}

// WithRegistry sets custom registry for agent execution.
func WithRegistry(registry *discoverypkg.Registry) Option {
	return func(a *Agent) {
		a.registry = registry
	}
}

// NewAgent creates a new agent instance.
func NewAgent(client *llm.Client, st *store.Store, opts ...Option) *Agent {
	a := &Agent{
		client:   client,
		store:    st,
		maxSteps: DefaultMaxSteps,
	}
	for _, opt := range opts {
		opt(a)
	}
	// We no longer instantiate a default search service here; the caller must provide a Registry.
	return a
}

// ActionResponse defines the JSON structure expected from LLM.
type ActionResponse struct {
	Thought string `json:"thought"`
	Action  struct {
		Name string          `json:"name"`
		Args json.RawMessage `json:"args"`
	} `json:"action"`
}

// Action arguments definitions
type WebSearchArgs struct {
	Query string `json:"query"`
}

type NavigateArgs struct {
	URL string `json:"url"`
}

type FindElementArgs struct {
	Description string `json:"description"`
}

type ClickArgs struct {
	Selector    string `json:"selector,omitempty"`
	Description string `json:"description,omitempty"`
}

type TypeArgs struct {
	Selector    string `json:"selector,omitempty"`
	Description string `json:"description,omitempty"`
	Text        string `json:"text"`
	PressEnter  bool   `json:"press_enter,omitempty"`
}

type ExtractArgs struct {
	Schema json.RawMessage `json:"schema"`
}

type RecordFindingArgs struct {
	Claim      string  `json:"claim"`
	SourceURL  string  `json:"source_url"`
	Confidence float64 `json:"confidence"`
}

type DoneArgs struct {
	Result string `json:"result"`
}

// RunStepCallback allows real-time progress monitoring.
type StepCallback func(stepNum int, thought string, action string, args string, result string, err error)

func (a *Agent) Run(ctx context.Context, goal string, existingRunID int64, cb StepCallback) (*store.AgentRun, error) {
	// 1. Create run in SQLite if not provided
	runID := existingRunID
	var err error
	if runID == 0 {
		runID, err = a.store.CreateAgentRun(goal)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize agent run record: %w", err)
		}
	}

	// 2. Launch stealth browser session
	l := launcher.New().Headless(true).Leakless(false)
	controlURL, err := l.Launch()
	if err != nil {
		_ = a.store.UpdateAgentRunStatus(runID, "failed", err.Error())
		return nil, fmt.Errorf("failed to launch chromium for agent: %w", err)
	}

	browser := rod.New().ControlURL(controlURL)
	if err := browser.Connect(); err != nil {
		_ = a.store.UpdateAgentRunStatus(runID, "failed", err.Error())
		return nil, fmt.Errorf("failed to connect to browser: %w", err)
	}
	defer browser.MustClose()

	page, err := stealth.Page(browser)
	if err != nil {
		if page != nil {
			_ = page.Close()
		}
		page, err = browser.Page(proto.TargetCreateTarget{})
		if err != nil {
			_ = a.store.UpdateAgentRunStatus(runID, "failed", err.Error())
			return nil, fmt.Errorf("failed to create browser page: %w", err)
		}
	}
	defer page.Close()

	prof := stealthpkg.GetRandomProfile()
	browserpkg.ApplyProfile(page, prof)

	// 3. System prompt definition
	systemPrompt := `You are Onyx Scrapper, an autonomous browser agent.
Your goal is: "` + goal + `"

You must reason step-by-step and respond ONLY with a single JSON object matching this format:
{
  "thought": "Explanation of your plan and reasoning for this step",
  "action": {
    "name": "web_search|navigate|find_element|click|type|extract|done`
	if a.subQuestionID > 0 {
		systemPrompt += `|record_finding`
	}
	systemPrompt += `",
    "args": { ... }
  }
}

Available actions and arguments:
1. web_search: {"query": "search query"} - Searches the open web via SearXNG aggregator and returns top search result URLs and snippets.
2. navigate: {"url": "https://..."} - Navigates browser to target URL.
3. find_element: {"description": "plain english element description"} - Finds CSS selector for target element.
4. click: {"selector": "#id or .class", "description": "optional description if selector unknown"} - Clicks element.
5. type: {"selector": "#id or .class", "description": "optional description", "text": "text to type", "press_enter": true|false} - Inputs text into field.
6. extract: {"schema": "product|article|event|search-result-list or custom JSON schema"} - Extracts structured JSON from page.
7. done: {"result": "The FULL comprehensive final report. You MUST include all actual data, facts, and extracted JSON that fulfills the goal here. DO NOT just output a confirmation."} - Completes execution.`
	
	if a.subQuestionID > 0 {
		systemPrompt += `
8. record_finding: {"claim": "clear factual statement", "source_url": "URL where claim is found", "confidence": 0.0-1.0} - Immediately saves a finding to the database without stopping the agent.`
	}

	systemPrompt += `

Rules:
- Respond strictly with valid JSON. No markdown code blocks surrounding the JSON unless required, but prefer raw JSON string.
- Execute actions one step at a time.
- If an action fails, use alternative strategies.
- When calling 'done', the 'result' field MUST contain the actual complete data or report requested by the goal, never just a summary of your steps.`

	messages := []llm.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: fmt.Sprintf("Begin execution of goal: %q", goal)},
	}

	var finalResult string
	var finalStatus string = "running"

	for stepNum := 1; stepNum <= a.maxSteps; stepNum++ {
		stepCtx, stepCancel := context.WithTimeout(ctx, 5*time.Minute)
		respStr, err := a.client.Chat(stepCtx, messages)
		stepCancel()

		if err != nil {
			errStr := fmt.Errorf("LLM chat error at step %d: %w", stepNum, err).Error()
			status := "failed"
			if errors.Is(err, context.Canceled) {
				status = "cancelled"
				errStr = "Run cancelled by user"
			}
			_, _ = a.store.SaveAgentStep(runID, stepNum, "llm_call", "", "", errStr)
			_ = a.store.UpdateAgentRunStatus(runID, status, errStr)
			if cb != nil {
				cb(stepNum, "", "error", "", "", err)
			}
			return nil, fmt.Errorf("step %d LLM error: %w", stepNum, err)
		}

		// Parse JSON action response
		cleanResp := strings.TrimSpace(respStr)
		cleanResp = strings.TrimPrefix(cleanResp, "```json")
		cleanResp = strings.TrimPrefix(cleanResp, "```")
		cleanResp = strings.TrimSuffix(cleanResp, "```")
		cleanResp = strings.TrimSpace(cleanResp)

		var actionResp ActionResponse
		if err := json.Unmarshal([]byte(cleanResp), &actionResp); err != nil {
			// Retry prompt informing LLM of JSON syntax error
			errMsg := fmt.Sprintf("Failed to parse your response as JSON: %v. Raw output was: %s. Respond strictly with valid JSON.", err, cleanResp)
			messages = append(messages, llm.Message{Role: "assistant", Content: respStr})
			messages = append(messages, llm.Message{Role: "user", Content: errMsg})
			_, _ = a.store.SaveAgentStep(runID, stepNum, "invalid_json", string(cleanResp), "", err.Error())
			if cb != nil {
				cb(stepNum, "Parse error", "parse_error", cleanResp, "", err)
			}
			continue
		}

		actionName := strings.ToLower(strings.TrimSpace(actionResp.Action.Name))
		argsJSON := string(actionResp.Action.Args)

		var stepResult string
		var stepErr error

		switch actionName {
		case "web_search":
			var args WebSearchArgs
			if err := json.Unmarshal(actionResp.Action.Args, &args); err != nil {
				stepErr = fmt.Errorf("invalid web_search args: %w", err)
			} else {
				stepResult, stepErr = a.execWebSearch(ctx, args.Query)
			}

		case "navigate":
			var args NavigateArgs
			if err := json.Unmarshal(actionResp.Action.Args, &args); err != nil {
				stepErr = fmt.Errorf("invalid navigate args: %w", err)
			} else {
				stepResult, stepErr = a.execNavigate(ctx, page, args.URL)
			}

		case "find_element":
			var args FindElementArgs
			if err := json.Unmarshal(actionResp.Action.Args, &args); err != nil {
				stepErr = fmt.Errorf("invalid find_element args: %w", err)
			} else {
				stepResult, stepErr = a.execFindElement(ctx, page, args.Description)
			}

		case "click":
			var args ClickArgs
			if err := json.Unmarshal(actionResp.Action.Args, &args); err != nil {
				stepErr = fmt.Errorf("invalid click args: %w", err)
			} else {
				stepResult, stepErr = a.execClick(ctx, page, args.Selector, args.Description)
			}

		case "type":
			var args TypeArgs
			if err := json.Unmarshal(actionResp.Action.Args, &args); err != nil {
				stepErr = fmt.Errorf("invalid type args: %w", err)
			} else {
				stepResult, stepErr = a.execType(ctx, page, args.Selector, args.Description, args.Text, args.PressEnter)
			}

		case "extract":
			var args ExtractArgs
			if err := json.Unmarshal(actionResp.Action.Args, &args); err != nil {
				stepErr = fmt.Errorf("invalid extract args: %w", err)
			} else {
				schemaStr := strings.TrimSpace(string(args.Schema))
				var strVal string
				if err := json.Unmarshal(args.Schema, &strVal); err == nil {
					schemaStr = strVal
				}
				stepResult, stepErr = a.execExtract(ctx, page, schemaStr)
			}
			
		case "record_finding":
			var args RecordFindingArgs
			if err := json.Unmarshal(actionResp.Action.Args, &args); err != nil {
				stepErr = fmt.Errorf("invalid record_finding args: %w", err)
			} else if a.subQuestionID == 0 {
				stepErr = fmt.Errorf("record_finding is not available outside of deep research mode")
			} else {
				_, stepErr = a.store.SaveFinding(a.subQuestionID, args.Claim, args.SourceURL, "agent", args.Confidence)
				if stepErr == nil {
					stepResult = fmt.Sprintf("Successfully recorded finding: %q", args.Claim)
				}
			}

		case "done":
			var args DoneArgs
			_ = json.Unmarshal(actionResp.Action.Args, &args)
			finalResult = args.Result
			if finalResult == "" {
				finalResult = actionResp.Thought
			}
			stepResult = "Goal achieved: " + finalResult
			finalStatus = "completed"

		default:
			stepErr = fmt.Errorf("unknown action %q", actionName)
		}

		// Save step to SQLite
		errStr := ""
		if stepErr != nil {
			errStr = stepErr.Error()
		}
		_, _ = a.store.SaveAgentStep(runID, stepNum, actionName, argsJSON, stepResult, errStr)

		if cb != nil {
			cb(stepNum, actionResp.Thought, actionName, argsJSON, stepResult, stepErr)
		}

		if actionName == "done" || finalStatus == "completed" {
			_ = a.store.UpdateAgentRunStatus(runID, "completed", finalResult)
			return a.store.GetAgentRun(runID)
		}

		// Prepare next iteration state feedback for LLM
		messages = append(messages, llm.Message{Role: "assistant", Content: respStr})

		feedback := fmt.Sprintf("Step %d result for action %q:\n", stepNum, actionName)
		if stepErr != nil {
			feedback += fmt.Sprintf("ERROR: %v\n", stepErr)
		} else {
			feedback += fmt.Sprintf("OUTPUT: %s\n", stepResult)
		}
		messages = append(messages, llm.Message{Role: "user", Content: feedback})
	}

	// Max steps exceeded
	finalStatus = "max_steps_exceeded"
	finalResult = fmt.Sprintf("Agent reached max steps limit (%d) before completion.", a.maxSteps)
	_ = a.store.UpdateAgentRunStatus(runID, finalStatus, finalResult)

	return a.store.GetAgentRun(runID)
}

func (a *Agent) execNavigate(ctx context.Context, page *rod.Page, targetURL string) (string, error) {
	if !strings.HasPrefix(targetURL, "http://") && !strings.HasPrefix(targetURL, "https://") {
		targetURL = "https://" + targetURL
	}

	_ = stealthpkg.DefaultDomainRateLimiter.Wait(ctx, targetURL)
	_ = stealthpkg.HumanDelayCtx(ctx, 300, 800)

	if err := page.Navigate(targetURL); err != nil {
		return "", fmt.Errorf("navigate error: %w", err)
	}

	_ = page.WaitLoad()
	_ = page.WaitStable(1 * time.Second)
	_ = stealthpkg.HumanDelayCtx(ctx, 400, 900)

	info, _ := page.Info()
	title := info.Title

	rawHTML, err := page.HTML()
	if err != nil {
		return "", fmt.Errorf("failed to get page html: %w", err)
	}

	cleanText, cleanErr := extract.CleanHTML(rawHTML)
	if cleanErr != nil {
		cleanText = rawHTML
	}

	integrity := quality.AnalyzeFetchIntegrity(rawHTML, cleanText, "rod", nil)

	// Persist page in database
	if pageID, err := a.store.SavePage(targetURL, rawHTML, cleanText, "rod", string(integrity)); err != nil {
		slog.Warn("Failed to save page to store", "url", targetURL, "error", err)
	} else {
		slog.Debug("Saved page to store", "url", targetURL, "page_id", pageID)
	}

	if integrity != quality.FetchOK && integrity != quality.FetchFallbackRecovered && integrity != quality.FetchPartial {
		return fmt.Sprintf("[FETCH_INTEGRITY: %s — this source produced no usable content, do not treat as read, consider an alternate source or query reformulation]", integrity), nil
	}

	snippet := cleanText
	if len(snippet) > MaxSnippetLen {
		snippet = snippet[:MaxSnippetLen] + "... [truncated]"
	}

	return fmt.Sprintf("Successfully navigated to %s\nPage Title: %q\nContent Snippet:\n%s", targetURL, title, snippet), nil
}

func (a *Agent) execFindElement(ctx context.Context, page *rod.Page, description string) (string, error) {
	rawHTML, err := page.HTML()
	if err != nil {
		return "", fmt.Errorf("failed to get page html: %w", err)
	}

	selector, err := extract.FindElement(ctx, a.client, rawHTML, description)
	if err != nil {
		return "", fmt.Errorf("find element failed: %w", err)
	}

	return selector, nil
}

func (a *Agent) execClick(ctx context.Context, page *rod.Page, selector, description string) (string, error) {
	if selector == "" && description != "" {
		sel, err := a.execFindElement(ctx, page, description)
		if err != nil {
			return "", fmt.Errorf("could not resolve selector for description %q: %w", description, err)
		}
		selector = sel
	}

	if selector == "" {
		return "", fmt.Errorf("selector or description is required for click action")
	}

	var el *rod.Element
	var err error

	if strings.HasPrefix(selector, "//") || strings.HasPrefix(selector, "(") {
		el, err = page.ElementX(selector)
	} else {
		el, err = page.Element(selector)
	}

	if err != nil {
		return "", fmt.Errorf("element %q not found on page: %w", selector, err)
	}

	_ = stealthpkg.HumanDelayCtx(ctx, 300, 800)

	if err := el.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return "", fmt.Errorf("failed to click element %q: %w", selector, err)
	}

	_ = page.WaitLoad()
	_ = page.WaitStable(1 * time.Second)
	_ = stealthpkg.HumanDelayCtx(ctx, 300, 800)

	rawHTML, _ := page.HTML()
	cleanText, _ := extract.CleanHTML(rawHTML)
	snippet := cleanText
	if len(snippet) > MaxSnippetLen {
		snippet = snippet[:MaxSnippetLen] + "... [truncated]"
	}

	return fmt.Sprintf("Successfully clicked element %q. Updated page content snippet:\n%s", selector, snippet), nil
}

func (a *Agent) execType(ctx context.Context, page *rod.Page, selector, description, text string, pressEnter bool) (string, error) {
	if selector == "" && description != "" {
		sel, err := a.execFindElement(ctx, page, description)
		if err != nil {
			return "", fmt.Errorf("could not resolve selector for description %q: %w", description, err)
		}
		selector = sel
	}

	if selector == "" {
		return "", fmt.Errorf("selector or description is required for type action")
	}

	var el *rod.Element
	var err error

	if strings.HasPrefix(selector, "//") || strings.HasPrefix(selector, "(") {
		el, err = page.ElementX(selector)
	} else {
		el, err = page.Element(selector)
	}

	if err != nil {
		return "", fmt.Errorf("input element %q not found on page: %w", selector, err)
	}

	_ = stealthpkg.HumanDelayCtx(ctx, 300, 800)

	if err := el.SelectAllText(); err == nil {
		_ = el.Input("")
	}

	if err := el.Input(text); err != nil {
		return "", fmt.Errorf("failed to input text into %q: %w", selector, err)
	}

	if pressEnter {
		_ = stealthpkg.HumanDelayCtx(ctx, 200, 500)
		_ = page.Keyboard.Press(input.Enter)
		_ = page.WaitLoad()
		_ = page.WaitStable(1 * time.Second)
	}

	_ = stealthpkg.HumanDelayCtx(ctx, 300, 800)

	rawHTML, _ := page.HTML()
	cleanText, _ := extract.CleanHTML(rawHTML)
	snippet := cleanText
	if len(snippet) > MaxSnippetLen {
		snippet = snippet[:MaxSnippetLen] + "... [truncated]"
	}

	return fmt.Sprintf("Successfully typed %q into element %q (pressEnter=%v). Updated snippet:\n%s", text, selector, pressEnter, snippet), nil
}

func (a *Agent) execExtract(ctx context.Context, page *rod.Page, schema string) (string, error) {
	rawHTML, err := page.HTML()
	if err != nil {
		return "", fmt.Errorf("failed to get page html: %w", err)
	}

	rawJSON, err := extract.ExtractJSON(ctx, a.client, rawHTML, schema)
	if err != nil {
		return "", fmt.Errorf("extraction error: %w", err)
	}

	info, _ := page.Info()
	if pageObj, err := a.store.GetPageByURL(info.URL); err == nil && pageObj != nil {
		if _, saveErr := a.store.SaveExtraction(pageObj.ID, schema, string(rawJSON)); saveErr != nil {
			slog.Warn("Failed to save extraction for page", "page_id", pageObj.ID, "error", saveErr)
		}
	}

	return string(rawJSON), nil
}

func (a *Agent) execWebSearch(ctx context.Context, query string) (string, error) {
	if a.registry == nil {
		return "", fmt.Errorf("registry not configured")
	}

	results := a.registry.Search(ctx, query)

	if len(results) == 0 {
		return fmt.Sprintf("No web search results found for query %q.", query), nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Web Search Results for query %q:\n", query))
	for i, item := range results {
		sb.WriteString(fmt.Sprintf("%d. Title: %s\n   URL: %s\n   Snippet: %s\n   Provider: %s\n\n", i+1, item.Title, item.URL, item.Snippet, item.Provider))
		if i >= 7 {
			break
		}
	}

	return strings.TrimSpace(sb.String()), nil
}
