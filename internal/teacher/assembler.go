package teacher

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

var glossaryRegex = regexp.MustCompile(`(?i)<!--\s*glossary:\s*([^=\r\n]+?)\s*=\s*(.+?)\s*-->`)

type glossaryEntry struct {
	Term       string
	Definition string
}

// AssembleReport stitches all approved outline sections, generates the TOC, compiles
// glossary annotations, adds next steps, indexes into SQLite FTS5, and marks the run done.
func (o *Orchestrator) AssembleReport(ctx context.Context, runID string) (string, error) {
	if o.store == nil {
		return "", errors.New("teacher store is not initialized")
	}

	// Advance run status to assembling
	if err := o.store.UpdateRunStatus(runID, RunStatusAssembling); err != nil {
		return "", fmt.Errorf("failed to update run status to assembling: %w", err)
	}

	o.emitEvent(runID, "assembling", map[string]string{"run_id": runID})

	run, err := o.store.GetRun(runID)
	if err != nil {
		return "", fmt.Errorf("failed to get run %s: %w", runID, err)
	}
	if run == nil {
		return "", fmt.Errorf("teacher run %s not found", runID)
	}

	brief, err := o.GetBrief(runID)
	if err != nil {
		return "", fmt.Errorf("failed to get brief for run %s: %w", runID, err)
	}

	outline, err := o.store.GetOutline(runID)
	if err != nil {
		return "", fmt.Errorf("failed to fetch outline for run %s: %w", runID, err)
	}
	if len(outline) == 0 {
		return "", fmt.Errorf("no outline sections found for run %s", runID)
	}

	sections, err := o.store.GetSectionsForRun(runID)
	if err != nil {
		return "", fmt.Errorf("failed to fetch sections for run %s: %w", runID, err)
	}

	sectionMap := make(map[string]TeacherSection)
	for _, s := range sections {
		sectionMap[s.OutlineID] = s
	}

	topic := brief.Topic
	if strings.TrimSpace(topic) == "" {
		topic = run.RawGoal
	}

	var report strings.Builder

	// 1. Main Title
	report.WriteString(fmt.Sprintf("# %s\n\n", topic))

	// 2. "What You'll Learn" Overview
	var overviewText strings.Builder
	overviewText.WriteString(fmt.Sprintf("This educational guide provides a structured breakdown of **%s** tailored for the **%s** level.\n\n", topic, brief.LearnerLevel))
	overviewText.WriteString("### What You'll Learn\n\n")
	for _, sec := range outline {
		if strings.TrimSpace(sec.LearningObjective) != "" {
			overviewText.WriteString(fmt.Sprintf("- **%s**: %s\n", sec.Title, sec.LearningObjective))
		}
	}
	overviewText.WriteString("\n---\n\n")
	report.WriteString(overviewText.String())

	// 3. Table of Contents
	report.WriteString("## Table of Contents\n\n")
	for i, sec := range outline {
		slug := generateAnchorSlug(sec.Title)
		report.WriteString(fmt.Sprintf("%d. [%s](#%s)\n", i+1, sec.Title, slug))
	}
	report.WriteString(fmt.Sprintf("%d. [Glossary](#glossary)\n", len(outline)+1))
	report.WriteString(fmt.Sprintf("%d. [Where to Go Next](#where-to-go-next)\n\n", len(outline)+2))
	report.WriteString("---\n\n")

	// 4. Section Bodies & Glossary extraction
	glossaryMap := make(map[string]string) // lowercase term -> entry

	for _, sec := range outline {
		teacherSec, ok := sectionMap[sec.ID]
		content := ""
		if ok {
			content = teacherSec.FinalMD
			if strings.TrimSpace(content) == "" {
				content = teacherSec.DraftMD
			}
		}

		if strings.TrimSpace(content) == "" {
			content = fmt.Sprintf("*(Draft for %s pending)*", sec.Title)
		}

		// Extract glossary tags
		matches := glossaryRegex.FindAllStringSubmatch(content, -1)
		for _, m := range matches {
			if len(m) >= 3 {
				term := strings.TrimSpace(m[1])
				def := strings.TrimSpace(m[2])
				if term != "" && def != "" {
					key := strings.ToLower(term)
					if _, exists := glossaryMap[key]; !exists {
						glossaryMap[key] = fmt.Sprintf("- **%s**: %s", term, def)
					}
				}
			}
		}

		// Strip glossary comment tags from final rendered section
		cleanContent := glossaryRegex.ReplaceAllString(content, "")
		cleanContent = stripLeadingMatchingHeader(cleanContent, sec.Title)
		cleanContent = demoteSubHeaders(cleanContent)

		report.WriteString(fmt.Sprintf("<div id=\"teacher-sec-%s\" class=\"teacher-sec-block\" data-section-id=\"%s\">\n\n", sec.ID, sec.ID))
		report.WriteString(fmt.Sprintf("## %s\n\n", sec.Title))
		report.WriteString(strings.TrimSpace(cleanContent))
		report.WriteString("\n\n</div>\n\n---\n\n")

		// Index section for FTS
		_ = o.store.IndexReportFTS(runID, sec.Title, cleanContent)
	}

	// 5. Glossary Section
	report.WriteString("## Glossary\n\n")
	var glossaryLines []string
	for _, line := range glossaryMap {
		glossaryLines = append(glossaryLines, line)
	}
	sort.Strings(glossaryLines)

	var glossaryContent strings.Builder
	if len(glossaryLines) > 0 {
		for _, g := range glossaryLines {
			glossaryContent.WriteString(g + "\n")
		}
	} else {
		glossaryContent.WriteString("*Key terms and definitions are explained directly within their respective sections above.*\n")
	}
	report.WriteString(glossaryContent.String())
	report.WriteString("\n---\n\n")
	_ = o.store.IndexReportFTS(runID, "Glossary", glossaryContent.String())

	// 6. Where to Go Next Section
	report.WriteString("## Where to Go Next\n\n")
	var nextSteps strings.Builder
	nextSteps.WriteString(fmt.Sprintf("With a solid foundation in **%s**, here are suggested paths to continue building expertise:\n\n", topic))

	if len(brief.ExplicitScopeOut) > 0 {
		nextSteps.WriteString("### Advanced & Adjacent Topics\n")
		for _, out := range brief.ExplicitScopeOut {
			nextSteps.WriteString(fmt.Sprintf("- **%s**: Explore how this concept extends or integrates with broader systems.\n", out))
		}
		nextSteps.WriteString("\n")
	}

	if strings.TrimSpace(brief.Motivation) != "" {
		nextSteps.WriteString(fmt.Sprintf("### Application to Your Goals\n- Apply these principles directly toward your target: *%s*.\n\n", brief.Motivation))
	}

	report.WriteString(nextSteps.String())
	_ = o.store.IndexReportFTS(runID, "Where to Go Next", nextSteps.String())

	finalReportMD := report.String()

	// 7. Persist to SQLite teacher_runs
	if err := o.store.UpdateRunReport(runID, finalReportMD); err != nil {
		return "", fmt.Errorf("failed to save final report markdown to store: %w", err)
	}

	return finalReportMD, nil
}

