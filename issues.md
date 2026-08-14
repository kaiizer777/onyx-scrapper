# Comprehensive Audit & Root-Cause Remediation Guide: Teacher Agent Pipeline

This document provides an exhaustive, production-grade technical audit of the **Teacher Agent Pipeline** (Clarification Engine, Pedagogical Outline Planner, Per-Section Parallel Research Worker, Writer, Critic/Refinement Loop, Assembly/FTS Indexer, SSE Event Streaming, and Web UI Integration) in the Onyx Scrapper codebase.

---

## Architecture & Data Flow Overview

```
                          ┌────────────────────────┐
                          │   Learner Raw Goal     │
                          └───────────┬────────────┘
                                      │
                                      ▼
                      ┌─────────────────────────────────┐
                      │  Phase 2: Clarification Loop    │ ◄─── (POST /teacher/answer,
                      │   ("The Grill" Intake Engine)   │       GET  /teacher/report/{id})
                      └───────────────┬─────────────────┘
                                      │
                                      ▼
                      ┌─────────────────────────────────┐
                      │    Compiled Learning Brief      │ (GET/PATCH /teacher/brief/{id})
                      └───────────────┬─────────────────┘
                                      │ (POST /teacher/generate)
                                      ▼
                      ┌─────────────────────────────────┐
                      │   Phase 5: Outline Planner      │ (DAG Dependency Graph,
                      │     (DAG & Kahn's TopoSort)     │  Section 0 Insertion)
                      └───────────────┬─────────────────┘
                                      │
                                      ▼
                      ┌─────────────────────────────────┐
                      │  Phase 4: Section Research      │ (Parallel Worker Pool,
                      │   (Discovery, Fetch, Authority) │  Per-Run Quality Budget)
                      └───────────────┬─────────────────┘
                                      │
                                      ▼
                      ┌─────────────────────────────────┐
                      │   Phase 6: Section Writer       │ (Parallel Drafting,
                      │   (Factual Grounding Markdown)  │  Status: OutlineStatusDrafting)
                      └───────────────┬─────────────────┘
                                      │
                                      ▼
                      ┌─────────────────────────────────┐
                      │ Phase 7: Critic & Refinement    │ (Evaluator-Optimizer Loop,
                      │   (5-Dimension Rubric / Revise) │  Revision Count Bounds)
                      └───────────────┬─────────────────┘
                                      │
                                      ▼
                      ┌─────────────────────────────────┐
                      │ Phase 8: Assembly & Indexing    │ (ToC, Glossaries, Where Next,
                      │   (SQLite FTS5, Final Markdown) │  Status: RunStatusDone)
                      └───────────────┬─────────────────┘
                                      │
                                      ▼
                      ┌─────────────────────────────────┐
                      │ Real-time SSE Stream & Web UI   │ (Live progress dashboard,
                      │ (/teacher/stream/{id}, /ui)     │  Table of Contents ScrollSpy)
                      └─────────────────────────────────┘
```

---

## Master Table of Identified Issues

