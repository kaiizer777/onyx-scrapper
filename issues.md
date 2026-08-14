# Deep Audit Report & Root-Cause Remediation Prompt

This document provides a comprehensive technical audit of the **Agentic Research (ReAct)**, **Deep Research (Multi-Branch Reflection)**, and **Teacher Agent (Clarification & Pedagogical Synthesis)** pipelines in Onyx Scrapper.

> **Instruction for the Next Agent**:
> 1. Read and re-verify each listed issue against the codebase to confirm root causes.
> 2. Implement the root-cause fixes systematically across the affected packages without introducing unnecessary abstractions or breaking backward compatibility.
> 3. Verify all fixes by adding/updating deterministic unit tests and running `go test ./...`.

---

## Table of Issues

| ID | Module | Severity | Issue Summary | Affected Files |
|---|---|---|---|---|
| **ISSUE-01** | `teacher` | **Critical (Reliability)** | Quality Budget is a global singleton, causing permanent fetch exhaustion for all runs after 40 calls | `internal/teacher/orchestrator.go`, `internal/teacher/research.go` |
| **ISSUE-02** | `teacher` | **Critical (Panic / Crash)** | Fatal runtime panic on SSE broadcast due to send on closed channel during concurrent disconnect | `internal/teacher/stream.go` |
| **ISSUE-03** | `research` | **High (Performance / Logic)** | Sub-research status overwrite to `"failed"` triggers runaway redundant re-execution in every reflection round | `internal/research/orchestrator.go`, `internal/research/worker.go`, `internal/report/render.go` |
| **ISSUE-04** | `agent` | **High (Panic / Crash)** | Unchecked `page.Info()` returns `nil` leading to fatal nil pointer dereferences | `internal/agent/agent.go` |
| **ISSUE-05** | `teacher` | **High (Data / State)** | `teacher_fts` virtual table duplicates all rows on section regeneration, polluting search rankings | `internal/teacher/assembler.go`, `internal/teacher/store.go` |
| **ISSUE-06** | `teacher` | **High (Logic / State)** | Section regeneration fails to reset `revision_count`, causing critic to skip revision passes immediately | `internal/teacher/store.go`, `internal/teacher/writer.go` |
| **ISSUE-07** | `teacher` / `webui` | **Medium (UX / Token Waste)** | Web UI reload on clarifying runs sends empty answers, creating ghost clarification rounds | `internal/webui/templates/index.html`, `internal/api/server.go`, `internal/teacher/orchestrator.go` |
| **ISSUE-08** | `llm` | **Medium (Reliability)** | HTTP 429 (Rate Limit / Too Many Requests) is marked non-retryable in LLM Client, causing sudden worker drops | `internal/llm/client.go` |
| **ISSUE-09** | `teacher` | **Low (Formatting)** | `cleanMarkdownContent` fails to strip plain triple-backtick markdown fences | `internal/teacher/writer.go` |
| **ISSUE-10** | `cmd/onyx` | **Low (Hygiene)** | CLI `test-teacher` smoke test initializes DB in root directory (`onyx.db`) instead of `data/onyx.db` | `cmd/onyx/main.go` |

---

## Detailed Issue Analysis & Remediation Specs

---

### ISSUE-01: Quality Budget Singleton Permanent Exhaustion
- **Category**: Concurrency / Resource Management
- **Severity**: Critical
- **Location**: `internal/teacher/orchestrator.go:41-66`, `internal/teacher/research.go:252-255`

#### Root Cause
In `teacher.NewOrchestratorWithStore`, a single `quality.Budget` instance is initialized once (`o.budget = quality.NewBudget(maxExtraCalls)`). Because the `Orchestrator` is a long-lived singleton on the HTTP server (`api.Server`), `o.budget` is shared across all teacher runs. As each run executes candidate fetches via `w.budget.TryAcquire()`, `budget.currentCalls` increments globally. Once 40 total fetches occur across all lifetime runs of the server, `TryAcquire()` permanently returns `false`. All subsequent runs skip web page fetching and fall back to internal synthetic dummy findings.

#### Steps to Confirm
1. Inspect `NewOrchestratorWithStore` in `internal/teacher/orchestrator.go`: notice `budget := quality.NewBudget(maxExtraCalls)` is stored in `o.budget`.
2. Inspect `ResearchOutline` in `internal/teacher/research.go`: notice `worker.budget = o.budget`.
3. Check `internal/api/server.go`: notice `s.teacherOrchestrator` is created once in `cmd/onyx/main.go` and reused for all incoming HTTP requests.

