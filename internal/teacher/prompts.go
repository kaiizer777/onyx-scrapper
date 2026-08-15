package teacher

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/kaiizer777/onyx-scrapper/internal/timecontext"
)

// Tool names for the Clarification engine (Phase 2).
const (
	ToolAskLearner    = "ask_learner"
	ToolFinalizeBrief = "finalize_brief"
)

// TeacherActionResponse defines the JSON envelope expected from LLM action outputs,
// matching the ReAct / agent convention in internal/agent.
type TeacherActionResponse struct {
	Thought string `json:"thought"`
	Action  struct {
		Name string          `json:"name"`
		Args json.RawMessage `json:"args"`
	} `json:"action"`
}

// AskLearnerArgs defines arguments for the ask_learner tool.
type AskLearnerArgs struct {
	Question  string   `json:"question"`
	Text      string   `json:"text,omitempty"`
	Questions []string `json:"questions,omitempty"`
	InputKind string   `json:"input_kind"` // single_select | multi_select | free_text
	Options   []string `json:"options,omitempty"`
}

// SanitizeAtomicQuestion cleans up numbered prefixes and trims surrounding whitespace
// to ensure a single clean atomic question is presented.
func SanitizeAtomicQuestion(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	lower := strings.ToLower(trimmed)
	for _, p := range []string{"question 1:", "question 1.", "q1:", "1.", "1)", "-"} {
		if strings.HasPrefix(lower, p) {
			trimmed = strings.TrimSpace(trimmed[len(p):])
			break
		}
	}
	return trimmed
}

// GetQuestion returns the resolved and sanitized question string.
func (a *AskLearnerArgs) GetQuestion() string {
	if strings.TrimSpace(a.Question) != "" {
		return SanitizeAtomicQuestion(a.Question)
	}
	if strings.TrimSpace(a.Text) != "" {
		return SanitizeAtomicQuestion(a.Text)
	}
	if len(a.Questions) > 0 && strings.TrimSpace(a.Questions[0]) != "" {
		return SanitizeAtomicQuestion(a.Questions[0])
	}
	return ""
}

// FinalizeBriefArgs defines arguments for the finalize_brief tool.
type FinalizeBriefArgs struct {
	Brief LearningBrief `json:"brief"`
}

// ClarificationSystemPromptTemplate is the system prompt for the intake/clarification step (Appendix A).
const ClarificationSystemPromptTemplate = `You are the intake step for a personal learning-report generator. Your only job right now is to understand what the learner actually wants before any research happens — never assume.

You must reason step-by-step and respond ONLY with a single JSON object matching this format:
{
  "thought": "Your reasoning about what context is missing or if you have enough to finalize",
  "action": {
    "name": "ask_learner|finalize_brief",
    "args": { ... }
  }
}

Available actions and arguments:
1. ask_learner: {
  "question": "Clear, specific question for the learner",
  "input_kind": "single_select|multi_select|free_text",
  "options": ["Option 1", "Option 2"] // tappable options for single_select/multi_select, omit or empty for free_text
}
2. finalize_brief: {
  "brief": {
    "topic": "string (normalized topic statement)",
    "domain": "string (free text domain, e.g. 'distributed systems', 'organic chemistry', 'music theory')",
    "learner_level": "string (e.g. 'total beginner', 'has used it but doesn't understand internals', 'refreshing forgotten material')",
    "motivation": "string (why they are learning this: exam, project, curiosity, interview prep, ...)",
    "depth": "overview|working_understanding|deep_dive",
    "known_reference_points": ["string (concepts learner already understands to anchor analogies)"],
    "explicit_scope_in": ["string (subtopics to include)"],
    "explicit_scope_out": ["string (subtopics to exclude)"],
    "format_preferences": {
      "length": "short|medium|long",
      "wants_code_examples": true|false|null,
      "wants_diagrams": true|false
    },
    "assumptions_to_avoid": ["string (things the agent must NOT assume)"]
  }
}

Rules:
- Respond strictly with valid JSON. Do not output markdown fences if possible, or use standard raw JSON.
- CRITICAL: Ask exactly ONE atomic, focused question per turn. Never bundle multiple questions, bullet points, or numbered lists into a single turn.
- If multiple pieces of information are needed, choose the single most foundational question first.
- Prefer tappable options (input_kind=single_select/multi_select) over free text whenever the space of sensible answers is small; use free_text for anything open-ended (the topic itself, specific things they already know, specific things to avoid assuming).
- Always ensure the options provided in 'options' directly and unambiguously answer the single question asked.
- Do not assume the subject is technical. Do not assume a skill level. Do not assume why they're learning this. If it's not stated or clearly implied, ask.
- Stop asking once you could write a genuinely useful, non-generic learning brief — don't pad with low-value questions. You must ask at least {{min_rounds}} questions unless the learner has explicitly asked to start now.
- On your last available round ({{max_rounds}}), you MUST call finalize_brief with your best effort, filling any unknown fields with clearly-labeled reasonable defaults (default depth: "{{default_depth}}").`

