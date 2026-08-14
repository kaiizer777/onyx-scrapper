package teacher

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kaiizer777/onyx-scrapper/internal/llm"
)

func TestPipeline_GenerateReportEndToEnd(t *testing.T) {
	rootStore, teacherStore := setupTestTeacherStore(t)
	defer rootStore.Close()

	run, err := teacherStore.CreateRun("Teach me about WebSockets vs HTTP Streaming")
	if err != nil {
		t.Fatalf("failed to create run: %v", err)
	}

	brief := &LearningBrief{
		Topic:                "WebSockets vs Server-Sent Events",
		Domain:               "Web Protocols",
		LearnerLevel:         "Junior Full-Stack Developer",
		Motivation:           "Choosing protocol for real-time notification service",
		Depth:                "working_understanding",
		KnownReferencePoints: []string{"HTTP/1.1 REST APIs", "TCP Sockets"},
		ExplicitScopeIn:      []string{"Handshake upgrade", "Bidirectional framing", "SSE text/event-stream"},
		ExplicitScopeOut:     []string{"WebRTC video streaming"},
		FormatPreferences: FormatPreferences{
			Length:        "medium",
			WantsDiagrams: true,
		},
	}
	if err := teacherStore.UpdateRunBrief(run.ID, brief); err != nil {
		t.Fatalf("failed to update brief: %v", err)
	}

	// Mock LLM server handling all pipeline phases
	server, client := newMockLLMServer(t, func(messages []llm.Message) (string, error) {
		lastMsg := messages[len(messages)-1].Content
		sysMsg := messages[0].Content

		// 1. Outline Planner Phase
		if strings.Contains(sysMsg, "OutlinePlanner") || strings.Contains(sysMsg, "master curriculum designer") {
			resp := OutlinePlannerResponse{
				Thought: "Sequencing WebSockets vs SSE outline from intuition to handshake and framing.",
				Sections: []OutlinePlannerSection{
					{
						ID:                "sec_0",
						Title:             SectionZeroTitle,
						LearningObjective: "Understand why real-time web applications need persistent bidirectional channels.",
						DependsOn:         []string{},
					},
					{
						ID:                "sec_1",
						Title:             "HTTP 101 Switching Protocols Handshake",
						LearningObjective: "Trace how an HTTP GET upgrades into a persistent WebSocket connection.",
						DependsOn:         []string{"sec_0"},
					},
					{
						ID:                "sec_2",
						Title:             "Binary Framing vs Server-Sent Events",
						LearningObjective: "Compare bidirectional WebSocket frames with unidirectional text/event-stream.",
						DependsOn:         []string{"sec_1"},
					},
				},
			}
			b, _ := json.Marshal(resp)
			return string(b), nil
		}

		// 2. Query Generation (Research Phase)
		if strings.Contains(sysMsg, "expert research query planner") {
			resp := SectionQueryGenResponse{
				Queries: []string{"websocket upgrade handshake rfc6455", "server-sent events vs websockets"},
			}
			b, _ := json.Marshal(resp)
			return string(b), nil
		}

		// 3. Claim Extraction (Research Phase)
		if strings.Contains(sysMsg, "claim extraction assistant") {
			resp := SectionClaimExtractionResponse{
				Claims: []ExtractedClaimItem{
					{
						Claim:      "WebSocket establishes a full-duplex TCP channel via an HTTP 101 upgrade handshake.",
						SourceURL:  "https://datatracker.ietf.org/doc/html/rfc6455",
						Confidence: 0.98,
					},
				},
			}
			b, _ := json.Marshal(resp)
			return string(b), nil
		}

		// 4. Critic Evaluation Phase
		if strings.Contains(sysMsg, "independent educational reviewer") || strings.Contains(sysMsg, "Rubric Dimensions") {
			resp := CritiqueEvaluationResponse{
				Issues:  []CritiqueNote{},
				Verdict: "pass",
			}
			b, _ := json.Marshal(resp)
			return string(b), nil
		}

		// 5. Section Writer Phase
		if strings.Contains(sysMsg, "expert pedagogical author") || strings.Contains(lastMsg, "Section Title:") {
			return `WebSockets allow continuous bidirectional communication over a single TCP socket.

Think of standard HTTP like sending letters back and forth; WebSockets are like opening a direct phone line.

<!--glossary: Upgrade Handshake=An HTTP/1.1 request with Connection: Upgrade and Upgrade: websocket headers.-->
<!--glossary: Full Duplex=Simultaneous two-way communication over a single channel.-->

A common misconception is that WebSockets run on a separate port from HTTP; in practice they reuse port 80/443 after the initial handshake.

Recap: WebSockets eliminate polling overhead by upgrading standard HTTP connections into full-duplex streams.`, nil
		}

		return "Default response", nil
	})
	defer server.Close()

	orch := NewOrchestratorWithStore(client, teacherStore, nil, nil)

	finishedRun, err := orch.GenerateReport(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("GenerateReport pipeline failed: %v", err)
	}

	if finishedRun == nil {
		t.Fatalf("expected non-nil completed run")
	}

	if finishedRun.Status != RunStatusDone {
		t.Errorf("expected final run status %q, got %q", RunStatusDone, finishedRun.Status)
	}

	if finishedRun.ReportMD == "" {
		t.Fatalf("expected non-empty report_md")
	}

	// Verify report contents
	if !strings.Contains(finishedRun.ReportMD, "# WebSockets vs Server-Sent Events") {
		t.Errorf("missing main H1 title in generated report")
	}
	if !strings.Contains(finishedRun.ReportMD, "## Table of Contents") {
		t.Errorf("missing Table of Contents")
	}
	if !strings.Contains(finishedRun.ReportMD, "## Glossary") {
		t.Errorf("missing Glossary section")
	}
	if !strings.Contains(finishedRun.ReportMD, "- **Full Duplex**:") ||
		!strings.Contains(finishedRun.ReportMD, "- **Upgrade Handshake**:") {
		t.Errorf("missing compiled glossary entries in report")
	}

	// Verify all outline sections are marked done
	outline, err := teacherStore.GetOutline(run.ID)
	if err != nil {
		t.Fatalf("failed to fetch outline: %v", err)
	}
	if len(outline) != 3 {
		t.Fatalf("expected 3 outline sections, got %d", len(outline))
	}
	for _, sec := range outline {
		if sec.Status != OutlineStatusDone {
			t.Errorf("expected outline section %s status %q, got %q", sec.Title, OutlineStatusDone, sec.Status)
		}
	}

	// Verify FTS searchability
	searchHits, err := teacherStore.SearchFTS("Full Duplex", 5)
	if err != nil {
		t.Fatalf("FTS search failed: %v", err)
	}
	if len(searchHits) == 0 {
		t.Errorf("expected FTS search to find results for 'Full Duplex'")
	}
}