#### Root-Cause Fix Specification
- `quality.Budget` must be scoped **per run**, not globally across the orchestrator singleton.
- In `internal/teacher/research.go` (`ResearchOutline`): create a fresh `budget := quality.NewBudget(maxExtraCalls)` per `ResearchOutline` or `GenerateReport` call, or pass a new `Budget` to `SectionResearchWorker`.
- Ensure `deep-research` (`internal/research/orchestrator.go`) also allocates a per-run `Budget` when `orchestrator.Run()` is called rather than relying on `NewOrchestrator`.

---

### ISSUE-02: Fatal Runtime Panic on SSE Event Broadcast
- **Category**: Concurrency / Channel Safety
- **Severity**: Critical (Server Crash)
- **Location**: `internal/teacher/stream.go:88-108`

#### Root Cause
In `EventBroadcaster.Broadcast`, the method acquires a read lock `b.mu.RLock()`, copies active subscriber channels into a local slice `channels`, and releases `b.mu.RUnlock()`.
If a client disconnects or an `unsubscribe()` / `CloseRun()` call executes on another goroutine before the send loop executes, it acquires `b.mu.Lock()` and calls `close(ch)`.
When `Broadcast` then iterates over `channels` and executes `select { case ch <- event: }`, Go raises an unrecoverable runtime panic: `panic: send on closed channel`.

#### Steps to Confirm
1. Inspect `Broadcast` in `internal/teacher/stream.go`.
2. Notice `ch <- event` is executed on un-synchronized channels after releasing the read lock.
3. In Go runtime semantics, sending on a closed channel always panics.

#### Root-Cause Fix Specification
- Keep channel dispatch synchronized with subscriber registration/deregistration under mutex, or guard the send against closed channels using a subscriber struct with an active flag, or protect with a deferred `recover()` during channel send.
- A clean idiom: hold `b.mu.RLock()` during non-blocking send `select { case ch <- event: default: }`, and do not close `ch` inside `Broadcast` while maintaining a safe closing contract in `unsubscribe`/`CloseRun`. Ensure closing a subscriber channel only happens under write lock when removed from the map.

---

### ISSUE-03: Sub-Research Status Overwrite & Runaway Reflection Loops
- **Category**: Research Pipeline Logic / Resource Waste
- **Severity**: High
- **Location**: `internal/research/orchestrator.go:153-165, 236-248`, `internal/research/worker.go:71-77`, `internal/report/render.go:17-21`

#### Root Cause
1. In `worker.go`, if search/fetch yields no usable data, `RunSubResearch` updates the subquestion status in the store to `"insufficient_data"` and returns an error.
2. In `orchestrator.go` (`executeParallelResearch`), when `RunSubResearch` returns an error, it unconditionally executes `_ = o.store.UpdateSubQuestionStatus(q.ID, "failed")`, clobbering `"insufficient_data"`.
3. In `orchestrator.go` (`Run`), the reflection loop iterates up to `opts.MaxReflectionRounds` (default 5). On every round, it selects:
   ```go
   if sq.Status == "pending" || sq.Status == "failed" {
       pendingSqs = append(pendingSqs, sq)
   }
   ```
   Because failed sub-questions remain in `"failed"` status, the orchestrator **re-executes all previously failed sub-questions in every single reflection round**, creating up to 5 duplicate parallel search & fetch runs for queries that already failed.
4. Furthermore, because `"insufficient_data"` is overwritten to `"failed"`, `reportpkg.RenderReport` never detects `sq.Status == "insufficient_data"` and fails to render the `> [!WARNING] **Insufficient Data**` callout in the final report.

#### Steps to Confirm
1. Check `RunSubResearch` in `internal/research/worker.go:72`. Notice `_ = w.store.UpdateSubQuestionStatus(sqID, "insufficient_data")`.
2. Check `executeParallelResearch` in `internal/research/orchestrator.go:243`. Notice `_ = o.store.UpdateSubQuestionStatus(q.ID, "failed")` runs on all errors.
3. Check `Run` in `internal/research/orchestrator.go:156`. Notice `sq.Status == "pending" || sq.Status == "failed"` re-triggers all failed questions in every reflection round.

#### Root-Cause Fix Specification
- In `executeParallelResearch`: do NOT overwrite status to `"failed"` if the subquestion status was already set to `"insufficient_data"` (or preserve `"insufficient_data"` as a terminal non-retryable state).
- In `orchestrator.Run`: only pick up newly created subquestions with status `"pending"`. Failed / insufficient_data questions should not be blindly re-executed in subsequent reflection rounds unless specifically intended by a re-plan.
- Ensure `RenderReport` correctly renders the warning callout for questions with status `"insufficient_data"` or `"failed"`.

---

### ISSUE-04: Unchecked `page.Info()` Leading to Fatal Nil Dereference
- **Category**: ReAct Agent Runtime Safety
- **Severity**: High (Agent Crash)
- **Location**: `internal/agent/agent.go:418-419, 578-579`