| Issue ID | Pipeline Component | Severity | Category | Summary | Primary Affected Files |
|---|---|---|---|---|---|
| **ISSUE-01** | `Clarification / WebUI` | **Critical** | State Loss / Mutation Bug | Web UI reload or history navigation triggers empty answer mutation, destroying past Q&A history and spawning phantom clarification rounds | `internal/webui/templates/index.html`, `internal/api/server.go`, `internal/teacher/orchestrator.go` |
| **ISSUE-02** | `WebUI / Clarification` | **Critical** | Frontend / Syntax Error | Clarification option chips fail on click due to undeclared `Set` reference and single-quote HTML attribute unescaping | `internal/webui/templates/index.html` |
| **ISSUE-03** | `Clarification / Prompts` | **High** | LLM Prompt / Validation | Teacher dumps multiple compound questions in a single turn due to lack of atomic question constraints and array response normalization | `internal/teacher/prompts.go`, `internal/teacher/orchestrator.go` |
| **ISSUE-04** | `WebUI / Streaming` | **High** | SSE / UI State Sync | Refreshing during generation leaves progress dashboard blank and stuck at 5% due to missing state pre-hydration | `internal/webui/templates/index.html`, `internal/teacher/stream.go` |
| **ISSUE-05** | `Research / Budget` | **Critical** | Concurrency / Starvation | `quality.Budget` is a global orchestrator singleton, causing permanent search/fetch starvation for all runs after 40 calls | `internal/teacher/orchestrator.go`, `internal/teacher/research.go` |
| **ISSUE-06** | `Streaming / SSE` | **Critical** | Panic / Crash | Fatal runtime panic (`send on closed channel`) during concurrent client disconnect on SSE event broadcast | `internal/teacher/stream.go` |
| **ISSUE-07** | `Assembly / FTS` | **High** | Data Integrity | Section regeneration duplicates all run entries in `teacher_fts` SQLite virtual table | `internal/teacher/assembler.go`, `internal/teacher/store.go` |
| **ISSUE-08** | `Critic / Store` | **High** | Logic / State Reset | Section regeneration fails to reset `revision_count`, causing critic to bypass refinement immediately | `internal/teacher/store.go`, `internal/teacher/critic.go` |
| **ISSUE-09** | `Writer / Markdown` | **Medium** | Text Formatting | `cleanMarkdownContent` fails to strip plain triple-backtick markdown code fences without language tags | `internal/teacher/writer.go` |
| **ISSUE-10** | `CLI / Storage` | **Low** | Workspace Hygiene | `onyx test-teacher` smoke test initializes DB in root directory (`onyx.db`) instead of `data/onyx.db` | `cmd/onyx/main.go` |

---

## Detailed Technical Audit & Root-Cause Remediation Specifications

---

### ISSUE-01: Web UI Reload & History Navigation Erasing Clarification State and Spawning Phantom Rounds
- **Component**: Clarification Engine / Web UI Integration
- **Severity**: Critical (Data Loss & Token Waste)
- **Files**:
  - `internal/webui/templates/index.html:2821-2832, 4117-4281`
  - `internal/api/server.go:1316-1355`
  - `internal/teacher/orchestrator.go:102-170, 266-288`
  - `internal/teacher/store.go:326-360`

#### Root Cause Analysis
1. In `internal/webui/templates/index.html`, when `loadRun('teacher', id)` is triggered (via page reload with URL hash `#teacher/{id}` or clicking an item in the history sidebar), the function clears the conversation thread (`thread.children.remove()`) and renders only the initial user bubble `raw_goal`.
2. When `reportData.status === 'clarifying'`, `loadRun` executes an asynchronous POST mutation request:
   ```javascript
   const ansRes = await fetch('/teacher/answer', {
     method: 'POST',
     headers: { 'Content-Type': 'application/json' },
     body: JSON.stringify({ run_id: id, answer: "" })
   });
   ```
3. On the backend, `handleTeacherAnswer` invokes `o.teacherOrchestrator.ClarificationTurn(ctx, req.RunID, req.Answer)`. Because `req.Answer` is empty (`""`), `ClarificationTurn` assumes the model needs to generate the *next* question. It replays previous rounds into the LLM prompt and executes `o.client.Chat`.
4. The LLM generates a brand new question, which `ClarificationTurn` persists into `teacher_clarifications` as a new round (`Round: len(rounds) + 1`).
5. Every single time the user refreshes the page or navigates between history items and returns to an in-progress clarification run:
   - An expensive LLM call is executed.
   - A new unanswered question is appended to SQLite.
   - The round counter increments (e.g. Round 1 -> Round 2 -> Round 3).
   - All previously asked questions and user answers are completely missing from the UI DOM.
   - After a few refreshes, `len(rounds) >= maxRounds` triggers and prematurely forces `finalize_brief` on empty/orphaned questions.
6. Furthermore, `handleTeacherReport` in `internal/api/server.go` omits the `clarifications` slice from its JSON response, making it impossible for the frontend to re-hydrate the historical Q&A conversation.

#### Steps to Confirm
1. Start a teacher run with goal `"Learn Rust Concurrency"`.
2. Answer Round 1. When Round 2 question appears, refresh the browser page (`F5`).
3. Check SQLite database: `SELECT id, round, question, answer FROM teacher_clarifications WHERE run_id = '<id>';`.
4. Observe that a 3rd round was created with an empty answer for round 2, and the UI only displays Round 3 without any previous history.

