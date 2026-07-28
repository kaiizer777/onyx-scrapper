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

	"github.com/kaiizer777/onyx-scrapper/internal/config"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatCompletionRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
}

type chatCompletionResponse struct {
	Choices []struct {
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error,omitempty"`
}

type Client struct {
	baseURL string
	apiKey  string
	model   string
	hc      *http.Client
}

func NewClient(cfg config.OpenCodeZenConfig) *Client {
	model := cfg.DefaultModel
	if model == "" {
		model = "mimo-v2.5-free"
	}
	return &Client{
		baseURL: strings.TrimSuffix(cfg.BaseURL, "/"),
		apiKey:  cfg.APIKey,
		model:   model,
		hc:      &http.Client{Timeout: 60 * time.Second},
	}
}

func NewClientFromConfigPath(path string) (*Client, error) {
	cfg, err := config.LoadConfig(path)
	if err != nil {
		return nil, err
	}
	return NewClient(cfg.OpenCodeZen), nil
}

func (c *Client) Chat(ctx context.Context, messages []Message) (string, error) {
	url := fmt.Sprintf("%s/chat/completions", c.baseURL)

	reqBody := chatCompletionRequest{
		Model:    c.model,
		Messages: messages,
	}

	jsonBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	maxRetries := 10
	backoff := 1 * time.Second
	maxBackoff := 30 * time.Second

	for attempt := 1; attempt <= maxRetries+1; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewBuffer(jsonBytes))
		if err != nil {
			return "", fmt.Errorf("failed to create http request: %w", err)
		}

		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.apiKey))

		resp, reqErr := c.hc.Do(req)

		var retryable bool
		var errStr string

		if reqErr != nil {
			retryable = true
			errStr = reqErr.Error()
		} else {
			bodyBytes, readErr := io.ReadAll(resp.Body)
			resp.Body.Close()

			if readErr != nil {
				retryable = true
				errStr = fmt.Sprintf("failed to read response body: %v", readErr)
			} else if resp.StatusCode != http.StatusOK {
				var chatResp chatCompletionResponse
				_ = json.Unmarshal(bodyBytes, &chatResp)

				if chatResp.Error != nil && chatResp.Error.Message != "" {
					errStr = fmt.Sprintf("api error (status %d): %s", resp.StatusCode, chatResp.Error.Message)
				} else {
					errStr = fmt.Sprintf("api error (status %d): %s", resp.StatusCode, string(bodyBytes))
				}

				if resp.StatusCode >= 500 {
					retryable = true
				} else {
					retryable = false // e.g., 4xx errors
				}
			} else {
				var chatResp chatCompletionResponse
				if unmarshalErr := json.Unmarshal(bodyBytes, &chatResp); unmarshalErr != nil {
					return "", fmt.Errorf("failed to unmarshal response: %w (raw body: %s)", unmarshalErr, string(bodyBytes))
				}

				if len(chatResp.Choices) == 0 {
					return "", fmt.Errorf("empty choices returned from model")
				}

				return chatResp.Choices[0].Message.Content, nil
			}
		}

		if !retryable || attempt > maxRetries {
			if !retryable {
				return "", fmt.Errorf("%s", errStr)
			}
			return "", fmt.Errorf("max retries exhausted. last error: %s", errStr)
		}

		fmt.Printf("[RETRY] Attempt %d failed: %s. Retrying in %v...\n", attempt, errStr, backoff)

		select {
		case <-ctx.Done():
			return "", fmt.Errorf("context cancelled during retry backoff: %w", ctx.Err())
		case <-time.After(backoff):
		}

		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}

	return "", fmt.Errorf("unreachable")
}