#### Root Cause
In `execNavigate`:
```go
info, _ := page.Info()
title := info.Title
```
In `execExtract`:
```go
info, _ := page.Info()
if pageObj, err := a.store.GetPageByURL(info.URL); err == nil && pageObj != nil {
```
If Chromium crashed, page target closed, or navigation threw an error, `page.Info()` returns `(nil, err)`. Unconditionally dereferencing `info.Title` and `info.URL` causes an immediate panic in the agent goroutine.

#### Steps to Confirm
1. Check lines 418-419 and 578-579 in `internal/agent/agent.go`.
2. Notice the error return from `page.Info()` is discarded (`_`) and `info` is dereferenced without a `nil` check.

#### Root-Cause Fix Specification
- Safely check `if info != nil` before accessing `info.Title` and `info.URL`.
- In `execNavigate`: fallback to `title := targetURL` or `title := ""` if `info == nil`.
- In `execExtract`: fallback to page URL parameter or skip DB lookup if `info == nil`.

---

### ISSUE-05: FTS5 Index Row Duplication on Section Regeneration
- **Category**: Database / Search Integrity
- **Severity**: High
- **Location**: `internal/teacher/assembler.go:140, 161, 181`, `internal/teacher/store.go:674-683`

#### Root Cause
When `RegenerateSection` is called, it re-drafts, re-critiques, and calls `AssembleReport(ctx, runID)`.
In `AssembleReport`, it iterates through all outline sections and calls:
```go
_ = o.store.IndexReportFTS(runID, sec.Title, cleanContent)
_ = o.store.IndexReportFTS(runID, "Glossary", glossaryContent.String())
_ = o.store.IndexReportFTS(runID, "Where to Go Next", nextSteps.String())
```
`IndexReportFTS` performs `INSERT INTO teacher_fts (run_id, section_title, content) VALUES (?, ?, ?)`.
Because SQLite FTS5 virtual tables do not have primary keys or unique constraints on custom columns, each section regeneration duplicates all rows for `runID` in `teacher_fts`. If 5 sections are regenerated, there will be 6x duplicate rows per section in FTS, corrupting search snippets and ranking scores.

#### Steps to Confirm
1. Check `AssembleReport` in `internal/teacher/assembler.go`.
2. Check `IndexReportFTS` in `internal/teacher/store.go:674`. Notice there is no deletion of existing FTS entries for `runID` prior to insertion.

#### Root-Cause Fix Specification
- In `internal/teacher/store.go`: add `ClearReportFTS(runID string) error` that executes `DELETE FROM teacher_fts WHERE run_id = ?;`.
- In `internal/teacher/assembler.go`: call `o.store.ClearReportFTS(runID)` once at the start of report assembly before indexing sections.

---

### ISSUE-06: Stale `revision_count` on Section Regeneration Bypassing Critique Passes
- **Category**: Evaluator-Optimizer Feedback Loop / Data State
- **Severity**: High
- **Location**: `internal/teacher/store.go:568-571`, `internal/teacher/writer.go:57-76`, `internal/teacher/critic.go:139-144`

#### Root Cause
1. When regenerating a section, `DraftSection` creates a new draft and sets `sec.RevisionCount = 0`.
2. It calls `SaveSectionDraft(sec)`. The SQL statement in `SaveSectionDraft` is:
   ```sql
   INSERT INTO teacher_sections (id, run_id, outline_id, draft_md, critique_notes, final_md, revision_count, created_at, updated_at)
   VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
   ON CONFLICT(id) DO UPDATE SET
       draft_md = excluded.draft_md,
       updated_at = excluded.updated_at;
   ```
   Notice that on conflict (when regenerating an existing section), `revision_count`, `final_md`, and `critique_notes` are **NOT updated**.
3. When `CritiqueAndRefineSection` subsequently queries the database for the section via `GetSectionsForRun`, it reads the stale `revision_count` from the previous run (e.g. `revision_count = 2`).
4. On the very first critique evaluation, even if the critic issues a `"revise"` verdict with major flaws, the check:
   ```go
   if sec.RevisionCount >= critiquePassLimit { // 2 >= 2 is true
   ```
   immediately triggers and forces acceptance of the unrevised draft as final!

#### Steps to Confirm
1. Check `SaveSectionDraft` in `internal/teacher/store.go:568`. Notice `ON CONFLICT(id)` only updates `draft_md` and `updated_at`.
2. Check `CritiqueAndRefineSection` in `internal/teacher/critic.go:139`. Notice `sec.RevisionCount >= critiquePassLimit` aborts revision passes if `revision_count` is already at ceiling.

#### Root-Cause Fix Specification
- In `SaveSectionDraft` (`store.go`), update the `ON CONFLICT(id)` clause to reset `revision_count = excluded.revision_count`, `final_md = ''`, and `critique_notes = '[]'`.