#### Remediation Specification
1. **In `internal/api/server.go` (`handleTeacherReport`)**:
   - Query the clarification history from the store: `clarifications, _ := s.teacherOrchestrator.Store().GetClarifications(runID)`.
   - Include `"clarifications": clarifications` in the JSON response payload.
2. **In `internal/teacher/orchestrator.go` (`ClarificationTurn`)**:
   - Add idempotency guard: If `cleanAnswer == ""` and `len(rounds) > 0`, check if the latest round is already unanswered (`rounds[lastIdx].Answer == ""`).
   - If the latest round is unanswered and `cleanAnswer == ""`, return the existing pending round immediately without calling the LLM:
     ```go
     if len(rounds) > 0 && cleanAnswer == "" {
         lastIdx := len(rounds) - 1
         if rounds[lastIdx].Answer == "" {
             return &ClarificationResult{
                 RunID:    runID,
                 Status:   RunStatusClarifying,
                 Round:    rounds[lastIdx].Round,
                 Question: &rounds[lastIdx].Question,
             }, nil
         }
     }
     ```
3. **In `internal/webui/templates/index.html` (`loadRun`)**:
   - Remove the `fetch('/teacher/answer', ... body: { answer: "" })` call entirely.
   - Reconstruct the full clarification conversation thread from `reportData.clarifications`:
     - Iterate through `reportData.clarifications`. For each completed round (`r.answer` is present), render the question block (read-only) followed by the user answer bubble (`appendUserBubble(r.answer)`).
     - If `reportData.status === 'clarifying'`, render the active interactive clarification card for the pending round.
     - If `reportData.status === 'brief_ready'`, render the `renderTeacherBrief(reportData.brief)`.
     - If `reportData.status === 'done'`, render `renderTeacherReport(reportData)`.

---

### ISSUE-02: Clarification Option Chips Failing on Click Due to Undeclared Variable and Quote Syntax Errors
- **Component**: Web UI Frontend Interaction
- **Severity**: Critical (User Interaction Blocked)
- **Files**:
  - `internal/webui/templates/index.html:2372-2380, 4145-4245`

#### Root Cause Analysis
1. **Undeclared Variable ReferenceError**:
   In `index.html`, `selectedTeacherMultiOptions` is used inside `renderTeacherClarification` (`selectedTeacherMultiOptions.clear()`), `toggleTeacherMultiChip`, and `submitTeacherMultiAnswer`. However, `let selectedTeacherMultiOptions = new Set();` is never declared at the top of the `<script>` tag. When `renderTeacherClarification` runs, calling `.clear()` on an undefined variable throws `Uncaught ReferenceError: selectedTeacherMultiOptions is not defined`, halting JavaScript execution.
