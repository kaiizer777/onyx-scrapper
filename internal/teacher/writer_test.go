package teacher

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaiizer777/onyx-scrapper/internal/config"
	"github.com/kaiizer777/onyx-scrapper/internal/llm"
)

func TestWriter_DraftSectionEndToEnd(t *testing.T) {
	rootStore, teacherStore := setupTestTeacherStore(t)
	defer rootStore.Close()

	run, err := teacherStore.CreateRun("Learn Transformer Attention")
	if err != nil {
		t.Fatalf("failed to create run: %v", err)
	}

	wantsCode := true
	brief := &LearningBrief{
		Topic:                "Self-Attention in Transformers",
		Domain:               "Machine Learning",
		LearnerLevel:         "Intermediate software engineer",
		Motivation:           "Implementing custom attention layers in PyTorch",
		Depth:                "working_understanding",
		KnownReferencePoints: []string{"Database Indexing", "Hash Maps"},
		ExplicitScopeIn:      []string{"Scaled Dot-Product", "Query/Key/Value projections"},
		ExplicitScopeOut:     []string{"FlashAttention GPU kernels"},
		FormatPreferences: FormatPreferences{
			Length:            "medium",
			WantsDiagrams:     true,
			WantsCodeExamples: &wantsCode,
		},
	}
	if err := teacherStore.UpdateRunBrief(run.ID, brief); err != nil {
		t.Fatalf("failed to update brief: %v", err)
	}

	secOutline := TeacherOutlineSection{
		ID:                "sec_test_attn",
		RunID:             run.ID,
		SectionOrder:      1,
		Title:             "Scaled Dot-Product Attention Mechanics",
		LearningObjective: "Compute scaled dot-product attention scores from Q, K, V tensors.",
		Status:            OutlineStatusPending,
	}
	if err := teacherStore.SaveOutline([]TeacherOutlineSection{secOutline}); err != nil {
		t.Fatalf("failed to save outline: %v", err)
	}

	// Seed finding
	finding := &TeacherFinding{
		ID:             "tf_attn_1",
		RunID:          run.ID,
		SectionID:      secOutline.ID,
		Claim:          "Scaled dot-product attention calculates softmax(QK^T / sqrt(d_k)) * V to weight values by query-key relevance.",
		SourceURL:      "https://arxiv.org/abs/1706.03762",
		SourceProvider: "arxiv",
		AuthorityTier:  "Primary",
		Confidence:     0.99,
		CreatedAt:      time.Now().UTC(),
	}
	if err := teacherStore.SaveFinding(finding); err != nil {
		t.Fatalf("failed to save finding: %v", err)
	}

	draftOutput := `At its core, self-attention allows a neural network to dynamically decide which parts of a sequence are most relevant to each other.

Imagine searching a database index (our analogy): the Query is your search filter, Keys are the indexed tags, and Values are the stored payload records. When the Query matches a Key, you retrieve the corresponding Value.

Here is a PyTorch snippet illustrating the computation:
` + "```python\nimport torch\nimport math\n\ndef attention(q, k, v):\n    d_k = q.size(-1)\n    scores = torch.matmul(q, k.transpose(-2, -1)) / math.sqrt(d_k)\n    p_attn = scores.softmax(dim=-1)\n    return torch.matmul(p_attn, v)\n```" + `

A common misconception is that attention scores must sum to 1 across the sequence before softmax; softmax itself is what normalizes dot products into a valid probability distribution.

In summary, scaled dot-product attention projects tokens into Query, Key, and Value spaces, scales similarities by sqrt(d_k), and weights values by softmax relevance.

<!--glossary: Softmax Normalization=An activation function turning logits into a probability distribution summing to 1.-->
` + "```mermaid\ngraph TD\nQ[Query] --> MatMul\nK[Key] --> MatMul\nMatMul --> Scale[Scale by sqrt d_k]\nScale --> Softmax\nSoftmax --> OutputMatMul\nV[Value] --> OutputMatMul\nOutputMatMul --> Out[Context Vector]\n```"

	var promptChecked atomic.Bool
	server, client := newMockLLMServer(t, func(messages []llm.Message) (string, error) {
		for _, m := range messages {
			if strings.Contains(m.Content, "Database Indexing") &&
				strings.Contains(m.Content, "Scaled Dot-Product Attention Mechanics") &&
				strings.Contains(m.Content, "softmax(QK^T / sqrt(d_k))") {
				promptChecked.Store(true)
			}
		}
		return draftOutput, nil
	})
	defer server.Close()

	orch := NewOrchestratorWithStore(client, teacherStore, nil, nil)

	sec, err := orch.DraftSection(context.Background(), run.ID, &secOutline)
	if err != nil {
		t.Fatalf("DraftSection failed: %v", err)
	}

	if !promptChecked.Load() {
		t.Errorf("expected prompt to contain reference points, section title, and findings")
	}

	if sec == nil || sec.DraftMD == "" {
		t.Fatalf("expected non-empty draft")
	}

	// Verify persistence in SQLite
	persistedSec, err := teacherStore.GetSection(sec.ID)
	if err != nil {
		t.Fatalf("failed to retrieve section from store: %v", err)
	}
	if persistedSec == nil || !strings.Contains(persistedSec.DraftMD, "database index") {
		t.Errorf("persisted section draft missing expected content: %v", persistedSec)
	}

	// Verify outline status advanced to critiquing
	outlineList, err := teacherStore.GetOutline(run.ID)
	if err != nil || len(outlineList) == 0 {
		t.Fatalf("failed to get outline: %v", err)
	}
	if outlineList[0].Status != OutlineStatusCritiquing {
		t.Errorf("expected outline section status %q, got %q", OutlineStatusCritiquing, outlineList[0].Status)
	}
}

