package tools

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// loggedSearch is a Firecrawl reply as run f8e43d3a (2026-11-16) received it:
// "Tokyo weather forecast Friday November 20 2026", five results, the first an
// AccuWeather excerpt of 5,181 characters.
func loggedSearch(t *testing.T) []searchResult {
	t.Helper()
	raw, err := os.ReadFile("testdata/firecrawl_tokyo_friday.json")
	if err != nil {
		t.Fatal(err)
	}
	var reply struct {
		Results []searchResult `json:"results"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		t.Fatal(err)
	}
	return reply.Results
}

func TestPlainSnippetsFitTheStages(t *testing.T) {
	results := loggedSearch(t)
	plainSnippets(results)
	all, _ := json.Marshal(map[string]any{"query": "q", "results": results})
	// The agent's payload caps: 4,000 a value, 12,000 the whole.
	if len(all) > 12000 {
		t.Errorf("results are %d characters, over the 12,000 a stage takes whole", len(all))
	}
	for i, r := range results {
		if n := len([]rune(r.Snippet)); n > snippetMaxChars {
			t.Errorf("snippet %d is %d characters", i, n)
		}
		for _, mark := range []string{"](http", "![", "\\\n"} {
			if strings.Contains(r.Snippet, mark) {
				t.Errorf("snippet %d still holds %q:\n%s", i, mark, r.Snippet)
			}
		}
	}
	// The japan.travel table is the result that held the day asked about.
	var table string
	for _, r := range results {
		if strings.Contains(r.URL, "japan.travel") {
			table = r.Snippet
		}
	}
	if !strings.Contains(table, "20 Nov (Friday)") || !strings.Contains(table, "Clear | 15° | 5° | 10%") {
		t.Errorf("Friday's row is gone from the japan.travel snippet:\n%s", table)
	}
}

func TestPlainSnippetKeepsWords(t *testing.T) {
	in := "[**Tokyo, Tokyo** 55°F![](https://x/12.svg)](https://x/forecast)\n\n\n\nChevron   down\\\n![Cloudy and cooler](https://x/7.svg) 57°"
	want := "**Tokyo, Tokyo** 55°F\nChevron down\nCloudy and cooler 57°"
	if got := plainSnippet(in); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCutSnippetEndsAtASpace(t *testing.T) {
	got := cutSnippet(strings.Repeat("word ", 100), 52)
	if !strings.HasSuffix(got, "word…") || len([]rune(got)) > 52 {
		t.Errorf("got %q", got)
	}
	if got := cutSnippet("short", 52); got != "short" {
		t.Errorf("a short snippet changed: %q", got)
	}
}
