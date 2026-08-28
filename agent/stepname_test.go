package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/llm"
)

// A step's name has to travel with its result.
//
// The two protocol fields that could have carried it are spoken for: a tool
// message's name must be the tool that ran, and the call id pairs it with the
// assistant turn. So a run that fetched twice presented two web_fetch results
// with nothing saying which was which, and the names lived only in the worklog —
// truncated, and thousands of characters away.
//
// This is what a planner reads when it is told to take a finished step's value.
func TestStepResultContent_CarriesTheStepName(t *testing.T) {
	got := stepResultContent(StepResult{
		NodeID: "n3", Tool: "web_fetch", Name: "fetch_tam",
		Payload: json.RawMessage(`{"total_market_value":"$8,391.1 million"}`),
	})
	var obj map[string]any
	if err := json.Unmarshal([]byte(got), &obj); err != nil {
		t.Fatalf("the result is no longer JSON: %v (%q)", err, got)
	}
	if obj["step"] != "fetch_tam" {
		t.Errorf("the step's name is not in its result, so nothing pairs the value with the name a plan uses: %q", got)
	}
	// The fields must stay where every reader already looks for them.
	if obj["total_market_value"] != "$8,391.1 million" {
		t.Errorf("the payload's fields moved; a wrapper would break every reader at once: %q", got)
	}
}

// A failure is a result too, and the stage reading it needs to know which step
// failed as much as it needs the error.
func TestStepResultContent_NamesTheStepThatFailed(t *testing.T) {
	got := stepResultContent(StepResult{
		NodeID: "n4", Tool: "web_fetch", Name: "fetch_server_count",
		Err: "HTTP 403: forbidden",
	})
	var obj map[string]any
	if err := json.Unmarshal([]byte(got), &obj); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if obj["step"] != "fetch_server_count" || obj["error"] != "HTTP 403: forbidden" {
		t.Errorf("a failure no longer says which step it was: %q", got)
	}
}

// A tool that returned only text still gets its name attached.
func TestStepResultContent_NamesATextOnlyResult(t *testing.T) {
	got := stepResultContent(StepResult{Tool: "bash", Name: "list_files", Content: "a.txt\nb.txt"})
	var obj map[string]any
	if err := json.Unmarshal([]byte(got), &obj); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if obj["step"] != "list_files" || obj["content"] != "a.txt\nb.txt" {
		t.Errorf("text result lost its name or its text: %q", got)
	}
}

// A payload that is not an object has nowhere to put the name that would not
// change the shape its reader expects. Leave it alone.
func TestStepResultContent_LeavesANonObjectPayloadAlone(t *testing.T) {
	const arr = `[{"url":"https://example.com"}]`
	if got := stepResultContent(StepResult{Name: "search", Payload: json.RawMessage(arr)}); got != arr {
		t.Errorf("an array payload was reshaped: %q", got)
	}
}

// A tool that declares its own "step" field owns that name. Shadowing it would
// hand the reader the planner's name where the tool's value belongs.
func TestStepResultContent_DoesNotShadowAToolsOwnStepField(t *testing.T) {
	const own = `{"step":"3 of 7","status":"running"}`
	got := stepResultContent(StepResult{Name: "deploy", Payload: json.RawMessage(own)})
	var obj map[string]any
	_ = json.Unmarshal([]byte(got), &obj)
	if obj["step"] != "3 of 7" {
		t.Errorf("the tool's own step field was overwritten: %q", got)
	}
}

// End to end: two calls to the SAME tool in one arc must be tellable apart by
// the stage reading them. This is the case that produced the bug — two
// web_fetch results, one of them the one a later plan needed.
func TestBuildMessagesWithResults_TellsTwoCallsOfOneToolApart(t *testing.T) {
	arcs := [][]StepResult{{
		{NodeID: "n3", Tool: "web_fetch", Name: "fetch_tam",
			Payload: json.RawMessage(`{"value":"$8,391.1 million"}`)},
		{NodeID: "n4", Tool: "web_fetch", Name: "fetch_server_count",
			Payload: json.RawMessage(`{"value":"1.2 million servers"}`)},
	}}
	msgs := BuildMessagesWithResults("sys", "objective", nil, arcs)

	var tools []llm.Message
	for _, m := range msgs {
		if m.Role == "tool" {
			tools = append(tools, m)
		}
	}
	if len(tools) != 2 {
		t.Fatalf("got %d tool messages, want 2", len(tools))
	}
	for _, want := range []string{"fetch_tam", "fetch_server_count"} {
		found := false
		for _, m := range tools {
			if strings.Contains(m.Content, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("%q appears in no tool result; two web_fetch results are indistinguishable to the stage reading them", want)
		}
	}
}