func TestWriter_DraftAllSectionsConcurrency(t *testing.T) {
	rootStore, teacherStore := setupTestTeacherStore(t)
	defer rootStore.Close()

	run, err := teacherStore.CreateRun("Learn Distributed Systems")
	if err != nil {
		t.Fatalf("failed to create run: %v", err)
	}

	brief := &LearningBrief{
		Topic:        "Distributed Systems Fundamentals",
		Domain:       "Computer Science",
		LearnerLevel: "Senior Engineer",
		Motivation:   "Architecture Review",
		Depth:        "working_understanding",
	}
	_ = teacherStore.UpdateRunBrief(run.ID, brief)

	var outline []TeacherOutlineSection
	for i := 0; i < 4; i++ {
		outline = append(outline, TeacherOutlineSection{
			ID:                generateID("to"),
			RunID:             run.ID,
			SectionOrder:      i,
			Title:             strings.Repeat("Section ", 1) + string(rune('A'+i)),
			LearningObjective: "Understand core concepts",
			Status:            OutlineStatusPending,
		})
	}
	_ = teacherStore.SaveOutline(outline)

	var concurrentDrafts int32
	var maxConcurrent int32

	server, client := newMockLLMServer(t, func(messages []llm.Message) (string, error) {
		curr := atomic.AddInt32(&concurrentDrafts, 1)
		for {
			oldMax := atomic.LoadInt32(&maxConcurrent)
			if curr <= oldMax || atomic.CompareAndSwapInt32(&maxConcurrent, oldMax, curr) {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
		atomic.AddInt32(&concurrentDrafts, -1)
		return "Draft body content for section.", nil
	})
	defer server.Close()

	cfg := &config.Config{
		Teacher: &config.TeacherConfig{
			SectionWorkerConcurrency: 3,
		},
	}

	orch := NewOrchestratorWithStore(client, teacherStore, nil, cfg)

	drafts, err := orch.DraftAllSections(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("DraftAllSections failed: %v", err)
	}

	if len(drafts) != 4 {
		t.Fatalf("expected 4 drafts, got %d", len(drafts))
	}

	if atomic.LoadInt32(&maxConcurrent) < 2 {
		t.Logf("concurrency observed: %d", atomic.LoadInt32(&maxConcurrent))
	}

	// Verify run status updated to writing
	updatedRun, _ := teacherStore.GetRun(run.ID)
	if updatedRun.Status != RunStatusWriting {
		t.Errorf("expected run status %q, got %q", RunStatusWriting, updatedRun.Status)
	}
}

func TestWriter_CleanMarkdownContent(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "plain triple backticks",
			input:    "```\n# Header\nContent here\n```",
			expected: "# Header\nContent here",
		},
		{
			name:     "markdown code fences",
			input:    "```markdown\n# Section Title\nDetailed notes.\n```",
			expected: "# Section Title\nDetailed notes.",
		},
		{
			name:     "md code fences",
			input:    "```md\n# Fast Title\nSummary.\n```",
			expected: "# Fast Title\nSummary.",
		},
		{
			name:     "json code fences",
			input:    "```json\n{\"key\": \"val\"}\n```",
			expected: "{\"key\": \"val\"}",
		},
		{
			name:     "no fences unmodified",
			input:    "# Normal Header\nParagraph here",
			expected: "# Normal Header\nParagraph here",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := cleanMarkdownContent(tc.input)
			if got != tc.expected {
				t.Errorf("expected %q, got %q", tc.expected, got)
			}
		})
	}
}
