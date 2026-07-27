package extract

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/kaiizer-99/onyx-scrapper/internal/llm"
)

type mockLLMClient struct {
	responses []string
	errors    []error
	callCount int
	history   [][]llm.Message
}

func (m *mockLLMClient) Chat(_ context.Context, messages []llm.Message) (string, error) {
	m.history = append(m.history, messages)
	idx := m.callCount
	m.callCount++

	if idx < len(m.errors) && m.errors[idx] != nil {
		return "", m.errors[idx]
	}
	if idx < len(m.responses) {
		return m.responses[idx], nil
	}
	return "", fmt.Errorf("unexpected chat call #%d", idx)
}

func TestGetSchemaTemplate(t *testing.T) {
	templates := []string{"article", "product", "event", "search-result-list", "search_results", "search"}
	for _, name := range templates {
		schema, found := GetSchemaTemplate(name)
		if !found {
			t.Errorf("expected GetSchemaTemplate(%q) to return found=true", name)
		}
		if !strings.Contains(schema, `"$schema"`) {
			t.Errorf("expected GetSchemaTemplate(%q) to return valid JSON schema, got %s", name, schema)
		}
	}

	_, found := GetSchemaTemplate("unknown-schema-xyz")
	if found {
		t.Errorf("expected GetSchemaTemplate for unknown schema to return false")
	}
}

func TestCleanJSONResponse(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "clean json object",
			input:    `{"name": "iPhone 15", "price": "$999"}`,
			expected: `{"name": "iPhone 15", "price": "$999"}`,
		},
		{
			name:     "markdown json code fence",
			input:    "```json\n{\n  \"name\": \"iPhone 15\",\n  \"price\": \"$999\"\n}\n```",
			expected: "{\n  \"name\": \"iPhone 15\",\n  \"price\": \"$999\"\n}",
		},
		{
			name:     "json array with leading and trailing prose",
			input:    "Here is the requested data:\n[{\"title\": \"Item 1\"}, {\"title\": \"Item 2\"}]\nHope this helps!",
			expected: "[{\"title\": \"Item 1\"}, {\"title\": \"Item 2\"}]",
		},
		{
			name:     "partial code fences",
			input:    "```\n{\"status\": \"ok\"}\n```",
			expected: "{\"status\": \"ok\"}",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := CleanJSONResponse(tt.input)
			if actual != tt.expected {
				t.Errorf("CleanJSONResponse() = %q, expected %q", actual, tt.expected)
			}
		})
	}
}

func TestExtractJSON_Success(t *testing.T) {
	mockResp := "```json\n{\"name\": \"Wireless Headphones\", \"price\": \"$149.99\", \"availability\": \"In Stock\"}\n```"
	client := &mockLLMClient{
		responses: []string{mockResp},
	}

	ctx := context.Background()
	content := "<html><body><h1>Wireless Headphones</h1><p>Price: $149.99</p><p>Status: In Stock</p></body></html>"
	rawJSON, err := ExtractJSON(ctx, client, content, "product")
	if err != nil {
		t.Fatalf("ExtractJSON failed: %v", err)
	}

	var product ProductSchema
	if err := json.Unmarshal(rawJSON, &product); err != nil {
		t.Fatalf("failed to unmarshal extracted JSON into ProductSchema: %v", err)
	}

	if product.Name != "Wireless Headphones" {
		t.Errorf("expected product name 'Wireless Headphones', got %q", product.Name)
	}
	if product.Price != "$149.99" {
		t.Errorf("expected product price '$149.99', got %q", product.Price)
	}
	if product.Availability != "In Stock" {
		t.Errorf("expected product availability 'In Stock', got %q", product.Availability)
	}

	if client.callCount != 1 {
		t.Errorf("expected 1 call to client.Chat, got %d", client.callCount)
	}
}

func TestExtractJSON_RetrySuccess(t *testing.T) {
	invalidJSONResp := "Here is the product info: Name: Headphones, Price: $100 (Oops forgot JSON structure)"
	validJSONResp := `{"name": "Headphones", "price": "$100", "availability": "In Stock"}`

	client := &mockLLMClient{
		responses: []string{invalidJSONResp, validJSONResp},
	}

	ctx := context.Background()
	content := "Headphones details - Price $100, In Stock"
	rawJSON, err := ExtractJSON(ctx, client, content, "product")
	if err != nil {
		t.Fatalf("ExtractJSON with retry failed: %v", err)
	}

	if client.callCount != 2 {
		t.Fatalf("expected 2 calls to client.Chat (initial + retry), got %d", client.callCount)
	}

	// Verify retry message payload contains error prompt
	retryMessages := client.history[1]
	if len(retryMessages) < 3 {
		t.Fatalf("expected at least 3 messages in retry conversation, got %d", len(retryMessages))
	}

	lastMsg := retryMessages[len(retryMessages)-1].Content
	if !strings.Contains(lastMsg, "invalid JSON") {
		t.Errorf("expected retry message to mention 'invalid JSON', got: %s", lastMsg)
	}

	var product ProductSchema
	if err := json.Unmarshal(rawJSON, &product); err != nil {
		t.Fatalf("failed to parse retry output: %v", err)
	}
	if product.Name != "Headphones" {
		t.Errorf("expected product name 'Headphones', got %q", product.Name)
	}
}

func TestExtractJSON_RetryFail(t *testing.T) {
	invalidResp1 := "Not a JSON"
	invalidResp2 := "Still not a JSON"

	client := &mockLLMClient{
		responses: []string{invalidResp1, invalidResp2},
	}

	ctx := context.Background()
	_, err := ExtractJSON(ctx, client, "some content", "product")
	if err == nil {
		t.Fatalf("expected ExtractJSON to fail when retry also returns invalid JSON, but it succeeded")
	}

	if !strings.Contains(err.Error(), "failed to parse JSON after retry") {
		t.Errorf("expected error message to contain 'failed to parse JSON after retry', got: %v", err)
	}
}