2. **HTML Attribute Entity Decoding Breaking Inline JS**:
   In `renderTeacherClarification`:
   ```javascript
   const escapedOpt = escapeHtml(opt);
   if (isSingle) {
     optionsHtml += `<div class="teacher-chip" onclick="selectTeacherSingleChip('${escapedOpt}')">${escapedOpt}</div>`;
   }
   ```
   `escapeHtml` replaces single quotes with `&#039;`. When the browser parses the HTML attribute `onclick="..."`, the browser's HTML parser decodes `&#039;` back to `'` *before* the JavaScript engine evaluates the string.
   For example, if the option is `"I'm a beginner"`, the attribute becomes:
   `onclick="selectTeacherSingleChip('I'm a beginner')"`
   Clicking the chip triggers `Uncaught SyntaxError: Unexpected identifier 'm'`. The click event handler fails silently, and no selection or answer is submitted.

#### Steps to Confirm
1. In `index.html`, trigger a clarification question with options containing apostrophes or contractions, such as `["I'm a beginner", "I have some experience", "Don't know"]`.
2. Click on the `"I'm a beginner"` chip.
3. Open Browser DevTools Console: observe `Uncaught SyntaxError: Unexpected identifier 'm'`. The option is never submitted.

#### Remediation Specification
1. **Declare Global State**:
   At the top of the `<script>` tag in `index.html` (alongside `currentRunId`, `currentPoll`), explicitly declare:
   ```javascript
   let selectedTeacherMultiOptions = new Set();
   ```
2. **Eliminate String Interpolation in Event Handlers**:
   Replace string-concatenated `onclick="selectTeacherSingleChip('...')"` with DOM element creation, `dataset`, and event listeners:
   ```javascript
   const chip = document.createElement('div');
   chip.className = 'teacher-chip';
   chip.textContent = opt;
   chip.dataset.optionValue = opt;
   if (isSingle) {
     chip.addEventListener('click', () => selectTeacherSingleChip(opt));
   } else if (isMulti) {
     chip.innerHTML = `<span class="teacher-chip-checkbox">✓</span><span>${escapeHtml(opt)}</span>`;
     chip.addEventListener('click', function() { toggleTeacherMultiChip(this, opt); });
   }
   chipGroup.appendChild(chip);
   ```
   This guarantees that strings containing single quotes, double quotes, backslashes, HTML characters, or code snippets will never break JavaScript parsing.

---

### ISSUE-03: LLM Dumping Multiple Questions in a Single Clarification Turn
- **Component**: Clarification Prompting & Args Parsing
- **Severity**: High (Degraded UX & Alignment Drift)
- **Files**:
  - `internal/teacher/prompts.go:28-43, 49-92`
  - `internal/teacher/orchestrator.go:238-288`
  - `internal/teacher/types.go:90-95`

#### Root Cause Analysis
1. `ClarificationSystemPromptTemplate` states `"Ask ONE question per turn"`, but lacks explicit negative rules and structural validation against compound questions.
2. In practice, models (especially with open-ended topics) frequently output compound multi-part questions such as:
   `"1. What is your programming background? 2. Why are you learning this? 3. Do you prefer diagrams or code?"`
   within a single `"question"` string.
3. When compound questions are returned, the UI renders the block with generic single-select chips (e.g. `["Beginner", "Intermediate"]`), which only answer the first subquestion, leaving the rest unaddressed.
4. Additionally, if the model returns an array format (e.g. `"args": { "questions": [...] }`), `AskLearnerArgs` fails to bind the slice, leading to an empty question error and a retry loop where the model often concatenates all questions into one string.

#### Steps to Confirm
1. Send a broad prompt to the Teacher Agent: `"I want to learn programming"`.
2. Inspect the raw LLM response: observe instances where multiple numbered questions are bundled into `action.args.question`.

#### Remediation Specification
1. **Strengthen System Prompt Rules in `prompts.go`**:
   Add explicit negative constraints:
   ```
   - CRITICAL: Ask exactly ONE atomic, focused question per turn. Never bundle multiple questions, bullet points, or numbered lists into a single turn.
   - If multiple pieces of information are needed, choose the single most foundational question first.
   - Always ensure the options provided in 'options' directly and unambiguously answer the single question asked.
   ```
2. **Support Array Fallback in `AskLearnerArgs`**:
   Update `AskLearnerArgs` in `prompts.go`:
   ```go
   type AskLearnerArgs struct {
       Question  string   `json:"question"`
       Text      string   `json:"text,omitempty"`
       Questions []string `json:"questions,omitempty"`
       InputKind string   `json:"input_kind"`
       Options   []string `json:"options,omitempty"`
   }
   ```
   In `GetQuestion()`: If `Question` and `Text` are empty, but `len(Questions) > 0`, extract `Questions[0]`.
3. **Atomic Question Sanitization in `ClarificationTurn`**:
   In `internal/teacher/orchestrator.go`, before saving the new question, clean up numbered prefixes (e.g. `^1\.\s*`) and trim multi-paragraph dumps so only the primary atomic question is presented.

---

### ISSUE-04: Missing Progress State Hydration on Reload During Generation
- **Component**: SSE Event Streaming / Web UI Progress Dashboard
- **Severity**: High (UI Desynchronization)
- **Files**:
  - `internal/webui/templates/index.html:4441-4595`
  - `internal/teacher/stream.go:40-80`
  - `internal/api/server.go:1236-1314`

#### Root Cause Analysis
1. When report generation begins (`POST /teacher/generate`), the backend emits SSE events (`outline_ready`, `section_researching`, `section_drafted`, `section_critiquing`, `section_done`).
2. If the user refreshes the browser while generation is underway, `loadRun` calls `startTeacherStream(id)`.
3. The SSE endpoint (`/teacher/stream/{id}`) only broadcasts new, forward-looking events; it does not replay past events.
4. Because `outline_ready` was emitted prior to the reload, the new SSE listener never receives the section list.
5. In `index.html`, `sectionsState` remains empty (`totalSections = 0`), the section list DOM element (`#gen-sec-list-${runID}`) remains blank, and the progress bar is stuck at `5% Complete` until the final `done` event arrives minutes later.

