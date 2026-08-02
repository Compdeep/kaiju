package docs

// A small, dependency-free markdown → HTML renderer for kaiju's reference docs.
// Kaiju links exactly one dependency-free binary; pulling in goldmark/blackfriday
// just to pretty-print docs is not worth the dependency surface, so this file
// hand-rolls the subset of markdown the docs actually use: ATX headings, fenced
// and inline code, bold/italic, links, ordered/unordered lists, GFM pipe tables,
// blockquotes, and horizontal rules. All text is HTML-escaped so raw < & " in the
// source can never break out of the page.

import (
	"fmt"
	"strings"
)

// RenderMarkdown converts a markdown document into a complete, styled HTML page
// matching the architecture doc's dark theme. title fills the <title> tag.
func RenderMarkdown(mdSource []byte, title string) []byte {
	body := renderMarkdownBody(string(mdSource))
	return []byte(markdownPage(title, body))
}

// docTitle derives a page title from the first H1 heading, falling back to the
// file's base name.
func docTitle(name string, src []byte) string {
	for _, line := range strings.Split(string(src), "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "# ") {
			h := strings.TrimSpace(strings.TrimRight(strings.TrimSpace(t[2:]), "#"))
			if h != "" {
				return "Kaiju · " + stripInlineMarkdown(h)
			}
		}
	}
	base := name
	if idx := strings.LastIndex(base, "/"); idx >= 0 {
		base = base[idx+1:]
	}
	return "Kaiju · " + strings.TrimSuffix(base, ".md")
}

// ── Block-level parsing ──────────────────────────────────────────────────────

func renderMarkdownBody(src string) string {
	src = strings.ReplaceAll(src, "\r\n", "\n")
	src = strings.ReplaceAll(src, "\r", "\n")
	var b strings.Builder
	renderBlockLines(&b, strings.Split(src, "\n"))
	return b.String()
}

func renderBlockLines(b *strings.Builder, lines []string) {
	i, n := 0, len(lines)
	for i < n {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		// Blank line — nothing to emit.
		if trimmed == "" {
			i++
			continue
		}

		// Fenced code block: ``` or ~~~ (optionally with a language info string).
		if isFence(trimmed) {
			fenceCh := trimmed[0]
			openLen := 0
			for openLen < len(trimmed) && trimmed[openLen] == fenceCh {
				openLen++
			}
			lang := strings.TrimSpace(trimmed[openLen:])
			if sp := strings.IndexAny(lang, " \t"); sp >= 0 {
				lang = lang[:sp]
			}
			i++
			var code []string
			for i < n {
				ct := strings.TrimSpace(lines[i])
				if len(ct) >= openLen && allChar(ct, fenceCh) {
					i++ // consume the closing fence
					break
				}
				code = append(code, lines[i])
				i++
			}
			writeCodeBlock(b, lang, strings.Join(code, "\n"))
			continue
		}

		// ATX heading.
		if level, text, ok := parseHeading(trimmed); ok {
			fmt.Fprintf(b, "<h%d id=\"%s\">%s</h%d>\n",
				level, escapeHTML(slugify(text)), renderInline(text), level)
			i++
			continue
		}

		// Horizontal rule.
		if isHR(trimmed) {
			b.WriteString("<hr>\n")
			i++
			continue
		}

		// GFM pipe table: a header row followed by a |---|---| separator row.
		if strings.Contains(line, "|") && i+1 < n && isTableSeparator(lines[i+1]) {
			header := splitTableRow(line)
			i += 2
			var rows [][]string
			for i < n && strings.Contains(lines[i], "|") && strings.TrimSpace(lines[i]) != "" {
				rows = append(rows, splitTableRow(lines[i]))
				i++
			}
			writeTable(b, header, rows)
			continue
		}

		// Blockquote — collect the run of '>' lines and render them recursively.
		if strings.HasPrefix(trimmed, ">") {
			var inner []string
			for i < n {
				ct := strings.TrimSpace(lines[i])
				if !strings.HasPrefix(ct, ">") {
					break
				}
				inner = append(inner, strings.TrimPrefix(strings.TrimPrefix(ct, ">"), " "))
				i++
			}
			b.WriteString("<blockquote>\n")
			renderBlockLines(b, inner)
			b.WriteString("</blockquote>\n")
			continue
		}

		// Lists.
		if isUnorderedItem(line) {
			i = renderList(b, lines, i, false)
			continue
		}
		if isOrderedItem(line) {
			i = renderList(b, lines, i, true)
			continue
		}

		// Paragraph — gather consecutive lines until a blank line or a new block.
		var para []string
		for i < n {
			cur := lines[i]
			ct := strings.TrimSpace(cur)
			if ct == "" || isFence(ct) || isHR(ct) || strings.HasPrefix(ct, ">") ||
				isUnorderedItem(cur) || isOrderedItem(cur) {
				break
			}
			if _, _, ok := parseHeading(ct); ok {
				break
			}
			if strings.Contains(cur, "|") && i+1 < n && isTableSeparator(lines[i+1]) {
				break
			}
			para = append(para, ct)
			i++
		}
		if len(para) > 0 {
			b.WriteString("<p>")
			b.WriteString(renderInline(strings.Join(para, " ")))
			b.WriteString("</p>\n")
		}
	}
}