// BuildClarificationPrompt renders the clarification system prompt with config bounds.
func BuildClarificationPrompt(minRounds, maxRounds int, defaultDepth string) string {
	if minRounds <= 0 {
		minRounds = 2
	}
	if maxRounds <= 0 {
		maxRounds = 10
	}
	if defaultDepth == "" {
		defaultDepth = "solid working understanding"
	}

	prompt := ClarificationSystemPromptTemplate
	prompt = strings.ReplaceAll(prompt, "{{min_rounds}}", strconv.Itoa(minRounds))
	prompt = strings.ReplaceAll(prompt, "{{max_rounds}}", strconv.Itoa(maxRounds))
	prompt = strings.ReplaceAll(prompt, "{{default_depth}}", defaultDepth)
	return prompt
}

// OutlinePlannerSection represents a single section in the LLM outline planner output.
type OutlinePlannerSection struct {
	ID                string   `json:"id"`
	Title             string   `json:"title"`
	LearningObjective string   `json:"learning_objective"`
	DependsOn         []string `json:"depends_on,omitempty"`
}

// OutlinePlannerResponse represents the structured output of the Outline Planner (Phase 5).
type OutlinePlannerResponse struct {
	Thought  string                  `json:"thought,omitempty"`
	Sections []OutlinePlannerSection `json:"sections"`
}

// Section 0 title constant required by §Phase 5.
const SectionZeroTitle = "Why this matters / core intuition in one paragraph"

// OutlinePlannerSystemPrompt is the prompt instructing the model to plan a pedagogical outline.
const OutlinePlannerSystemPrompt = `You are a master curriculum designer and educational architect. Your job is to take a compiled Learning Brief and turn it into a structured, pedagogically sound outline of 4 to 10 sections.

Rules:
1. Always include Section 0 as the very first section with:
   - "id": "sec_0"
   - "title": "Why this matters / core intuition in one paragraph"
   - "learning_objective": "Grasp the high-level intuition, real-world significance, and practical mental model before diving into details."
   - "depends_on": []
2. Generate between 4 and 10 sections total (including Section 0).
3. For each section, provide:
   - "id": A short unique identifier (e.g. "sec_0", "sec_1", "sec_2", ...)
   - "title": Clear, descriptive section title.
   - "learning_objective": Exactly one concise sentence stating what the learner will be able to do, explain, or understand after reading this section.
   - "depends_on": List of prerequisite section IDs that must be learned before this section.
4. Sequence sections logically from foundational concepts to advanced mechanics, respecting the learner's existing knowledge and targets.
5. Adhere strictly to the brief's explicit scope (include explicit_scope_in, exclude explicit_scope_out, avoid assumptions_to_avoid).
6. Output strictly valid JSON matching this schema:
{
  "thought": "Reasoning about pedagogical pacing and prerequisites...",
  "sections": [
    {
      "id": "sec_0",
      "title": "Why this matters / core intuition in one paragraph",
      "learning_objective": "Grasp the high-level intuition, real-world significance, and practical mental model before diving into details.",
      "depends_on": []
    },
    ...
  ]
}`

// BuildOutlinePlannerPrompt builds the user message for outline generation.
func BuildOutlinePlannerPrompt(brief *LearningBrief) (systemPrompt, userPrompt string) {
	systemPrompt = OutlinePlannerSystemPrompt

	briefJSON, _ := json.MarshalIndent(brief, "", "  ")
	userPrompt = fmt.Sprintf(`Please design a comprehensive teaching outline for the following Learning Brief:

%s

Ensure Section 0 ("Why this matters / core intuition in one paragraph") is included first, and all subsequent sections specify accurate prerequisites in "depends_on". Return ONLY valid JSON.`, string(briefJSON))

	return systemPrompt, userPrompt
}

// SectionQueryGenResponse represents search queries generated for a section.
type SectionQueryGenResponse struct {
	Queries []string `json:"queries"`
}

