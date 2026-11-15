package tools

import (
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

/*
 * nodeMarkdown writes the article readability found as markdown: headings,
 * paragraphs, list items, quotes and code blocks keep their breaks, and a link
 * keeps its address as [text](href). readability's TextContent drops both, so
 * a page's paragraphs ran together and none of its links reached the planner.
 * param: n - the article node (readability.Article.Node)
 * return: the article as markdown, trimmed
 */
func nodeMarkdown(n *html.Node) string {
	var b strings.Builder
	writeMarkdown(&b, n, false)
	lines := strings.Split(b.String(), "\n")
	out := make([]string, 0, len(lines))
	blank := false
	for _, l := range lines {
		l = strings.TrimRight(l, " \t")
		if strings.TrimSpace(l) == "" {
			blank = len(out) > 0
			continue
		}
		if blank {
			out = append(out, "")
			blank = false
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

// writeMarkdown writes n and its children to b. pre is set inside a code
// block, where whitespace is kept as it is.
func writeMarkdown(b *strings.Builder, n *html.Node, pre bool) {
	switch n.Type {
	case html.TextNode:
		if pre {
			b.WriteString(n.Data)
			return
		}
		t := strings.Join(strings.Fields(n.Data), " ")
		if t == "" {
			if n.Data != "" {
				b.WriteString(" ")
			}
			return
		}
		if n.Data[0] == ' ' || n.Data[0] == '\n' || n.Data[0] == '\t' {
			t = " " + t
		}
		if last := n.Data[len(n.Data)-1]; last == ' ' || last == '\n' || last == '\t' {
			t += " "
		}
		b.WriteString(t)
		return
	case html.ElementNode:
	default:
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			writeMarkdown(b, c, pre)
		}
		return
	}

	children := func() {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			writeMarkdown(b, c, pre)
		}
	}
	switch n.DataAtom {
	case atom.Script, atom.Style, atom.Noscript, atom.Template, atom.Svg, atom.Iframe, atom.Form, atom.Button:
		return
	case atom.Br:
		b.WriteString("\n")
	case atom.Hr:
		b.WriteString("\n\n---\n\n")
	case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
		b.WriteString("\n\n" + strings.Repeat("#", int(n.Data[1]-'0')) + " ")
		children()
		b.WriteString("\n\n")
	case atom.Li:
		b.WriteString("\n- ")
		children()
	case atom.Pre:
		b.WriteString("\n\n```\n")
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			writeMarkdown(b, c, true)
		}
		b.WriteString("\n```\n\n")
	case atom.Blockquote:
		var q strings.Builder
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			writeMarkdown(&q, c, pre)
		}
		b.WriteString("\n\n")
		for _, l := range strings.Split(strings.TrimSpace(q.String()), "\n") {
			b.WriteString("> " + strings.TrimSpace(l) + "\n")
		}
		b.WriteString("\n")
	case atom.A:
		var t strings.Builder
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			writeMarkdown(&t, c, pre)
		}
		text := strings.TrimSpace(t.String())
		href := strings.TrimSpace(attr(n, "href"))
		if text == "" || href == "" || strings.HasPrefix(href, "#") || strings.HasPrefix(strings.ToLower(href), "javascript:") {
			b.WriteString(t.String())
			return
		}
		inner := t.String()
		if strings.HasPrefix(inner, " ") {
			b.WriteString(" ")
		}
		b.WriteString("[" + text + "](" + strings.ReplaceAll(href, " ", "%20") + ")")
		if strings.HasSuffix(inner, " ") {
			b.WriteString(" ")
		}
	case atom.P, atom.Div, atom.Section, atom.Article, atom.Header, atom.Footer, atom.Main,
		atom.Ul, atom.Ol, atom.Table, atom.Tr, atom.Figure, atom.Figcaption, atom.Dl, atom.Dt, atom.Dd:
		b.WriteString("\n\n")
		children()
		b.WriteString("\n\n")
	case atom.Td, atom.Th:
		children()
		b.WriteString(" | ")
	default:
		children()
	}
}

// attr is n's attribute key, or "".
func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}