// generateAnchorSlug converts a section title into a valid GitHub-style markdown anchor slug.
func generateAnchorSlug(title string) string {
	lower := strings.ToLower(strings.TrimSpace(title))
	var sb strings.Builder
	lastDash := false

	for _, r := range lower {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			sb.WriteRune(r)
			lastDash = false
		} else if unicode.IsSpace(r) || r == '-' || r == '_' || r == '/' {
			if !lastDash && sb.Len() > 0 {
				sb.WriteRune('-')
				lastDash = true
			}
		}
	}

	res := sb.String()
	res = strings.Trim(res, "-")
	return res
}

// stripLeadingMatchingHeader removes duplicate '# Title' or '## Title' if the drafted body starts with it.
func stripLeadingMatchingHeader(content, title string) string {
	trimmed := strings.TrimSpace(content)
	lines := strings.Split(trimmed, "\n")
	if len(lines) == 0 {
		return content
	}

	firstLine := strings.TrimSpace(lines[0])
	cleanFirst := strings.TrimLeft(firstLine, "# ")
	cleanTitle := strings.TrimSpace(title)

	if strings.EqualFold(strings.TrimSpace(cleanFirst), cleanTitle) {
		return strings.TrimSpace(strings.Join(lines[1:], "\n"))
	}

	return content
}

// demoteSubHeaders converts any internal '## ' headings to '### ' so that only outline section titles remain top-level H2s.
func demoteSubHeaders(content string) string {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## ") && !strings.HasPrefix(trimmed, "### ") {
			lines[i] = "#" + line
		}
	}
	return strings.Join(lines, "\n")
}