#### Steps to Confirm
1. Start generation on a teacher run with 6 sections.
2. When section 2 is drafting, refresh the browser.
3. Observe that the progress dashboard appears with 0 sections listed, and no progress updates are visible until generation completes.

#### Remediation Specification
1. In `internal/webui/templates/index.html` (`loadRun` and `startTeacherStream`):
   - When loading an in-progress run, use `reportData.outline` and `reportData.sections` returned by `GET /teacher/report/${id}` to pre-populate the generation dashboard before establishing the SSE connection.
   - For each section in `reportData.outline`, initialize `sectionsState` with its existing status (`pending`, `drafted`, `done`) based on `reportData.sections`.
   - Calculate and display the initial progress percentage accurately based on existing sections.
   - Attach the SSE listener to stream remaining lifecycle events seamlessly.

---

### ISSUE-05: Quality Budget Singleton Causing Process-Wide Fetch Exhaustion
- **Component**: Research Worker / Quality Budget Governor
- **Severity**: Critical (Permanent Fetch Degradation)
- **Files**:
  - `internal/teacher/orchestrator.go:41-66`
  - `internal/teacher/research.go:59-66, 252-255`

#### Root Cause Analysis
1. In `teacher.NewOrchestratorWithStore`, `o.budget` is initialized once:
   `o.budget = quality.NewBudget(maxExtraCalls)` (default: 40).
2. The `Orchestrator` is a singleton attached to `api.Server` for the entire lifetime of the process.
3. Every section research worker calls `w.budget.TryAcquire()`. This increments a shared atomic counter `currentCalls`.
4. Once 40 candidate fetches occur across all runs, `TryAcquire()` returns `false` forever.
5. All subsequent teacher runs skip web fetching and fall back to synthetic internal findings.

#### Remediation Specification
1. Create a fresh `quality.Budget` instance **per run** inside `ResearchOutline`:
   ```go
   maxExtraCalls := 40
   if o.cfg != nil && o.cfg.Quality != nil && o.cfg.Quality.MaxExtraCallsPerRun > 0 {
       maxExtraCalls = o.cfg.Quality.MaxExtraCallsPerRun
   }
   runBudget := quality.NewBudget(maxExtraCalls)
   worker := &SectionResearchWorker{
       ...
       budget: runBudget,
   }
   ```
2. Remove the shared `budget` field from `Orchestrator` struct to prevent cross-run contamination.

---

### ISSUE-06: Fatal Runtime Panic on SSE Broadcast Send on Closed Channel
- **Component**: SSE Event Broadcaster
- **Severity**: Critical (Process Crash)
- **Files**:
  - `internal/teacher/stream.go:83-110`

#### Root Cause Analysis
1. In `EventBroadcaster.Broadcast`, the method acquires `b.mu.RLock()`, shallow-copies subscriber channels into a local slice `channels`, and calls `b.mu.RUnlock()`.
2. If a client disconnects or an HTTP handler exits before the broadcast send finishes, `unsubscribe()` acquires `b.mu.Lock()`, deletes the subscriber, and calls `close(ch)`.
3. When `Broadcast` iterates through `channels` and executes `ch <- event`, Go triggers `panic: send on closed channel`.

