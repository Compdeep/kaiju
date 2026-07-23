package agent

import (
	"encoding/json"
	"testing"

	"github.com/Compdeep/kaiju/internal/agent/tools"
)

// TestUnknownToolNames covers the pre-execution existence check that decides
// whether to re-plan: only step tools absent from the registry are returned,
// distinct, in order, with "gap" pseudo-steps and blanks ignored.
func TestUnknownToolNames(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Replace(&fakeTool{name: "web_search", params: json.RawMessage(`{}`)}, "builtin")
	reg.Replace(&fakeTool{name: "web_fetch", params: json.RawMessage(`{}`)}, "builtin")
	a := &Agent{registry: reg}

	cases := []struct {
		name  string
		steps []PlanStep
		want  []string
	}{
		{"all real", []PlanStep{{Tool: "web_search"}, {Tool: "web_fetch"}}, nil},
		{"one fake (skill-as-tool)", []PlanStep{{Tool: "web_research_guide"}, {Tool: "web_search"}}, []string{"web_research_guide"}},
		{"distinct + ordered", []PlanStep{{Tool: "foo"}, {Tool: "web_search"}, {Tool: "bar"}, {Tool: "foo"}}, []string{"foo", "bar"}},
		{"gap and blank ignored", []PlanStep{{Tool: "gap", Gap: "no tool"}, {Tool: ""}, {Tool: "web_fetch"}}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := a.unknownToolNames(c.steps)
			if len(got) != len(c.want) {
				t.Fatalf("got %v, want %v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("got %v, want %v", got, c.want)
				}
			}
		})
	}
}

// TestQuoteList checks the singular/plural verb agreement in the correction text.
func TestQuoteList(t *testing.T) {
	if got := quoteList([]string{"web_research_guide"}); got != `"web_research_guide" is` {
		t.Errorf("single: got %q", got)
	}
	if got := quoteList([]string{"a", "b"}); got != `"a", "b" are` {
		t.Errorf("plural: got %q", got)
	}
}
