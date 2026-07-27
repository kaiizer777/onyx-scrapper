package extract

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/net/html"
)

// CleanHTML parses raw HTML string and returns clean, structured Markdown-like text.
func CleanHTML(raw string) (string, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(raw))
	if err != nil {
		return "", fmt.Errorf("failed to parse HTML: %w", err)
	}

	// Remove non-content / boilerplate tags
	doc.Find("script, style, noscript, iframe, svg, canvas, header, footer, nav, aside, form, button, input, select, textarea").Each(func(_ int, s *goquery.Selection) {
		s.Remove()
	})

	// Focus on main content container if available, otherwise root/body
	mainSel := doc.Find("main, article, #content, .content, body").First()
	if mainSel.Length() == 0 {
		mainSel = doc.Selection
	}

	if len(mainSel.Nodes) == 0 {
		return "", fmt.Errorf("empty document body")
	}

	var sb strings.Builder
	renderNode(mainSel.Nodes[0], &sb, false)

	text := sanitizeText(sb.String())
	return text, nil
}

func renderNode(n *html.Node, sb *strings.Builder, inCode bool) {
	if n == nil {
		return
	}

	switch n.Type {
	case html.TextNode:
		t := n.Data
		if !inCode {
			t = regexp.MustCompile(`[ \t\r\n]+`).ReplaceAllString(t, " ")
		}
		sb.WriteString(t)

	case html.ElementNode:
		tag := strings.ToLower(n.Data)
		switch tag {
		case "h1", "h2", "h3", "h4", "h5", "h6":
			level := int(tag[1] - '0')
			sb.WriteString("\n\n" + strings.Repeat("#", level) + " ")
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				renderNode(c, sb, inCode)
			}
			sb.WriteString("\n\n")

		case "p":
			sb.WriteString("\n\n")
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				renderNode(c, sb, inCode)
			}
			sb.WriteString("\n\n")

		case "br":
			sb.WriteString("\n")

		case "a":
			href := ""
			for _, attr := range n.Attr {
				if attr.Key == "href" {
					href = attr.Val
					break
				}
			}
			var anchorText strings.Builder
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				renderNode(c, &anchorText, inCode)
			}
			linkStr := strings.TrimSpace(anchorText.String())
			if href != "" && linkStr != "" && !strings.HasPrefix(href, "javascript:") && !strings.HasPrefix(href, "#") {
				sb.WriteString(fmt.Sprintf(" [%s](%s) ", linkStr, href))
			} else if linkStr != "" {
				sb.WriteString(linkStr)
			}

		case "ul", "ol":
			sb.WriteString("\n\n")
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				renderNode(c, sb, inCode)
			}
			sb.WriteString("\n\n")

		case "li":
			sb.WriteString("\n- ")
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				renderNode(c, sb, inCode)
			}
			sb.WriteString("\n")

		case "pre":
			sb.WriteString("\n\n```\n")
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				renderNode(c, sb, true)
			}
			sb.WriteString("\n```\n\n")

		case "code":
			if !inCode {
				sb.WriteString("`")
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					renderNode(c, sb, true)
				}
				sb.WriteString("`")
			} else {
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					renderNode(c, sb, true)
				}
			}

		case "blockquote":
			sb.WriteString("\n\n> ")
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				renderNode(c, sb, inCode)
			}
			sb.WriteString("\n\n")

		default:
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				renderNode(c, sb, inCode)
			}
		}
	case html.DocumentNode:
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			renderNode(c, sb, inCode)
		}
	}
}

func sanitizeText(text string) string {
	lines := strings.Split(text, "\n")
	var cleaned []string
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if trimmed != "" || (len(cleaned) > 0 && cleaned[len(cleaned)-1] != "") {
			cleaned = append(cleaned, trimmed)
		}
	}
	result := strings.Join(cleaned, "\n")
	reMultiNewline := regexp.MustCompile(`\n{3,}`)
	result = reMultiNewline.ReplaceAllString(result, "\n\n")
	return strings.TrimSpace(result)
}
