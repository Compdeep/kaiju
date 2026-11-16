package agent

import (
	"strings"
	"testing"
)

// The reflector's prompt puts what stays the same from round to round first,
// so a provider can read it from its cache, and what changes after: the brief,
// the counts, this round's results and the failures.
func TestReflectorPromptPutsStableSectionsFirst(t *testing.T) {
	returns := "### Resolved\n\n**now:**\nRESULT_NOW\n\n" +
		earlierRoundsSubheading + "**old:**\nRESULT_OLD\n\n" +
		"### Failed\n\n- **bad** (probe): boom\n"
	g := NewGraph()
	resolvedToolNode(g, "x", "y")
	gate := &ContextResponse{Sources: map[string]string{SourceNodeReturns: returns}}
	brief := reframeHeading + "\n\nWHERE WE ARE: ..."
	p := (&Agent{}).reflectorPrompt(g, gate, Trigger{Type: "chat_query", Data: []byte(`{"query":"q"}`)}, "round 2 of 3", brief)

	order := []string{"## Original Request", earlierRoundsHeading, "**old:**", "## History", reframeHeading, "## Graph Summary", "## Node Results\n", "**now:**", "### Failed"}
	last := -1
	for _, h := range order {
		i := strings.Index(p, h)
		if i < 0 {
			t.Fatalf("%q missing from the prompt:\n%s", h, p)
		}
		if i < last {
			t.Fatalf("%q is out of order:\n%s", h, p)
		}
		last = i
	}
	if strings.Count(p, "RESULT_OLD") != 1 || strings.Count(p, "RESULT_NOW") != 1 {
		t.Errorf("a result appears twice or not at all:\n%s", p)
	}
	if strings.Contains(p, "### Resolved in earlier rounds") {
		t.Errorf("the earlier rounds kept their old subheading inside Node Results:\n%s", p)
	}
}

func TestSplitEarlierRoundsWithoutAny(t *testing.T) {
	earlier, rest := splitEarlierRounds("### Resolved\n\n**a:**\nb\n")
	if earlier != "" || rest != "### Resolved\n\n**a:**\nb\n" {
		t.Errorf("earlier=%q rest=%q", earlier, rest)
	}
	earlier, rest = splitEarlierRounds(earlierRoundsSubheading + "**old:**\nx\n")
	if earlier != "**old:**\nx" || rest != "" {
		t.Errorf("only earlier: earlier=%q rest=%q", earlier, rest)
	}
}