#### Remediation Specification
1. In `internal/teacher/stream.go`, protect the channel dispatch loop against panics, or keep the channel send synchronized:
   ```go
   func (b *EventBroadcaster) Broadcast(event StreamEvent) {
       if event.Timestamp == "" {
           event.Timestamp = time.Now().UTC().Format(time.RFC3339)
       }

       b.mu.RLock()
       defer b.mu.RUnlock()

       subsMap, exists := b.subscribers[event.RunID]
       if !exists || len(subsMap) == 0 {
           return
       }

       for subID, ch := range subsMap {
           if b.closedSubs[subID] {
               continue
           }
           select {
           case ch <- event:
           default:
           }
       }
   }
   ```
2. Ensure channel closing in `unsubscribe` only occurs while holding `b.mu.Lock()` with subscriber removal.

---

### ISSUE-07: Section Regeneration Duplicating Rows in `teacher_fts`
- **Component**: Assembler / SQLite FTS5 Indexing
- **Severity**: High (Data Corruption)
- **Files**:
  - `internal/teacher/assembler.go:140, 161, 181`
  - `internal/teacher/store.go:674-683`

#### Root Cause Analysis
1. `AssembleReport` calls `o.store.IndexReportFTS(runID, sec.Title, cleanContent)` for every section.
2. `IndexReportFTS` executes `INSERT INTO teacher_fts (run_id, section_title, content) VALUES (?, ?, ?)`.
3. Because SQLite FTS5 tables do not enforce unique constraints, regenerating a section re-indexes all sections, inserting duplicate rows.
4. Subsequent FTS search queries return duplicated snippets and distorted BM25 ranking scores.

#### Remediation Specification
1. In `internal/teacher/store.go`, add:
   ```go
   func (s *Store) ClearReportFTS(runID string) error {
       s.writeMu.Lock()
       defer s.writeMu.Unlock()
       query := `DELETE FROM teacher_fts WHERE run_id = ?;`
       _, err := s.db.Exec(query, runID)
       return err
   }
   ```
2. In `internal/teacher/assembler.go` (`AssembleReport`), invoke `_ = o.store.ClearReportFTS(runID)` once at the beginning of report assembly before re-indexing.

---

### ISSUE-08: Section Regeneration Bypassing Critic Loop Due to Stale `revision_count`
- **Component**: Store / Section Regeneration
- **Severity**: High (Quality Gate Failure)
- **Files**:
  - `internal/teacher/store.go:568-571`
  - `internal/teacher/critic.go:139-156`

#### Root Cause Analysis
1. When regenerating a section, `DraftSection` creates a new draft and calls `SaveSectionDraft`.
2. In `internal/teacher/store.go`:
   ```sql
   INSERT INTO teacher_sections (id, run_id, outline_id, draft_md, critique_notes, final_md, revision_count, created_at, updated_at)
   VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
   ON CONFLICT(id) DO UPDATE SET
       draft_md = excluded.draft_md,
       updated_at = excluded.updated_at;
   ```
3. `ON CONFLICT` leaves `revision_count` at its previous value (e.g. 2).
4. When `CritiqueAndRefineSection` runs, it checks `if sec.RevisionCount >= critiquePassLimit` (limit: 2). Because `sec.RevisionCount` is already 2, it immediately accepts the initial unrefined draft without performing any critique passes.

#### Remediation Specification
1. In `internal/teacher/store.go` (`SaveSectionDraft`), update the `ON CONFLICT` clause:
   ```sql
   ON CONFLICT(id) DO UPDATE SET
       draft_md = excluded.draft_md,
       critique_notes = NULL,
       final_md = '',
       revision_count = 0,
       updated_at = excluded.updated_at;
   ```

---

### ISSUE-09: Markdown Fence Stripping Bug in Writer Output
- **Component**: Writer / Markdown Sanitization
- **Severity**: Medium (Formatting Artifacts)
- **Files**:
  - `internal/teacher/writer.go:144-157`

#### Root Cause Analysis
1. `cleanMarkdownContent` checks `strings.HasPrefix(trimmed, "```markdown")` and `strings.HasPrefix(trimmed, "```md")`.
2. When the LLM encloses output in plain triple backticks (```\n# Header\n...```), the fences are not stripped, leaking raw markdown fences into final rendered sections.

