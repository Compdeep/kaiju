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

/*
 * droppedTables writes the page's data tables that the article left out.
 * desc: go-readability scores a table as boilerplate and removes it, so every
 *       figure a page keeps in a table was lost before any model read the page:
 *       of Wikipedia's "Barycenter (astronomy)" (200 KB, 3 tables) it kept
 *       9,225 characters and no table, and the Sun–Jupiter distance, 742,370 km,
 *       is in one of them. This reads the tables from the raw page and returns
 *       those whose text the article does not already hold, as rows of cells.
 *
 *       Navigation boxes and side bars are tables too, and carry links rather
 *       than data, so a table marked as one is skipped, as is a table of fewer
 *       than two rows.
 * param: body - the raw page.
 * param: kept - the article text already extracted.
 * param: limit - the most characters to return.
 * return: the tables, each a block of "| a | b |" rows, or "".
 */
func droppedTables(body, kept string, limit int) string {
	if limit <= 0 || !strings.Contains(body, "<table") {
		return ""
	}
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		return ""
	}
	var out strings.Builder
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if out.Len() >= limit {
			return
		}
		if n.Type == html.ElementNode && n.DataAtom == atom.Table {
			if t := tableText(n); t != "" && !tableKept(t, kept) {
				if out.Len()+len(t) > limit {
					return
				}
				out.WriteString(t)
				out.WriteString("\n\n")
			}
			return // a table inside a table is written with its parent
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return strings.TrimSpace(out.String())
}

// skippedTableClass names the tables that hold navigation, not data.
var skippedTableClass = []string{"navbox", "sidebar", "metadata", "ambox", "mbox", "toc"}

// tableText renders one table as a caption line and "| a | b |" rows, or ""
// when it is navigation or too small to be data.
func tableText(t *html.Node) string {
	class := strings.ToLower(attr(t, "class"))
	for _, c := range skippedTableClass {
		if strings.Contains(class, c) {
			return ""
		}
	}
	var caption string
	var rows []string
	wide := false
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.DataAtom {
			case atom.Caption:
				caption = cellText(n)
				return
			case atom.Tr:
				var cells []string
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					if c.Type == html.ElementNode && (c.DataAtom == atom.Td || c.DataAtom == atom.Th) {
						cells = append(cells, cellText(c))
					}
				}
				if len(cells) > 1 {
					wide = true
				}
				if strings.TrimSpace(strings.Join(cells, "")) != "" {
					rows = append(rows, "| "+strings.Join(cells, " | ")+" |")
				}
				return
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(t)
	if len(rows) < 2 || !wide {
		return ""
	}
	if caption != "" {
		return caption + "\n" + strings.Join(rows, "\n")
	}
	return strings.Join(rows, "\n")
}

// cellText is a cell's text on one line, links and markup dropped.
func cellText(n *html.Node) string {
	var b strings.Builder
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
			b.WriteString(" ")
			return
		}
		if n.Type == html.ElementNode {
			switch n.DataAtom {
			case atom.Script, atom.Style, atom.Sup:
				// A footnote marker ("[5]") is not part of the value it follows.
				return
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(b.String()), " ")
}

// tableKept reports whether the article already holds this table: most of its
// longer cells appear in the article text.
func tableKept(table, kept string) bool {
	flat := strings.Join(strings.Fields(kept), " ")
	var long, found int
	for _, row := range strings.Split(table, "\n") {
		for _, cell := range strings.Split(row, "|") {
			cell = strings.TrimSpace(cell)
			if len(cell) < 4 {
				continue
			}
			long++
			if strings.Contains(flat, cell) {
				found++
			}
		}
	}
	return long == 0 || found*2 > long
}
