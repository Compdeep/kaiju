package tools

import (
	"strings"
	"testing"
)

// go-readability drops tables, and a page's figures are often only in one:
// of Wikipedia's "Barycenter (astronomy)" it kept no table, and the Sun–Jupiter
// distance, 742,370 km, was in one. droppedTables puts data tables back, and
// leaves out navigation boxes and tables the article already holds.

const tablesPage = `<html><body><article><p>The barycenter of the Sun and Jupiter lies just outside the Sun.</p></article>
<table class="wikitable"><caption>Sun and planet barycenters</caption>
<tr><th>Body</th><th>a (km)</th><th>r1 (km)</th></tr>
<tr><td>Jupiter</td><td>778,000,000</td><td>742,370<sup>[5]</sup></td></tr>
</table>
<table class="navbox"><tr><td>Mercury</td><td>Venus</td></tr><tr><td>Earth</td><td>Mars</td></tr></table>
<table><tr><td>one row only</td><td>x</td></tr></table>
</body></html>`

func TestDroppedTables_PutsBackADataTable(t *testing.T) {
	got := droppedTables(tablesPage, "The barycenter of the Sun and Jupiter lies just outside the Sun.", 10000)
	for _, want := range []string{"Sun and planet barycenters", "| Body | a (km) | r1 (km) |", "| Jupiter | 778,000,000 | 742,370 |"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "[5]") {
		t.Errorf("a footnote marker was kept in a value:\n%s", got)
	}
	if strings.Contains(got, "Mercury") {
		t.Errorf("a navigation box was put back as data:\n%s", got)
	}
	if strings.Contains(got, "one row only") {
		t.Errorf("a one-row table was put back:\n%s", got)
	}
}

func TestDroppedTables_SkipsWhatTheArticleHas(t *testing.T) {
	article := "Sun and planet barycenters Body a (km) r1 (km) Jupiter 778,000,000 742,370"
	if got := droppedTables(tablesPage, article, 10000); strings.Contains(got, "742,370") {
		t.Errorf("a table the article already holds was added again:\n%s", got)
	}
}

func TestDroppedTables_Bounded(t *testing.T) {
	if got := droppedTables(tablesPage, "", 10); got != "" {
		t.Errorf("a table larger than the limit was returned: %q", got)
	}
	if got := droppedTables("<p>no tables</p>", "", 10000); got != "" {
		t.Errorf("a page with no table returned %q", got)
	}
}
