package agent

import (
	"encoding/json"
	"testing"
)

func TestActionResponseParsing(t *testing.T) {
	rawJSON := `{
		"thought": "I will navigate to Hacker News",
		"action": {
			"name": "navigate",
			"args": {
				"url": "https://news.ycombinator.com"
			}
		}
	}`

	var resp ActionResponse
	err := json.Unmarshal([]byte(rawJSON), &resp)
	if err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if resp.Thought != "I will navigate to Hacker News" {
		t.Errorf("unexpected thought: %s", resp.Thought)
	}

	if resp.Action.Name != "navigate" {
		t.Errorf("unexpected action name: %s", resp.Action.Name)
	}

	var navArgs NavigateArgs
	err = json.Unmarshal(resp.Action.Args, &navArgs)
	if err != nil {
		t.Fatalf("Unmarshal args failed: %v", err)
	}

	if navArgs.URL != "https://news.ycombinator.com" {
		t.Errorf("unexpected url arg: %s", navArgs.URL)
	}
}

func TestAgentOptions(t *testing.T) {
	ag := NewAgent(nil, nil, WithMaxSteps(5))
	if ag.maxSteps != 5 {
		t.Errorf("expected maxSteps 5, got %d", ag.maxSteps)
	}
}

func TestWebSearchActionParsing(t *testing.T) {
	rawJSON := `{
		"thought": "I need to search for Go scraping libraries",
		"action": {
			"name": "web_search",
			"args": {
				"query": "go web scraping"
			}
		}
	}`

	var resp ActionResponse
	err := json.Unmarshal([]byte(rawJSON), &resp)
	if err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if resp.Action.Name != "web_search" {
		t.Errorf("expected action 'web_search', got %s", resp.Action.Name)
	}

	var searchArgs WebSearchArgs
	err = json.Unmarshal(resp.Action.Args, &searchArgs)
	if err != nil {
		t.Fatalf("Unmarshal search args failed: %v", err)
	}

	if searchArgs.Query != "go web scraping" {
		t.Errorf("expected query 'go web scraping', got %s", searchArgs.Query)
	}
}

