package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
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
	mu      sync.RWMutex
	baseURL string
	apiKey  string
	model   string
	hc      *http.Client
}

// SetBaseURL safely updates the base URL used for chat completion requests.
func (c *Client) SetBaseURL(baseURL string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.baseURL = strings.TrimSuffix(baseURL, "/")
}

// SetAPIKey safely updates the bearer token used for chat completion requests.
func (c *Client) SetAPIKey(apiKey string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.apiKey = apiKey
}

// SetModel safely updates the model identifier used for chat completion requests.
func (c *Client) SetModel(model string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.model = model
}

// Snapshot returns the current base URL, model, and a masked API key for display
// purposes. The API key is masked to first 4 + last 4 characters; if the key is
// shorter than 8 characters, only the first 2 characters are shown.
func (c *Client) Snapshot() (baseURL, model, maskedKey string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.baseURL, c.model, maskAPIKey(c.apiKey)
}

// RawAPIKey returns the unmasked API key. Use this only when persisting the key
// to config or making an authenticated call (e.g. listing models).
func (c *Client) RawAPIKey() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.apiKey
}

func maskAPIKey(key string) string {
	if key == "" {
		return ""
	}
	if len(key) <= 8 {
		if len(key) <= 2 {
			return "**"
		}
		return key[:2] + strings.Repeat("*", len(key)-2)
	}
	return key[:4] + strings.Repeat("*", len(key)-8) + key[len(key)-4:]
}

func NewClient(p config.ProviderConfig) *Client {
	return &Client{
		baseURL: strings.TrimSuffix(p.BaseURL, "/"),
		apiKey:  p.APIKey,
		model:   p.Model,
		hc:      &http.Client{Timeout: 10 * time.Minute},
	}
}

func NewClientFromConfigPath(path string) (*Client, error) {
	cfg, err := config.LoadConfig(path)
	if err != nil {
		return nil, err
	}
	return NewClient(cfg.ActiveProviderConfig()), nil
}

func (c *Client) Chat(ctx context.Context, messages []Message) (string, error) {
	c.mu.RLock()
	baseURL := c.baseURL
	apiKey := c.apiKey
	model := c.model
	c.mu.RUnlock()

	url := fmt.Sprintf("%s/chat/completions", baseURL)

	reqBody := chatCompletionRequest{
		Model:    model,
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
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", apiKey))

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