// BuildSectionQueryPrompt builds the prompt for generating targeted search queries for an outline section.
func BuildSectionQueryPrompt(brief *LearningBrief, section *TeacherOutlineSection) (systemPrompt, userPrompt string) {
	currentDateStr := timecontext.Now().Format("January 2, 2006")

	systemPrompt = `You are an expert research query planner. Your goal is to generate 2 to 5 highly specific, effective web search queries to research factual details, authoritative explanations, and common pitfalls for an educational report section.

Rules:
- Generate 2 to 5 search queries.
- Start with a solid foundational query, followed by targeted mechanical queries and authoritative documentation/reference queries.
- Incorporate the overall topic, domain, and specific section learning objective.
- Return ONLY valid JSON matching:
{
  "queries": ["query 1", "query 2", "query 3"]
}`

	userPrompt = fmt.Sprintf(`Topic: %s
Domain: %s
Learner Level: %s
Known Reference Points: %s
Current Date: %s

Section Title: "%s"
Section Objective: "%s"

Generate 2 to 5 search queries to find grounded, authoritative information for this section. Output strictly valid JSON.`,
		brief.Topic,
		brief.Domain,
		brief.LearnerLevel,
		strings.Join(brief.KnownReferencePoints, ", "),
		currentDateStr,
		section.Title,
		section.LearningObjective,
	)

	return systemPrompt, userPrompt
}

// ExtractedClaimItem represents a single factual claim extracted from research text.
type ExtractedClaimItem struct {
	Claim      string  `json:"claim"`
	SourceURL  string  `json:"source_url"`
	Confidence float64 `json:"confidence"`
}

// SectionClaimExtractionResponse represents the JSON response for factual claim extraction.
type SectionClaimExtractionResponse struct {
	Claims []ExtractedClaimItem `json:"claims"`
}

// BuildSectionClaimExtractionPrompt builds the extraction prompt for extracting claims from fetched page text.
func BuildSectionClaimExtractionPrompt(brief *LearningBrief, section *TeacherOutlineSection, sourceURL, chunkText string) (systemPrompt, userPrompt string) {
	currentDateStr := timecontext.Now().Format("January 2, 2006")

	systemPrompt = `You are a factual claim extraction assistant. Your job is to extract high-quality, grounded factual claims from source text that directly support teaching a specific outline section.

Rules:
- Extract clear, standalone factual assertions.
- Do not fabricate or extrapolate beyond what the source text explicitly states.
- For each claim, set "source_url" to the provided source URL and "confidence" to a float between 0.0 and 1.0 (typically 0.85 to 1.0 for authoritative statements).
- Output strictly valid JSON matching:
{
  "claims": [
    {
      "claim": "Factual assertion text",
      "source_url": "URL",
      "confidence": 0.95
    }
  ]
}`

	userPrompt = fmt.Sprintf(`Topic: %s
Domain: %s
Section Title: "%s"
Learning Objective: "%s"
Source URL: %s
Current Date: %s

Source Text:
%s

Extract grounded factual claims from this text. Respond strictly with valid JSON.`,
		brief.Topic,
		brief.Domain,
		section.Title,
		section.LearningObjective,
		sourceURL,
		currentDateStr,
		chunkText,
	)

	return systemPrompt, userPrompt
}

// SectionWriterPromptTemplate is the prompt for parallel section writing workers (Appendix B).
const SectionWriterPromptTemplate = `You are writing ONE section of a personal learning report. Write to be READ, not chatted with — no questions back to the learner, no "let me know if...".

Learner context: {{level}}, learning this because {{motivation}}, already comfortable with: {{known_reference_points}}. Depth target: {{depth}}. Length target: {{length}}.

Section: "{{title}}" — by the end, the learner should be able to: {{learning_objective}}.

Grounded findings (facts you may draw on, do not invent facts beyond these + well-established background knowledge):
{{findings}}

Structure:
1. Plain-language core idea first before any formalism.
2. One dominant analogy (prefer one tied to {{known_reference_points}} if it fits naturally, don't force it).
3. One concrete worked example appropriate to the domain (code snippet for CS, worked problem for math/science, annotated excerpt for humanities, etc.).
4. 1-2 common misconceptions and why they're wrong.
5. 2-3 sentence recap tied back to the section's learning objective.
6. If a diagram would clarify a process/relationship and diagrams are wanted, include one ` + "```mermaid```" + ` block.
7. Optional glossary annotations for key technical terms: <!--glossary: term=definition-->`

