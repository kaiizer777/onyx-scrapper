package extract

import (
	"strings"
)

// Common schema Go struct representations for documentation and typed usage

type ArticleSchema struct {
	Title          string   `json:"title"`
	Author         string   `json:"author,omitempty"`
	DatePublished  string   `json:"date_published,omitempty"`
	ContentSummary string   `json:"content_summary"`
	Tags           []string `json:"tags,omitempty"`
}

type ProductSchema struct {
	Name         string   `json:"name"`
	Price        string   `json:"price"`
	Currency     string   `json:"currency,omitempty"`
	Availability string   `json:"availability"`
	Rating       string   `json:"rating,omitempty"`
	Description  string   `json:"description,omitempty"`
	Features     []string `json:"features,omitempty"`
}

type EventSchema struct {
	Title       string `json:"title"`
	Organizer   string `json:"organizer,omitempty"`
	Date        string `json:"date"`
	Location    string `json:"location"`
	Description string `json:"description,omitempty"`
	TicketPrice string `json:"ticket_price,omitempty"`
}

type SearchResultItem struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

type SearchResultListSchema struct {
	Query   string             `json:"query,omitempty"`
	Results []SearchResultItem `json:"results"`
}

// Built-in JSON Schema template strings

const ArticleJSONSchema = `{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "type": "object",
  "properties": {
    "title": { "type": "string" },
    "author": { "type": "string" },
    "date_published": { "type": "string" },
    "content_summary": { "type": "string" },
    "tags": {
      "type": "array",
      "items": { "type": "string" }
    }
  },
  "required": ["title", "content_summary"]
}`

const ProductJSONSchema = `{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "type": "object",
  "properties": {
    "name": { "type": "string" },
    "price": { "type": "string" },
    "currency": { "type": "string" },
    "availability": { "type": "string" },
    "rating": { "type": "string" },
    "description": { "type": "string" },
    "features": {
      "type": "array",
      "items": { "type": "string" }
    }
  },
  "required": ["name", "price", "availability"]
}`

const EventJSONSchema = `{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "type": "object",
  "properties": {
    "title": { "type": "string" },
    "organizer": { "type": "string" },
    "date": { "type": "string" },
    "location": { "type": "string" },
    "description": { "type": "string" },
    "ticket_price": { "type": "string" }
  },
  "required": ["title", "date", "location"]
}`

const SearchResultListJSONSchema = `{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "type": "object",
  "properties": {
    "query": { "type": "string" },
    "results": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "title": { "type": "string" },
          "url": { "type": "string" },
          "snippet": { "type": "string" }
        },
        "required": ["title", "url"]
      }
    }
  },
  "required": ["results"]
}`

// GetSchemaTemplate resolves a schema template name (e.g. "article", "product", "event", "search-result-list")
// to its corresponding JSON schema string. Returns the schema and true if found, empty string and false otherwise.
func GetSchemaTemplate(name string) (string, bool) {
	key := strings.ToLower(strings.TrimSpace(name))
	key = strings.ReplaceAll(key, "_", "-")

	switch key {
	case "article":
		return ArticleJSONSchema, true
	case "product":
		return ProductJSONSchema, true
	case "event":
		return EventJSONSchema, true
	case "search-result-list", "search-results", "search-list", "search":
		return SearchResultListJSONSchema, true
	default:
		return "", false
	}
}
