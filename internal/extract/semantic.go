package extract

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/kaiizer-99/onyx-scrapper/internal/llm"
	"golang.org/x/net/html"
)

const MaxDOMCharLimit = 40000

// Finder wraps an LLM client to locate DOM elements based on natural language descriptions.
type Finder struct {
	client *llm.Client
}

// NewFinder constructs a new Finder instance.
func NewFinder(client *llm.Client) *Finder {
	return &Finder{client: client}
}

// FindElement locates an element described by description within the raw HTML string using MiMo.
func (f *Finder) FindElement(rawHTML string, description string) (string, error) {
	return FindElement(f.client, rawHTML, description)
}

// FindElement sends simplified DOM and description to MiMo to extract a CSS selector or XPath.
func FindElement(client *llm.Client, rawHTML string, description string) (string, error) {
	if client == nil {
		return "", fmt.Errorf("llm client is required")
	}
	if strings.TrimSpace(description) == "" {
		return "", fmt.Errorf("description cannot be empty")
	}

	simplifiedDOM, err := SimplifyDOM(rawHTML)
	if err != nil {
		return "", fmt.Errorf("failed to simplify DOM: %w", err)
	}

	prompt := fmt.Sprintf(`You are an expert web scraping and DOM analysis assistant.
Given a simplified HTML snippet and a target element description in plain English, return the precise CSS selector or XPath to locate that element on the web page.

RULES:
1. Respond with ONLY the selector or XPath string.
2. Do NOT enclose the response in markdown code blocks, quotes, or commentary.
3. Prefer concise and unique CSS selectors (e.g. #search-input, input[name='q'], button.submit-btn, a[href*='login']).
4. If a CSS selector cannot uniquely match the element, use an XPath (e.g. //button[contains(text(),'Submit')]).

Target Description: %s

Simplified DOM:
%s`, description, simplifiedDOM)

	messages := []llm.Message{
		{Role: "user", Content: prompt},
	}

	rawResp, err := client.Chat(messages)
	if err != nil {
		return "", fmt.Errorf("llm chat error: %w", err)
	}

	selector := CleanSelectorResponse(rawResp)
	if selector == "" {
		return "", fmt.Errorf("model returned empty selector")
	}

	return selector, nil
}

// SimplifyDOM cleans HTML by removing non-structural/heavy tags and keeping key attributes for selector generation.
func SimplifyDOM(rawHTML string) (string, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(rawHTML))
	if err != nil {
		return "", fmt.Errorf("failed to parse HTML: %w", err)
	}

	// Remove heavy/non-interactive tags
	doc.Find("script, style, noscript, iframe, svg, canvas, meta, link, head").Each(func(_ int, s *goquery.Selection) {
		s.Remove()
	})

	var sb strings.Builder
	body := doc.Find("body")
	if body.Length() == 0 {
		body = doc.Selection
	}

	if len(body.Nodes) == 0 {
		return "", fmt.Errorf("empty HTML document")
	}

	renderSimplifiedNode(body.Nodes[0], &sb)

	result := sb.String()

	// Clean up multi-line whitespace
	reSpaces := regexp.MustCompile(`[ \t]+`)
	reNewlines := regexp.MustCompile(`\n{3,}`)
	result = reSpaces.ReplaceAllString(result, " ")
	result = reNewlines.ReplaceAllString(result, "\n\n")
	result = strings.TrimSpace(result)

	// Token budget guard
	if len(result) > MaxDOMCharLimit {
		result = result[:MaxDOMCharLimit] + "\n... [DOM Truncated for Token Budget]"
	}

	return result, nil
}

func renderSimplifiedNode(n *html.Node, sb *strings.Builder) {
	if n == nil {
		return
	}

	switch n.Type {
	case html.TextNode:
		text := strings.TrimSpace(n.Data)
		if text != "" {
			if len(text) > 100 {
				text = text[:100] + "..."
			}
			sb.WriteString(text)
		}

	case html.ElementNode:
		tag := strings.ToLower(n.Data)

		sb.WriteString("<" + tag)

		// Retain selector-relevant attributes
		for _, attr := range n.Attr {
			key := strings.ToLower(attr.Key)
			val := attr.Val

			// Skip heavy or irrelevant attributes
			if key == "style" || strings.HasPrefix(key, "on") {
				continue
			}

			// Truncate data URIs
			if (key == "src" || key == "href") && strings.HasPrefix(val, "data:") {
				val = "[data-uri]"
			}

			if isRelevantAttribute(key) {
				if len(val) > 120 {
					val = val[:120] + "..."
				}
				sb.WriteString(fmt.Sprintf(` %s="%s"`, key, html.EscapeString(val)))
			}
		}

		// Self-closing elements
		if isSelfClosing(tag) {
			sb.WriteString(" />\n")
			return
		}

		sb.WriteString(">")

		for c := n.FirstChild; c != nil; c = c.NextSibling {
			renderSimplifiedNode(c, sb)
		}

		sb.WriteString("</" + tag + ">\n")

	case html.DocumentNode:
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			renderSimplifiedNode(c, sb)
		}
	}
}

func isRelevantAttribute(attr string) bool {
	if strings.HasPrefix(attr, "data-") || strings.HasPrefix(attr, "aria-") {
		return true
	}
	switch attr {
	case "id", "class", "name", "type", "placeholder", "role", "href", "title", "alt", "value", "for", "action", "method", "target":
		return true
	default:
		return false
	}
}

func isSelfClosing(tag string) bool {
	switch tag {
	case "area", "base", "br", "col", "embed", "hr", "img", "input", "link", "meta", "param", "source", "track", "wbr":
		return true
	default:
		return false
	}
}

// CleanSelectorResponse strips markdown formatting and quotes from raw LLM output.
func CleanSelectorResponse(resp string) string {
	s := strings.TrimSpace(resp)
	// Remove code fences if LLM included them despite prompt instructions
	s = regexp.MustCompile("^```(?:css|xpath)?\\s*").ReplaceAllString(s, "")
	s = regexp.MustCompile("\\s*```$").ReplaceAllString(s, "")
	s = strings.TrimSpace(s)
	// Remove surrounding quotes if present
	if (strings.HasPrefix(s, `"`) && strings.HasSuffix(s, `"`)) ||
		(strings.HasPrefix(s, `'`) && strings.HasSuffix(s, `'`)) ||
		(strings.HasPrefix(s, "`") && strings.HasSuffix(s, "`")) {
		s = s[1 : len(s)-1]
	}
	return strings.TrimSpace(s)
}