// BuildSectionWriterPrompt constructs the system and user prompts for drafting a section.
func BuildSectionWriterPrompt(brief *LearningBrief, section *TeacherOutlineSection, findings []TeacherFinding) (systemPrompt, userPrompt string) {
	systemPrompt = `You are an expert pedagogical author writing ONE section of a personal learning report. Write to be READ, not chatted with — no conversational filler, no questions back to the learner, no "let me know if...".

Pedagogical Structure Requirements:
1. Plain-language core idea first before any mathematical or technical formalism.
2. Exactly one dominant analogy anchored to the learner's known reference points when appropriate (do not force if none match).
3. One concrete domain-appropriate worked example (code snippet for CS/programming, worked problem for math/science, annotated excerpt for humanities).
4. 1–2 common misconceptions and why they are wrong.
5. 2–3 sentence recap tied to the section's learning objective.
6. If diagrams are requested, include one fenced ` + "```mermaid```" + ` diagram illustrating the core process, architecture, or relationship.
7. For key technical terms introduced, embed glossary annotations in the format: <!--glossary: term=definition-->

Output strictly the section body in clean, beautifully formatted Markdown.`

	var findingsText strings.Builder
	if len(findings) > 0 {
		for i, f := range findings {
			tier := f.AuthorityTier
			if tier == "" {
				tier = "Established"
			}
			src := f.SourceURL
			if src == "" {
				src = f.SourceProvider
			}
			findingsText.WriteString(fmt.Sprintf("%d. %s (Source: %s, Tier: %s)\n", i+1, f.Claim, src, tier))
		}
	} else {
		findingsText.WriteString("Draw upon authoritative principles and standard domain reference knowledge.\n")
	}

	wantsDiagrams := "false"
	if brief.FormatPreferences.WantsDiagrams {
		wantsDiagrams = "true (include one ```mermaid``` block illustrating the concept)"
	}

	wantsCode := "domain-appropriate"
	if brief.FormatPreferences.WantsCodeExamples != nil {
		if *brief.FormatPreferences.WantsCodeExamples {
			wantsCode = "true (include illustrative code snippets)"
		} else {
			wantsCode = "false (avoid code snippets, use conceptual/textual worked examples)"
		}
	}

	knownPoints := "none specified"
	if len(brief.KnownReferencePoints) > 0 {
		knownPoints = strings.Join(brief.KnownReferencePoints, ", ")
	}

	scopeIn := "none specified"
	if len(brief.ExplicitScopeIn) > 0 {
		scopeIn = strings.Join(brief.ExplicitScopeIn, "; ")
	}

	scopeOut := "none specified"
	if len(brief.ExplicitScopeOut) > 0 {
		scopeOut = strings.Join(brief.ExplicitScopeOut, "; ")
	}

	assumptionsAvoid := "none specified"
	if len(brief.AssumptionsToAvoid) > 0 {
		assumptionsAvoid = strings.Join(brief.AssumptionsToAvoid, "; ")
	}

	userPrompt = fmt.Sprintf(`Topic: %s
Domain: %s
Learner Level: %s
Motivation: %s
Target Depth: %s
Target Length: %s
Known Reference Points: %s
Wants Diagrams: %s
Wants Code Examples: %s
Explicit Scope In: %s
Explicit Scope Out: %s
Assumptions to Avoid: %s

Section Title: "%s"
Learning Objective: "%s"

Grounded Findings:
%s

Write the complete section adhering to the 7 pedagogical requirements. Respond ONLY with the Markdown content.`,
		brief.Topic,
		brief.Domain,
		brief.LearnerLevel,
		brief.Motivation,
		brief.Depth,
		brief.FormatPreferences.Length,
		knownPoints,
		wantsDiagrams,
		wantsCode,
		scopeIn,
		scopeOut,
		assumptionsAvoid,
		section.Title,
		section.LearningObjective,
		findingsText.String(),
	)

	return systemPrompt, userPrompt
}

// CritiqueEvaluationResponse represents the structured evaluation result from the Critic LLM.
type CritiqueEvaluationResponse struct {
	Issues  []CritiqueNote `json:"issues"`
	Verdict string         `json:"verdict"` // "pass" | "revise"
}

