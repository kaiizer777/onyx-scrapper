package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/kaiizer-99/onyx-scrapper/internal/config"
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

func (c *Client) Chat(messages []Message) (string, error) {
	url := fmt.Sprintf("%s/chat/completions", c.baseURL)

	reqBody := chatCompletionRequest{
		Model:    c.model,
		Messages: messages,
	}

	jsonBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewBuffer(jsonBytes))
	if err != nil {
		return "", fmt.Errorf("failed to create http request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.apiKey))

	resp, err := c.hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("http request error: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body: %w", err)
	}

	var chatResp chatCompletionResponse
	if err := json.Unmarshal(bodyBytes, &chatResp); err != nil {
		return "", fmt.Errorf("failed to unmarshal response: %w (raw body: %s)", err, string(bodyBytes))
	}

	if resp.StatusCode != http.StatusOK {
		if chatResp.Error != nil && chatResp.Error.Message != "" {
			return "", fmt.Errorf("api error (status %d): %s", resp.StatusCode, chatResp.Error.Message)
		}
		return "", fmt.Errorf("api error (status %d): %s", resp.StatusCode, string(bodyBytes))
	}

	if len(chatResp.Choices) == 0 {
		return "", fmt.Errorf("empty choices returned from model")
	}

	return chatResp.Choices[0].Message.Content, nil
}
