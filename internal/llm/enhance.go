package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const GroqDefaultModel = "llama-3.3-70b-versatile"
const GroqCompletionsURL = "https://api.groq.com/openai/v1/chat/completions"

// EnhancePromptWithGroq uses Groq's llama-3.3-70b-versatile model to transform a raw
// user prompt into a high-quality, structured, and effective prompt for an AI agent.
func EnhancePromptWithGroq(ctx context.Context, apiKey, userPrompt string) (string, error) {
	userPrompt = strings.TrimSpace(userPrompt)
	if userPrompt == "" {
		return "", fmt.Errorf("prompt is empty")
	}

	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return "", fmt.Errorf("groq API key is empty")
	}

	systemPrompt := `You are an expert prompt engineer. Your ONLY task is to take the user's input and REWRITE it into a clear, detailed, and highly effective prompt for an autonomous AI research agent.

CRITICAL RULES:
1. DO NOT ANSWER the user's prompt or question. Even if the user asks a direct question (e.g., "what is X?"), your job is to rewrite the prompt (e.g., "Research and explain X in detail..."), NOT to provide the answer to X.
2. Maintain the user's original intent, but flesh out explicit goals, actionable details, and scope.
3. Return ONLY the final enhanced prompt text.
4. Do not include any preambles, introductory commentary, markdown wrappers, or quote marks.`

	reqBody := chatCompletionRequest{
		Model: GroqDefaultModel,
		Messages: []Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
	}

	jsonBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, GroqCompletionsURL, bytes.NewBuffer(jsonBytes))
	if err != nil {
		return "", fmt.Errorf("failed to create http request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	hc := &http.Client{Timeout: 15 * time.Second}
	resp, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("groq api request failed: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read groq response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var chatResp chatCompletionResponse
		_ = json.Unmarshal(bodyBytes, &chatResp)
		if chatResp.Error != nil && chatResp.Error.Message != "" {
			return "", fmt.Errorf("groq api error (status %d): %s", resp.StatusCode, chatResp.Error.Message)
		}
		return "", fmt.Errorf("groq api error (status %d): %s", resp.StatusCode, string(bodyBytes))
	}

	var chatResp chatCompletionResponse
	if err := json.Unmarshal(bodyBytes, &chatResp); err != nil {
		return "", fmt.Errorf("failed to unmarshal groq response: %w", err)
	}

	if len(chatResp.Choices) == 0 {
		return "", fmt.Errorf("empty choices returned from groq model")
	}

	enhanced := strings.TrimSpace(chatResp.Choices[0].Message.Content)
	// Strip surrounding double quotes if present
	enhanced = strings.TrimPrefix(enhanced, "\"")
	enhanced = strings.TrimSuffix(enhanced, "\"")
	enhanced = strings.TrimSpace(enhanced)

	if enhanced == "" {
		return userPrompt, nil
	}

	return enhanced, nil
}
