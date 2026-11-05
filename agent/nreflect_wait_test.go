package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// A reflection that concludes while a step is still running waits for it.
//
// In nReflect a reflection fires once BatchSize steps have finished, whether or
// not others are still running. An investigation that planned six local queries
// and two web searches reflected on the six within a second, concluded, and the
// run ended with both searches still running: the one result that could have
// answered the question was never read, and the reflection said every query had
// returned. Seen live, 4 November 2026.

// lateTool answers, but after the quick steps have all finished.
type lateTool struct{ name string }

func (l *lateTool) Name() string                { return l.name }
func (l *lateTool) Description() string         { return "answers after the others" }
func (l *lateTool) Impact(map[string]any) int   { return toolapi.ImpactObserve }
func (l *lateTool) RequiresTarget() bool        { return false }
func (l *lateTool) Parameters() json.RawMessage { return json.RawMessage(`{"type":"object","properties":{}}`) }
func (l *lateTool) Execute(ctx context.Context, _ map[string]any) (string, error) {
	select {
	case <-time.After(300 * time.Millisecond):
		return toolapi.ToolOK("listing", "the late answer", nil).JSON(), nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func TestAConcludingReflectionWaitsForARunningStep(t *testing.T) {
	model := newStubModel(t, map[string]stubReply{
		"submit_preflight": {Args: map[string]any{
			"mode": "agent", "intent": "observe", "skills": []string{},
		}},
		"plan": plan(
			step("process_list", "quick_a", nil),
			step("get_process", "quick_b", nil),
			step("web_search", "late", nil),
		),
		"reflector_decision": {Args: map[string]any{"decision": "conclude", "outcome": "done"}},
	})
	a := agentOnStubWith(t, model, DAGConfig{
		DAGEnabled: true, MaxNodes: 20, MaxLLMCalls: 20, BatchSize: 2,
		DAGWallClock: 10 * time.Second,
	}, &countingTool{name: "process_list"}, &countingTool{name: "get_process"}, &lateTool{name: "web_search"})

	res, err := a.RunDAGSync(context.Background(), Trigger{
		Type: "chat_query", DAGMode: DAGModeNReflect,
		Data: json.RawMessage(`{"query":"what is running?"}`),
	})
	if err != nil || res == nil {
		t.Fatalf("the run failed: %v (stages called: %v)", err, model.functionsCalled())
	}
	if got := nodeWithTag(t, traceNodes(t, res), "late")["state"]; got != "resolved" {
		t.Errorf("the step still running when the reflection concluded has state %v, want resolved: "+
			"the run ended without its result", got)
	}
}