func renderList(b *strings.Builder, lines []string, start int, ordered bool) int {
	tag := "ul"
	if ordered {
		tag = "ol"
	}
	b.WriteString("<" + tag + ">\n")
	i, n := start, len(lines)
	for i < n {
		line := lines[i]
		if strings.TrimSpace(line) == "" {
			break
		}
		if ordered && !isOrderedItem(line) {
			break
		}
		if !ordered && !isUnorderedItem(line) {
			break
		}
		parts := []string{listItemContent(line, ordered)}
		i++
		// Fold plain continuation lines (indented, not a new marker) into the item.
		for i < n {
			cur := lines[i]
			if strings.TrimSpace(cur) == "" || isUnorderedItem(cur) || isOrderedItem(cur) {
				break
			}
			parts = append(parts, strings.TrimSpace(cur))
			i++
		}
		b.WriteString("<li>")
		b.WriteString(renderInline(strings.Join(parts, " ")))
		b.WriteString("</li>\n")
	}
	b.WriteString("</" + tag + ">\n")
	return i
}

// ── Block helpers ────────────────────────────────────────────────────────────

func isFence(trimmed string) bool {
	return strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~")
}

func parseHeading(trimmed string) (int, string, bool) {
	level := 0
	for level < len(trimmed) && trimmed[level] == '#' {
		level++
	}
	if level < 1 || level > 6 || level >= len(trimmed) {
		return 0, "", false
	}
	if trimmed[level] != ' ' && trimmed[level] != '\t' {
		return 0, "", false
	}
	text := strings.TrimSpace(strings.TrimRight(strings.TrimSpace(trimmed[level:]), "#"))
	return level, text, true
}

func isHR(trimmed string) bool {
	s := strings.ReplaceAll(trimmed, " ", "")
	if len(s) < 3 {
		return false
	}
	c := s[0]
	if c != '-' && c != '*' && c != '_' {
		return false
	}
	return allChar(s, c)
}

func isTableSeparator(line string) bool {
	cells := splitTableRow(line)
	if len(cells) == 0 {
		return false
	}
	for _, cell := range cells {
		if !isSeparatorCell(cell) {
			return false
		}
	}
	return true
}

func isSeparatorCell(cell string) bool {
	cell = strings.TrimSpace(cell)
	cell = strings.TrimPrefix(cell, ":")
	cell = strings.TrimSuffix(cell, ":")
	if cell == "" {
		return false
	}
	return allChar(cell, '-')
}

func splitTableRow(line string) []string {
	s := strings.TrimSpace(line)
	s = strings.TrimSuffix(strings.TrimPrefix(s, "|"), "|")
	parts := strings.Split(s, "|")
	cells := make([]string, len(parts))
	for i, p := range parts {
		cells[i] = strings.TrimSpace(p)
	}
	return cells
}