#### Remediation Specification
1. Update `cleanMarkdownContent` in `internal/teacher/writer.go`:
   ```go
   func cleanMarkdownContent(s string) string {
       trimmed := strings.TrimSpace(s)
       for _, prefix := range []string{"```markdown", "```md", "```JSON", "```json", "```"} {
           if strings.HasPrefix(trimmed, prefix) && strings.HasSuffix(trimmed, "```") {
               trimmed = strings.TrimPrefix(trimmed, prefix)
               trimmed = strings.TrimSuffix(trimmed, "```")
               return strings.TrimSpace(trimmed)
           }
       }
       return trimmed
   }
   ```

---

### ISSUE-10: CLI `test-teacher` Database Path Inconsistency
- **Component**: CLI Smoke Testing
- **Severity**: Low (Hygiene)
- **Files**:
  - `cmd/onyx/main.go`

#### Root Cause Analysis
1. `runTestTeacher` in `cmd/onyx/main.go` calls `store.NewStore("onyx.db")` instead of `store.NewStore(defaultDBPath)` (`"data/onyx.db"`), creating a stray database file in the repository root.

#### Remediation Specification
1. Replace `"onyx.db"` with `defaultDBPath` in `cmd/onyx/main.go`.

---

## Master Remediation Prompt for the Next Working Agent

```markdown
You are assigned to implement root-cause fixes for the Teacher Agent Pipeline issues documented in `issues.md`.

### Execution Instructions & Discipline Rules
1. **Zero Guesswork / Mandatory Pre-Verification**:
   - Before modifying any file, inspect the lines referenced in `issues.md` to re-verify the active codebase state.
   - Confirm each root cause against Go types, SQL queries, and JavaScript event bindings.
2. **Minimal, Targeted Diffs**:
   - Do not perform unnecessary rewrites or introduce extra dependencies.
   - Maintain backward compatibility across all HTTP APIs and SQLite schemas.
3. **Execution Sequence (Dependencies First)**:
   - **Step 1 (Backend Core & Store)**:
     - Fix `SaveSectionDraft` SQL conflict clause to reset `revision_count = 0` in `internal/teacher/store.go` (ISSUE-08).
     - Add `ClearReportFTS` to `internal/teacher/store.go` and call it in `internal/teacher/assembler.go` (ISSUE-07).
     - Fix `EventBroadcaster.Broadcast` channel safety under mutex in `internal/teacher/stream.go` (ISSUE-06).
     - Scope `quality.Budget` per-run in `internal/teacher/research.go` (ISSUE-05).
     - Update `cleanMarkdownContent` in `internal/teacher/writer.go` (ISSUE-09).
   - **Step 2 (Clarification & API Engine)**:
     - Update `AskLearnerArgs` and `ClarificationSystemPromptTemplate` in `internal/teacher/prompts.go` (ISSUE-03).
     - Add idempotency guard to `ClarificationTurn` in `internal/teacher/orchestrator.go` (ISSUE-01).
     - Include `clarifications` in `handleTeacherReport` payload in `internal/api/server.go` (ISSUE-01).
   - **Step 3 (Web UI Frontend & State Hydration)**:
     - Declare `selectedTeacherMultiOptions = new Set();` in `internal/webui/templates/index.html` (ISSUE-02).
     - Refactor chip rendering in `renderTeacherClarification` to use DOM element listeners and `data-option-value` (ISSUE-02).
     - Refactor `loadRun` in `index.html` to hydrate past clarification rounds from `reportData.clarifications` and remove the empty `/teacher/answer` POST call (ISSUE-01).
     - Pre-populate generation dashboard in `startTeacherStream` using existing outline/sections (ISSUE-04).
   - **Step 4 (CLI Hygiene)**:
     - Fix DB path in `cmd/onyx/main.go` (ISSUE-10).
4. **Verification & Testing**:
   - Run `go test -v ./internal/teacher/... ./internal/api/...` to ensure all existing and new unit tests pass.
   - Validate that browser refresh on an active clarification turn restores past rounds and active question without spawning extra rounds.
   - Validate that single-select and multi-select chips with apostrophes submit properly on click.
```
