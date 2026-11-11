package agent

import (
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/llm"
)

// The trace holds the whole request: the earlier steps a planner is shown as
// calls and their results sit between the system and user prompts, and a
// re-plan read without them looks like a prompt with nothing unusual in it.
func TestTheTraceShowsTheTurnsBetweenThePrompts(t *testing.T) {
	var tr LLMTrace
	traceMessages(&tr, []llm.Message{
		{Role: "system", Content: "plan things"},
		{Role: "assistant", ToolCalls: []llm.ToolCall{{Function: llm.FunctionCall{Name: "web_research", Arguments: `{"query":"barycenter"}`}}}},
		{Role: "tool", Name: "web_research", Content: "Source 1 — Barycenter"},
		{Role: "user", Content: "re-plan"},
	})
	got := tr.formatPrompts()
	for _, want := range []string{
		"--- SYSTEM PROMPT ---\nplan things",
		"--- TURNS ---",
		`[assistant] web_research({"query":"barycenter"})`,
		"[tool web_research] Source 1 — Barycenter",
		"--- USER PROMPT ---\nre-plan",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("%q missing from:\n%s", want, got)
		}
	}
	if strings.Index(got, "--- TURNS ---") > strings.Index(got, "--- USER PROMPT ---") {
		t.Error("the turns are written after the user prompt")
	}
}

// The output names the call it came back as, so a call to the wrong tool is
// seen as one.
func TestTheTraceNamesTheCall(t *testing.T) {
	out := LLMTrace{Call: "web_fetch", Output: `{"url":"x"}`}.formatOutput()
	if !strings.Contains(out, "--- OUTPUT ---\ncall: web_fetch\n") {
		t.Errorf("call not named:\n%s", out)
	}
}