func writeTable(b *strings.Builder, header []string, rows [][]string) {
	b.WriteString("<table>\n<thead>\n<tr>")
	for _, h := range header {
		b.WriteString("<th>" + renderInline(h) + "</th>")
	}
	b.WriteString("</tr>\n</thead>\n<tbody>\n")
	for _, row := range rows {
		b.WriteString("<tr>")
		for c := 0; c < len(header); c++ {
			cell := ""
			if c < len(row) {
				cell = row[c]
			}
			b.WriteString("<td>" + renderInline(cell) + "</td>")
		}
		b.WriteString("</tr>\n")
	}
	b.WriteString("</tbody>\n</table>\n")
}

func writeCodeBlock(b *strings.Builder, lang, code string) {
	b.WriteString("<pre><code")
	if lang != "" {
		b.WriteString(` class="language-` + escapeHTML(lang) + `"`)
	}
	b.WriteString(">" + escapeHTML(code) + "</code></pre>\n")
}

func leadingSpaces(s string) int {
	n := 0
	for n < len(s) && (s[n] == ' ' || s[n] == '\t') {
		n++
	}
	return n
}

func isUnorderedItem(line string) bool {
	s := line[leadingSpaces(line):]
	return len(s) >= 2 && (s[0] == '-' || s[0] == '*' || s[0] == '+') && (s[1] == ' ' || s[1] == '\t')
}

func isOrderedItem(line string) bool {
	s := line[leadingSpaces(line):]
	j := 0
	for j < len(s) && s[j] >= '0' && s[j] <= '9' {
		j++
	}
	if j == 0 || j >= len(s) {
		return false
	}
	if s[j] != '.' && s[j] != ')' {
		return false
	}
	return j+1 < len(s) && (s[j+1] == ' ' || s[j+1] == '\t')
}

func listItemContent(line string, ordered bool) string {
	s := line[leadingSpaces(line):]
	j := 0
	if ordered {
		for j < len(s) && s[j] >= '0' && s[j] <= '9' {
			j++
		}
		j++ // '.' or ')'
	} else {
		j = 1 // marker char
	}
	if j < len(s) && (s[j] == ' ' || s[j] == '\t') {
		j++
	}
	if j > len(s) {
		j = len(s)
	}
	return strings.TrimSpace(s[j:])
}

func allChar(s string, c byte) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] != c {
			return false
		}
	}
	return true
}

// ── Inline parsing ───────────────────────────────────────────────────────────

