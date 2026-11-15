package tools

import (
	"strings"
	"testing"

	"golang.org/x/net/html"
)

// The article keeps its paragraphs, headings, list items and links.
func TestNodeMarkdownKeepsTextAndLinks(t *testing.T) {
	doc, err := html.Parse(strings.NewReader(`<div><h2>Barycentre</h2><p>The Sun’s <a href="https://example.org/b">barycentre</a> moves.</p><p>Second   paragraph<script>x()</script></p><ul><li>one</li><li><a href="#top">two</a></li></ul></div>`))
	if err != nil {
		t.Fatal(err)
	}
	got := nodeMarkdown(doc)
	for _, want := range []string{
		"## Barycentre",
		"The Sun’s [barycentre](https://example.org/b) moves.",
		"moves.\n\nSecond paragraph",
		"- one",
		"- two",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "x()") || strings.Contains(got, "#top") {
		t.Errorf("script text or an in-page anchor came through:\n%s", got)
	}
}