// BuildCriticPrompt constructs the system and user prompts for evaluating a section draft against the 5 rubric dimensions.
func BuildCriticPrompt(brief *LearningBrief, section *TeacherOutlineSection, draftMD string, findings []TeacherFinding) (systemPrompt, userPrompt string) {
	systemPrompt = `You are an independent educational reviewer and technical critic. Score this section draft against the rubric below. Be skeptical of confident-sounding but ungrounded claims.

Rubric Dimensions:
1. factual_grounding: Verifies non-obvious assertions trace back to section findings. Flag any hallucinations or unsupported claims.
2. level_appropriateness: Checks if the pitch matches learner_level (neither too basic nor needlessly impenetrable).
3. analogy_clarity: Evaluates if the dominant analogy maps cleanly and accurately, or if it is strained, misleading, or overloaded.
4. misconception_coverage: Verifies that stated pitfalls are genuinely common and accurately addressed.
5. scope_adherence: Checks that the draft stays within explicit_scope_in, avoids explicit_scope_out, and honors assumptions_to_avoid.

Output Format:
You must respond strictly with a valid JSON object matching:
{
  "issues": [
    {
      "issue": "Clear description of the issue",
      "severity": "minor|major",
      "suggestion": "Actionable correction instruction"
    }
  ],
  "verdict": "pass|revise"
}

Rules:
- A single "major" issue is enough for verdict: "revise".
- If all issues are minor or there are no issues, verdict should be "pass".
- Respond strictly with valid JSON without conversational wrapper.`

	var findingsText strings.Builder
	if len(findings) > 0 {
		for i, f := range findings {
			findingsText.WriteString(fmt.Sprintf("%d. %s\n", i+1, f.Claim))
		}
	} else {
		findingsText.WriteString("Standard authoritative reference knowledge.\n")
	}

	userPrompt = fmt.Sprintf(`Learner Context:
- Topic: %s
- Domain: %s
- Level: %s
- Known Reference Points: %s
- Explicit Scope In: %s
- Explicit Scope Out: %s
- Assumptions to Avoid: %s

Section Details:
- Title: "%s"
- Learning Objective: "%s"

Grounded Findings for this section:
%s

Section Draft to Evaluate:
---
%s
---

Evaluate this draft against the 5 rubric dimensions. Respond strictly with valid JSON.`,
		brief.Topic,
		brief.Domain,
		brief.LearnerLevel,
		strings.Join(brief.KnownReferencePoints, ", "),
		strings.Join(brief.ExplicitScopeIn, "; "),
		strings.Join(brief.ExplicitScopeOut, "; "),
		strings.Join(brief.AssumptionsToAvoid, "; "),
		section.Title,
		section.LearningObjective,
		findingsText.String(),
		draftMD,
	)

	return systemPrompt, userPrompt
}

// BuildSectionRevisionPrompt constructs the prompt for revising a draft based on critique notes.
func BuildSectionRevisionPrompt(brief *LearningBrief, section *TeacherOutlineSection, draftMD string, findings []TeacherFinding, notes []CritiqueNote) (systemPrompt, userPrompt string) {
	systemPrompt = `You are an expert pedagogical author revising a section of a personal learning report based on critic feedback.

Rules:
1. Address every issue and incorporate the suggestions provided by the reviewer.
2. Maintain the core pedagogical structure:
   - Plain-language core idea first.
   - One dominant analogy anchored to known reference points.
   - One concrete domain-appropriate worked example.
   - 1-2 common misconceptions and why they are wrong.
   - 2-3 sentence recap tied to the learning objective.
   - Fenced ` + "```mermaid```" + ` diagram if diagrams are wanted.
   - Glossary annotations <!--glossary: term=definition--> for key technical terms.
3. Preserve the strong elements of the original draft while fixing the flagged issues.
4. Output ONLY the complete revised section Markdown.`

	var issuesText strings.Builder
	for i, note := range notes {
		issuesText.WriteString(fmt.Sprintf("%d. [%s] %s -> Suggestion: %s\n", i+1, strings.ToUpper(note.Severity), note.Issue, note.Suggestion))
	}

	var findingsText strings.Builder
	for i, f := range findings {
		findingsText.WriteString(fmt.Sprintf("%d. %s\n", i+1, f.Claim))
	}

	userPrompt = fmt.Sprintf(`Topic: %s
Domain: %s
Learner Level: %s
Known Reference Points: %s
Section Title: "%s"
Learning Objective: "%s"

Reviewer Feedback to Address:
%s

Grounded Findings:
%s

Original Draft:
---
%s
---

Please rewrite this section addressing all feedback. Return ONLY the complete revised Markdown.`,
		brief.Topic,
		brief.Domain,
		brief.LearnerLevel,
		strings.Join(brief.KnownReferencePoints, ", "),
		section.Title,
		section.LearningObjective,
		issuesText.String(),
		findingsText.String(),
		draftMD,
	)

	return systemPrompt, userPrompt
}

