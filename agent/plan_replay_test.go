package agent

import (
	"encoding/json"
	"testing"

	"github.com/Compdeep/kaiju/agent/llm"
)

// A call sent back to ask for a fix carries arguments a host can parse; a host
// that parses them refuses the whole request otherwise, and the fix is never
// asked for.
func TestReplayedCallsCarryJSON(t *testing.T) {
	calls := []llm.ToolCall{
		{ID: "a", Function: llm.FunctionCall{Name: "plan", Arguments: "<tool_call>{oops"}},
		{ID: "b", Function: llm.FunctionCall{Name: "plan", Arguments: `{"steps":[]}`}},
	}
	got := replayable(calls)
	for _, c := range got {
		if !json.Valid([]byte(c.Function.Arguments)) {
			t.Errorf("call %s replays %q", c.ID, c.Function.Arguments)
		}
	}
	if got[1].Function.Arguments != `{"steps":[]}` {
		t.Errorf("valid arguments changed: %q", got[1].Function.Arguments)
	}
	if calls[0].Function.Arguments != "<tool_call>{oops" {
		t.Error("the reply itself was changed")
	}
}