func renderInline(s string) string {
	var b strings.Builder
	i, n := 0, len(s)
	for i < n {
		c := s[i]
		switch c {
		case '`':
			j := i
			for j < n && s[j] == '`' {
				j++
			}
			ticks := j - i
			if end := findClosingTicks(s, j, ticks); end >= 0 {
				b.WriteString("<code>" + escapeHTML(s[j:end]) + "</code>")
				i = end + ticks
			} else {
				b.WriteString(strings.Repeat("`", ticks))
				i = j
			}
		case '[':
			if out, ni, ok := parseLink(s, i); ok {
				b.WriteString(out)
				i = ni
			} else {
				b.WriteByte('[')
				i++
			}
		case '*':
			if i+1 < n && s[i+1] == '*' {
				if inner, ni, ok := parseStars(s, i, "**"); ok {
					b.WriteString("<strong>" + renderInline(inner) + "</strong>")
					i = ni
					continue
				}
			}
			if inner, ni, ok := parseStars(s, i, "*"); ok {
				b.WriteString("<em>" + renderInline(inner) + "</em>")
				i = ni
			} else {
				b.WriteByte('*')
				i++
			}
		case '_':
			if out, ni, ok := parseUnderscore(s, i); ok {
				b.WriteString(out)
				i = ni
			} else {
				b.WriteByte('_')
				i++
			}
		case '<':
			b.WriteString("&lt;")
			i++
		case '>':
			b.WriteString("&gt;")
			i++
		case '&':
			b.WriteString("&amp;")
			i++
		case '"':
			b.WriteString("&#34;")
			i++
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

func findClosingTicks(s string, from, ticks int) int {
	i := from
	for i < len(s) {
		if s[i] == '`' {
			j := i
			for j < len(s) && s[j] == '`' {
				j++
			}
			if j-i == ticks {
				return i
			}
			i = j
		} else {
			i++
		}
	}
	return -1
}

// parseStars parses *emphasis* / **strong** starting at s[start]. It requires the
// content to be non-empty and not wrapped in spaces, so a stray "a * b" is left
// literal rather than turned into emphasis.
func parseStars(s string, start int, delim string) (string, int, bool) {
	openEnd := start + len(delim)
	if openEnd >= len(s) {
		return "", 0, false
	}
	idx := strings.Index(s[openEnd:], delim)
	if idx <= 0 {
		return "", 0, false
	}
	inner := s[openEnd : openEnd+idx]
	if inner[0] == ' ' || inner[len(inner)-1] == ' ' {
		return "", 0, false
	}
	return inner, openEnd + idx + len(delim), true
}

// parseUnderscore parses _emphasis_ / __strong__ with word-boundary flanking, so
// intraword underscores in identifiers (depends_on, task_files) stay literal.
func parseUnderscore(s string, i int) (string, int, bool) {
	if i > 0 && isWordByte(s[i-1]) {
		return "", 0, false
	}
	if i+1 < len(s) && s[i+1] == '_' {
		if inner, ni, ok := findUnderscoreClose(s, i+2, "__"); ok {
			return "<strong>" + renderInline(inner) + "</strong>", ni, true
		}
	}
	if inner, ni, ok := findUnderscoreClose(s, i+1, "_"); ok {
		return "<em>" + renderInline(inner) + "</em>", ni, true
	}
	return "", 0, false
}

func findUnderscoreClose(s string, from int, delim string) (string, int, bool) {
	if from >= len(s) || s[from] == ' ' {
		return "", 0, false
	}
	search := from
	for search < len(s) {
		idx := strings.Index(s[search:], delim)
		if idx < 0 {
			return "", 0, false
		}
		closePos := search + idx
		afterPos := closePos + len(delim)
		afterOK := afterPos >= len(s) || !isWordByte(s[afterPos])
		beforeOK := closePos > from && s[closePos-1] != ' '
		if afterOK && beforeOK {
			return s[from:closePos], afterPos, true
		}
		search = closePos + 1
	}
	return "", 0, false
}

func parseLink(s string, i int) (string, int, bool) {
	depth, textEnd := 0, -1
	for j := i; j < len(s); j++ {
		switch s[j] {
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				textEnd = j
			}
		}
		if textEnd >= 0 {
			break
		}
	}
	if textEnd < 0 || textEnd+1 >= len(s) || s[textEnd+1] != '(' {
		return "", 0, false
	}
	pdepth, urlEnd := 1, -1
	for k := textEnd + 2; k < len(s); k++ {
		switch s[k] {
		case '(':
			pdepth++
		case ')':
			pdepth--
			if pdepth == 0 {
				urlEnd = k
			}
		}
		if urlEnd >= 0 {
			break
		}
	}
	if urlEnd < 0 {
		return "", 0, false
	}
	text := s[i+1 : textEnd]
	dest := strings.TrimSpace(s[textEnd+2 : urlEnd])
	if sp := strings.IndexAny(dest, " \t"); sp >= 0 {
		dest = dest[:sp] // drop an optional "title"
	}
	return `<a href="` + escapeHTML(dest) + `">` + renderInline(text) + `</a>`, urlEnd + 1, true
}

func isWordByte(c byte) bool {
	return c == '_' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// ── Escaping / slugs ─────────────────────────────────────────────────────────

func escapeHTML(s string) string {
	if !strings.ContainsAny(s, "&<>\"") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&#34;")
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

func stripInlineMarkdown(s string) string {
	return strings.NewReplacer("`", "", "**", "", "*", "", "_", "", "[", "", "]", "").Replace(s)
}

// slugify matches GitHub's heading-anchor slugs (lowercase; keep [a-z0-9_-];
// spaces → '-'; drop everything else; no collapsing) so in-doc TOC links resolve.
func slugify(text string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(text) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteByte('-')
		}
	}
	return b.String()
}

// ── Page shell ───────────────────────────────────────────────────────────────

func markdownPage(title, body string) string {
	if title == "" {
		title = "Kaiju · Docs"
	}
	var b strings.Builder
	b.WriteString(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8" />
<meta name="viewport" content="width=device-width, initial-scale=1" />
<title>`)
	b.WriteString(escapeHTML(title))
	b.WriteString(`</title>
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=Exo+2:wght@400;600;700;800;900&family=JetBrains+Mono:wght@400;600&display=swap" rel="stylesheet">
<style>
`)
	b.WriteString(markdownCSS)
	b.WriteString(`
</style>
</head>
<body>
<div class="wrap">
<a class="backlink" href="/docs/architecture">← Architecture</a>
`)
	b.WriteString(body)
	b.WriteString(`</div>
</body>
</html>
`)
	return b.String()
}

const markdownCSS = `:root{
  --bg:#050506; --ink:#eaeaf1; --mut:#9a9aa8; --faint:#6a6a78;
  --blue:#38bdf8; --line:#1c1c22; --edge:#2b2b34;
  --code-bg:#0a0a0e; --code-ink:#a9def4; --raise:#0d0d11;
  --disp:'Exo 2',-apple-system,BlinkMacSystemFont,system-ui,sans-serif;
  --mono:'JetBrains Mono',ui-monospace,"SF Mono",Menlo,Consolas,monospace;
}
*{box-sizing:border-box}
html{scroll-behavior:smooth}
body{margin:0;background:
    radial-gradient(1200px 620px at 82% -8%, #0a2230 0%, transparent 58%),
    var(--bg);
  color:var(--ink);font:16px/1.7 var(--disp);-webkit-font-smoothing:antialiased}
.wrap{max-width:820px;margin:0 auto;padding:34px 26px 120px}
.backlink{display:inline-block;font-family:var(--mono);font-size:12.5px;color:var(--mut);
  border:1px solid var(--line);border-radius:8px;padding:5px 12px;margin-bottom:30px;
  transition:border-color .15s ease,color .15s ease}
.backlink:hover{color:var(--blue);border-color:var(--blue)}
a{color:var(--blue);text-decoration:none}
a:hover{text-decoration:underline}
h1,h2,h3,h4,h5,h6{color:#fff;font-weight:800;line-height:1.25;letter-spacing:-.4px;
  margin:1.8em 0 .6em;scroll-margin-top:20px}
h1{font-size:34px;font-weight:900;letter-spacing:-1px;margin:.2em 0 .6em;
  padding-bottom:.35em;border-bottom:1px solid var(--line)}
h2{font-size:26px;font-weight:900;padding-bottom:.3em;border-bottom:1px solid var(--line)}
h3{font-size:20px}
h4{font-size:16.5px}
h5{font-size:15px}
h6{font-size:13.5px;color:var(--mut)}
p{color:#c6c6d4;margin:0 0 1em}
strong,b{color:#fff;font-weight:700}
em,i{color:#e6e6f2;font-style:italic}
ul,ol{color:#c6c6d4;margin:0 0 1em;padding-left:1.6em}
li{margin:.3em 0}
li::marker{color:var(--blue)}
hr{border:0;height:1px;background:var(--line);margin:2.4em 0}
blockquote{margin:0 0 1.2em;padding:.5em 1.1em;color:var(--mut);
  border-left:3px solid var(--edge);background:var(--raise);border-radius:0 8px 8px 0}
blockquote p{margin:.4em 0;color:var(--mut)}
code{font-family:var(--mono);font-size:.85em;background:var(--code-bg);color:var(--code-ink);
  padding:1.5px 6px;border-radius:6px;border:1px solid var(--line)}
pre{background:var(--code-bg);border:1px solid var(--line);border-radius:12px;
  padding:16px 18px;overflow-x:auto;margin:0 0 1.4em;box-shadow:0 2px 10px rgba(0,0,0,.5)}
pre code{background:none;border:0;padding:0;font-size:13px;color:var(--code-ink);
  line-height:1.6;white-space:pre;display:block}
table{border-collapse:collapse;width:100%;margin:0 0 1.4em;font-size:14.5px;
  display:block;overflow-x:auto}
th,td{border:1px solid var(--line);padding:9px 14px;text-align:left;vertical-align:top}
thead th{background:var(--raise);color:#fff;font-weight:700}
tbody tr:nth-child(even){background:rgba(255,255,255,.02)}
tbody td{color:#c6c6d4}
img{max-width:100%}
`
