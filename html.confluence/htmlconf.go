// Package htmlconf converts plain HTML into Confluence storage format (XHTML).
//
// Usage:
//
//	storage, err := htmlconf.Convert(htmlString)
package htmlconf

import (
	"fmt"
	"strings"

	"golang.org/x/net/html"
)

// Convert takes an HTML string and returns Confluence storage format XHTML.
func Convert(htmlInput string) (string, error) {
	doc, err := html.Parse(strings.NewReader(htmlInput))
	if err != nil {
		return "", fmt.Errorf("parsing html: %w", err)
	}

	var sb strings.Builder
	body := findBody(doc)
	if body != nil {
		for c := body.FirstChild; c != nil; c = c.NextSibling {
			renderNode(&sb, c)
		}
	} else {
		renderNode(&sb, doc)
	}

	return sb.String(), nil
}

func findBody(n *html.Node) *html.Node {
	if n.Type == html.ElementNode && n.Data == "body" {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if b := findBody(c); b != nil {
			return b
		}
	}
	return nil
}

func renderNode(sb *strings.Builder, n *html.Node) {
	switch n.Type {
	case html.DocumentNode:
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			renderNode(sb, c)
		}
	case html.TextNode:
		sb.WriteString(html.EscapeString(n.Data))
	case html.CommentNode:
		// dropped intentionally
	case html.ElementNode:
		renderElement(sb, n)
	default:
		renderChildren(sb, n)
	}
}

func renderChildren(sb *strings.Builder, n *html.Node) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		renderNode(sb, c)
	}
}

func renderElement(sb *strings.Builder, n *html.Node) {
	switch n.Data {

	// <pre><code class="language-go">...</code></pre> -> Confluence code macro
	case "pre":
		if codeNode := firstChildElement(n, "code"); codeNode != nil {
			renderCodeMacro(sb, codeNode)
			return
		}
		renderCodeMacroRaw(sb, "", textContent(n))
		return

	// <img src="..."> -> <ac:image>. Local (non-http) src is treated as an
	// already-uploaded page attachment filename; http(s) src becomes ri:url.
	case "img":
		src := attr(n, "src")
		alt := attr(n, "alt")
		sb.WriteString(`<ac:image`)
		if alt != "" {
			sb.WriteString(fmt.Sprintf(` ac:alt="%s"`, html.EscapeString(alt)))
		}
		sb.WriteString(`>`)
		if isExternalURL(src) {
			sb.WriteString(fmt.Sprintf(`<ri:url ri:value="%s" />`, html.EscapeString(src)))
		} else {
			sb.WriteString(fmt.Sprintf(`<ri:attachment ri:filename="%s" />`, html.EscapeString(src)))
		}
		sb.WriteString(`</ac:image>`)
		return

	// <blockquote> -> Confluence info panel macro
	case "blockquote":
		sb.WriteString(`<ac:structured-macro ac:name="info"><ac:rich-text-body>`)
		renderChildren(sb, n)
		sb.WriteString(`</ac:rich-text-body></ac:structured-macro>`)
		return

	case "a":
		href := attr(n, "href")
		sb.WriteString(fmt.Sprintf(`<a href="%s"`, html.EscapeString(href)))
		writeRemainingAttrs(sb, n, "href")
		sb.WriteString(`>`)
		renderChildren(sb, n)
		sb.WriteString(`</a>`)
		return

	// Inline code (not inside <pre>) -> plain <code> span
	case "code":
		sb.WriteString(`<code>`)
		renderChildren(sb, n)
		sb.WriteString(`</code>`)
		return

	// Elements that map directly, no transformation needed
	case "h1", "h2", "h3", "h4", "h5", "h6",
		"p", "ul", "ol", "li",
		"strong", "b", "em", "i", "u", "s", "del", "strike",
		"table", "thead", "tbody", "tr", "th", "td",
		"hr", "br":
		writeOpenTag(sb, n)
		if !isVoidElement(n.Data) {
			renderChildren(sb, n)
			sb.WriteString(fmt.Sprintf("</%s>", n.Data))
		}
		return

	// Anything unrecognized: pass through unchanged rather than drop it
	default:
		writeOpenTag(sb, n)
		renderChildren(sb, n)
		sb.WriteString(fmt.Sprintf("</%s>", n.Data))
		return
	}
}

func renderCodeMacro(sb *strings.Builder, codeNode *html.Node) {
	lang := ""
	if class := attr(codeNode, "class"); class != "" {
		lang = strings.TrimPrefix(class, "language-")
	}
	renderCodeMacroRaw(sb, lang, textContent(codeNode))
}

func renderCodeMacroRaw(sb *strings.Builder, lang, code string) {
	sb.WriteString(`<ac:structured-macro ac:name="code">`)
	if lang != "" {
		sb.WriteString(fmt.Sprintf(`<ac:parameter ac:name="language">%s</ac:parameter>`, html.EscapeString(mapLanguage(lang))))
	}
	sb.WriteString(`<ac:parameter ac:name="linenumbers">true</ac:parameter>`)
	sb.WriteString(`<ac:plain-text-body><![CDATA[`)
	sb.WriteString(strings.ReplaceAll(code, "]]>", "]]]]><![CDATA[>"))
	sb.WriteString(`]]></ac:plain-text-body>`)
	sb.WriteString(`</ac:structured-macro>`)
}

func mapLanguage(lang string) string {
	switch strings.ToLower(lang) {
	case "golang":
		return "go"
	case "sh", "shell", "zsh":
		return "bash"
	case "yml":
		return "yaml"
	case "":
		return "none"
	default:
		return strings.ToLower(lang)
	}
}

func firstChildElement(n *html.Node, tag string) *html.Node {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && c.Data == tag {
			return c
		}
	}
	return nil
}

func textContent(n *html.Node) string {
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			sb.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return sb.String()
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func writeOpenTag(sb *strings.Builder, n *html.Node) {
	sb.WriteString("<" + n.Data)
	for _, a := range n.Attr {
		sb.WriteString(fmt.Sprintf(` %s="%s"`, a.Key, html.EscapeString(a.Val)))
	}
	if isVoidElement(n.Data) {
		sb.WriteString(" />")
	} else {
		sb.WriteString(">")
	}
}

func writeRemainingAttrs(sb *strings.Builder, n *html.Node, exclude ...string) {
	skip := map[string]bool{}
	for _, e := range exclude {
		skip[e] = true
	}
	for _, a := range n.Attr {
		if !skip[a.Key] {
			sb.WriteString(fmt.Sprintf(` %s="%s"`, a.Key, html.EscapeString(a.Val)))
		}
	}
}

func isVoidElement(tag string) bool {
	switch tag {
	case "br", "hr", "img", "input", "meta", "link":
		return true
	default:
		return false
	}
}

func isExternalURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}
