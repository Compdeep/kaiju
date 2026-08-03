package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// agentToolName is a RESERVED name, not a registered tool. Chat escalates to the
// agent via the router + RunAgentTask, not a tool call, so nothing is registered
// under this name. It is kept only as an exclusion marker: relevantTools and the
// DAG nodes' tool sections skip it, so a re-added delegation tool could never let
// the agent spawn an agent.
const agentToolName = "agent"

// RunAgentTask runs a task through the full executive synchronously. It COPIES
// the request's base Trigger so the sub-run inherits everything the request
// specified — per-request models, resolved intent, tool scope, session (for event
// attribution; the executive writes no memory), and conversation history — then
// overrides only the delegated bits (task, autonomous, fresh alert id). Deriving
// from the one struct means new request fields flow here for free, instead of
// being threaded (and dropped) by hand. The agent tool passes an empty base.
// Returns the verdict plus node/LLM counts.
func (a *Agent) RunAgentTask(ctx context.Context, base Trigger, task string) (verdict string, nodes, llmCalls int, err error) {
	data, merr := json.Marshal(map[string]string{"query": task})
	if merr != nil {
		return "", 0, 0, fmt.Errorf("agent: marshal task: %w", merr)
	}
	// COPY the request trigger so the sub-run inherits everything the request
	// specified — models (Provider/Model/Executor*), intent (MaxIntent), scope,
	// session (event attribution), history — then override only the delegated bits.
	trigger := base
	trigger.Type = "api_query"
	trigger.AlertID = fmt.Sprintf("agent-%d", time.Now().UnixNano())
	trigger.Data = data
	trigger.Source = "agent"
	trigger.ExecutionMode = "autonomous" // always investigate; never chat-escape a delegated task
	res, rerr := a.RunDAGSync(ctx, trigger)
	if rerr != nil {
		// A conversational fallback (trivial task) isn't a failure — return its text.
		var convErr *ExecutiveConversationalError
		if errors.As(rerr, &convErr) {
			return convErr.Text, 0, 0, nil
		}
		return "", 0, 0, rerr
	}
	if res == nil {
		return "", 0, 0, nil
	}
	return res.Verdict, res.Nodes, res.LLMCalls, nil
}