---

### ISSUE-07: Web UI Refresh Spawning Phantom Clarification Rounds
- **Category**: API / Web UI UX
- **Severity**: Medium
- **Location**: `internal/webui/templates/index.html:2821-2832`, `internal/api/server.go:1060-1090`, `internal/teacher/orchestrator.go:158-168`

#### Root Cause
In `index.html` (`loadRun`), when viewing a past or active run with `status === 'clarifying'`, the UI calls:
```javascript
const ansRes = await fetch('/teacher/answer', {
  method: 'POST',
  headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify({ run_id: id, answer: "" })
});
```
Because `/teacher/answer` is a mutation endpoint that triggers `ClarificationTurn`, and `answer == ""`, `ClarificationTurn` assumes the model needs to generate the next question. It calls the LLM and creates a new `ClarificationRound` in SQLite, incrementing `round` from 1 to 2 to 3 every time the user refreshes or clicks the run in the history list.

#### Steps to Confirm
1. Check `loadRun` in `internal/webui/templates/index.html:2823`.
2. Start a teacher run and refresh the browser page. Notice `teacher_clarifications` receives new empty rows on every refresh.

#### Root-Cause Fix Specification
- In `internal/api/server.go`: update `handleTeacherReport` (or provide `GET /teacher/clarification/{run_id}`) to return the current clarification rounds and pending question.
- In `index.html` (`loadRun`): read the active question from the GET report/clarification response rather than posting an empty answer to `/teacher/answer`.

---

### ISSUE-08: HTTP 429 Rate Limits Treated as Non-Retryable
- **Category**: LLM Client / Resilience
- **Severity**: Medium
- **Location**: `internal/llm/client.go:179-183`

#### Root Cause
In `Client.Chat`, retry classification is implemented as:
```go
if resp.StatusCode >= 500 {
    retryable = true
} else {
    retryable = false // e.g., 4xx errors
}
```
HTTP 429 (`StatusTooManyRequests`) is a 4xx status code. When multiple parallel workers in Deep Research (concurrency: 3) or Teacher Agent (concurrency: 4) make concurrent LLM requests to rate-limited providers (e.g. OpenCode Zen, MiMo, OpenAI), a transient 429 causes immediate hard failure instead of backing off.

#### Steps to Confirm
1. Inspect `Client.Chat` in `internal/llm/client.go:179-183`.
2. Notice `retryable = false` for all status codes below 500, including 429.

#### Root-Cause Fix Specification
- Update retry check in `internal/llm/client.go`:
  ```go
  if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
      retryable = true
  }
  ```

---

### ISSUE-09: Incomplete Code Fence Stripping in `cleanMarkdownContent`
- **Category**: Markdown Rendering / Formatting
- **Severity**: Low
- **Location**: `internal/teacher/writer.go:144-157`

#### Root Cause
`cleanMarkdownContent` only checks `strings.HasPrefix(trimmed, "```markdown")` and `strings.HasPrefix(trimmed, "```md")`. If the model responds with generic markdown fences like ```` ```\n# Content\n``` ````, the outer fences are retained, causing the entire section to render inside a single monospace code block in the frontend.

#### Steps to Confirm
1. Inspect `cleanMarkdownContent` in `internal/teacher/writer.go:144`.
2. Notice plain ```` ``` ```` prefixes are not checked.

#### Root-Cause Fix Specification
- In `cleanMarkdownContent`: strip generic ```` ``` ```` prefixes as well if matching suffix exists, taking care not to damage internal code blocks.

---

### ISSUE-10: CLI `test-teacher` Database Path Inconsistency
- **Category**: CLI / Developer Experience
- **Severity**: Low
- **Location**: `cmd/onyx/main.go:1658`

#### Root Cause
`runTestTeacher` initializes `store.NewStore("onyx.db")` instead of `store.NewStore(defaultDBPath)` (`"data/onyx.db"`). Running `onyx test-teacher` leaves a rogue `onyx.db` file in the root workspace directory.

#### Steps to Confirm
1. Inspect `cmd/onyx/main.go:1658`.
2. Notice `"onyx.db"` literal instead of `defaultDBPath`.

#### Root-Cause Fix Specification
- Replace `"onyx.db"` with `defaultDBPath` in `runTestTeacher`.

---

## Instructions for the Implementing Agent

1. **Confirmation Phase**:
   - Verify each of the 10 issues against the source code.
   - Confirm you understand why each root cause creates the failure mode.
2. **Implementation Phase**:
   - Implement the specified root-cause fixes in order of severity (Critical -> High -> Medium -> Low).
   - Ensure clean, idiomatic Go and maintain backward compatibility.
3. **Verification Phase**:
   - Run `go test ./...` and ensure all existing and new unit tests pass without regressions.
