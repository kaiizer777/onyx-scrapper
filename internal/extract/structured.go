package extract

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/kaiizer-99/onyx-scrapper/internal/llm"
)

const MaxExtractContentCharLimit = 35000

// LLMClient represents the interface required to perform LLM chat completions.
type LLMClient interface {
	Chat(messages []llm.Message) (string, error)
}

// Extractor handles extracting structured JSON from text/HTML using an LLM.
type Extractor struct {
	client LLMClient
}

// NewExtractor initializes a new Extractor instance.
func NewExtractor(client LLMClient) *Extractor {
	return &Extractor{client: client}
}

// ExtractJSON extracts structured data matching schema from content.
func (e *Extractor) ExtractJSON(content string, schema string) (json.RawMessage, error) {
	return ExtractJSON(e.client, content, schema)
}

// ExtractJSON sends page content and schema to the LLM model to return valid JSON matching the schema.
// If the LLM response is invalid JSON, it retries once with an error-correction prompt.
func ExtractJSON(client LLMClient, content string, schema string) (json.RawMessage, error) {
	if client == nil {
		return nil, fmt.Errorf("llm client is required")
	}
	if strings.TrimSpace(content) == "" {
		return nil, fmt.Errorf("content cannot be empty")
	}
	if strings.TrimSpace(schema) == "" {
		return nil, fmt.Errorf("schema cannot be empty")
	}

	// Resolve predefined schema name if applicable
	resolvedSchema, found := GetSchemaTemplate(schema)
	if found {
		schema = resolvedSchema
	}

	// If content is raw HTML, attempt to clean/convert to readable text first if possible
	cleanContent := content
	if strings.Contains(content, "<html") || strings.Contains(content, "<div") || strings.Contains(content, "<body") {
		if text, err := CleanHTML(content); err == nil && len(strings.TrimSpace(text)) > 0 {
			cleanContent = text
		}
	}

	// Token budget guard
	if len(cleanContent) > MaxExtractContentCharLimit {
		cleanContent = cleanContent[:MaxExtractContentCharLimit] + "\n... [Content Truncated for Token Budget]"
	}

	systemPrompt := "You are a precise data extraction engine. Extract structured data from content strictly matching the given JSON schema. Return ONLY valid JSON, with no prose, markdown fences, or explanations."
	userPrompt := fmt.Sprintf("Target JSON Schema:\n%s\n\nPage Content:\n%s", schema, cleanContent)

	messages := []llm.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	}

	rawResp, err := client.Chat(messages)
	if err != nil {
		return nil, fmt.Errorf("llm chat error: %w", err)
	}

	cleanedResp := CleanJSONResponse(rawResp)

	var rawJSON json.RawMessage
	if err := json.Unmarshal([]byte(cleanedResp), &rawJSON); err == nil && len(rawJSON) > 0 {
		return rawJSON, nil
	}

	// First attempt failed to parse as valid JSON. Retry once with error correction.
	parseErr := err
	if parseErr == nil {
		parseErr = fmt.Errorf("empty JSON output")
	}

	retryUserPrompt := fmt.Sprintf("Your last output was invalid JSON: %v. Fix it and output ONLY valid JSON matching the schema:\n%s", parseErr, schema)

	messages = append(messages,
		llm.Message{Role: "assistant", Content: rawResp},
		llm.Message{Role: "user", Content: retryUserPrompt},
	)

	retryResp, err := client.Chat(messages)
	if err != nil {
		return nil, fmt.Errorf("llm retry chat error: %w (original parse error: %v)", err, parseErr)
	}

	cleanedRetryResp := CleanJSONResponse(retryResp)
	var retryJSON json.RawMessage
	if err := json.Unmarshal([]byte(cleanedRetryResp), &retryJSON); err != nil {
		return nil, fmt.Errorf("failed to parse JSON after retry: %w (raw response: %q)", err, retryResp)
	}

	return retryJSON, nil
}

// CleanJSONResponse strips markdown code fences, leading/trailing prose or quotes from LLM response.
func CleanJSONResponse(resp string) string {
	s := strings.TrimSpace(resp)

	// Remove markdown code block fences (e.g. ```json ... ``` or ``` ...)
	reFence := regexp.MustCompile("(?s)^```(?:json)?\\s*(.*?)\\s*```$")
	if matches := reFence.FindStringSubmatch(s); len(matches) > 1 {
		s = strings.TrimSpace(matches[1])
	} else {
		// If code blocks were not matching end-to-end, strip leading/trailing fence if present
		s = regexp.MustCompile("^```(?:json)?\\s*").ReplaceAllString(s, "")
		s = regexp.MustCompile("\\s*```$").ReplaceAllString(s, "")
	}

	s = strings.TrimSpace(s)

	// Extract JSON object {...} or array [...] if surrounded by extra text
	startObj := strings.Index(s, "{")
	startArr := strings.Index(s, "[")

	start := -1
	end := -1

	if startObj != -1 && (startArr == -1 || startObj < startArr) {
		start = startObj
		end = strings.LastIndex(s, "}")
	} else if startArr != -1 {
		start = startArr
		end = strings.LastIndex(s, "]")
	}

	if start != -1 && end != -1 && end > start {
		s = s[start : end+1]
	}

	return strings.TrimSpace(s)
}
