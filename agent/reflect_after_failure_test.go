package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// A page that refuses. 403 is not transient and not repairable — the site has
// declined, and no rewriting of the request changes that.
type forbiddenTool struct{ calls int }

func (f *forbiddenTool) Name() string              { return "web_fetch" }
func (f *forbiddenTool) Description() string       { return "fetch a page" }
func (f *forbiddenTool) Impact(map[string]any) int { return toolapi.ImpactObserve }
func (f *forbiddenTool) RequiresTarget() bool      { return false }
func (f *forbiddenTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"url":{"type":"string"}}}`)
}
func (f *forbiddenTool) Execute(ctx context.Context, p map[string]any) (string, error) {
	return toolapi.StringResult(f.ExecuteTyped(ctx, p))
}
func (f *forbiddenTool) ExecuteTyped(_ context.Context, p map[string]any) (toolapi.ToolMessage, error) {
	f.calls++
	url, _ := p["url"].(string)
	return toolapi.ToolFail("page", "HTTP 403 403 Forbidden — "+url, map[string]any{
		"status": "HTTP 403 403 Forbidden", "url": url,
	}), nil
}

// A run whose only fetch is refused must still reach the reflector.
//
// Observed live: a plan fetched one page, got 403, and the run went straight to
// synthesis — no reflect node, no reflector call in the model log, and the user
// told the answer could not be retrieved. The reflector is the stage that
// decides whether a refused source is the end of the search; a run that never
// reaches it has made that decision by omission, and the prompt telling it to
// try another source is never read.
func TestAReflectionFiresAfterTheOnlyStepIsRefused(t *testing.T) {
	tool := &forbiddenTool{}
	model := newStubModel(t, map[string]stubReply{
		"submit_preflight": {Args: map[string]any{"mode": "agent", "intent": "observe"}},
		"plan": plan(step("web_fetch", "fetch_tx_page", map[string]any{
			"url": "https://etherscan.io/",
		})),
		"submit_decision": {Args: map[string]any{"decision": "conclude", "outcome": "done"}},
	})
	a := agentWithCompute(t, model, tool)

	res, err := a.RunDAGSync(context.Background(), operateTrigger("the latest ethereum transaction"))
	if err != nil {
		t.Fatalf("the run failed: %v (stages called: %v)", err, model.functionsCalled())
	}

	called := model.functionsCalled()
	t.Logf("stages called: %v", called)
	t.Logf("tool calls: %d", tool.calls)
	for _, n := range traceNodes(t, res) {
		t.Logf("  node %v %v %v", n["type"], n["tag"], n["state"])
	}

	if model.callsTo("submit_decision") == 0 {
		t.Error("the reflector never ran, so nothing decided whether a refused source " +
			"was the end of the search — the run concluded by omission")
	}
}

// A model stage that IS refused still stops the run. Retrying every remaining
// node against a key the provider has rejected spends the whole budget to
// arrive at the same answer, and the answer is a configuration problem the user
// has to fix.
func TestAModelAuthFailureStillStopsTheRun(t *testing.T) {
	for _, tc := range []struct {
		name string
		node *Node
		want bool
	}{
		{"a tool's 403 belongs to the site it fetched", &Node{Type: NodeTool, ToolName: "web_fetch"}, false},
		{"an actuator's 403 belongs to what it acted on", &Node{Type: NodeActuator}, false},
		{"the planner's 401 is ours", &Node{Type: NodeExecutive}, true},
		{"the reflector's 401 is ours", &Node{Type: NodeReflection}, true},
		{"the debugger's 401 is ours", &Node{Type: NodeMicroPlanner}, true},
	} {
		if got := isModelStage(tc.node); got != tc.want {
			t.Errorf("%s: isModelStage = %v, want %v", tc.name, got, tc.want)
		}
	}
}
