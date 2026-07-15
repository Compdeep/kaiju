package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Compdeep/kaiju/internal/agent/tools"
)

// agentToolName is the registry name of the agent tool. It is deliberately
// EXCLUDED from relevantTools (see agent.go) so the executive/planner never sees
// it — that caps recursion: an agent can't spawn an agent. It is reachable only
// when a lane names it explicitly, e.g. the chat lane's chat_tools allowlist.
const agentToolName = "agent"

// AgentTool lets a lightweight lane (the chat lane) hand a genuinely multi-step
// task to the full executive — planner, tools, reflection — and get back the
// synthesized answer. It is how "chat escalates to the agent for deep work"
// without the chat lane itself carrying the planner.
type AgentTool struct {
	agent *Agent
}

// NewAgentTool constructs an AgentTool bound to an Agent.
func NewAgentTool(a *Agent) *AgentTool { return &AgentTool{agent: a} }

func (t *AgentTool) Name() string { return agentToolName }

func (t *AgentTool) Description() string {
	return "Delegate a complex, multi-step task to the full agent, which plans and runs " +
		"tools (web, compute, files, etc.), reflects on results, and returns a synthesized " +
		"answer. Use this for real work that needs planning or several steps — not for a " +
		"single lookup or an answer you can give directly."
}

var agentToolParamSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"task": {"type": "string", "description": "The task for the agent to carry out, stated in full. The agent does NOT see this conversation — include every concrete detail it needs (names, numbers, URLs, constraints)."}
	},
	"required": ["task"]
}`)

func (t *AgentTool) Parameters() json.RawMessage { return agentToolParamSchema }

// Impact: invoking the agent is itself observe-level (it's delegation). Whatever
// actions the sub-agent then takes are gated inside its own run by that run's
// intent, not here.
func (t *AgentTool) Impact(map[string]any) int { return tools.ImpactObserve }

// Execute runs the task through the full executive synchronously and returns the
// agent's answer. The sub-run is a fresh, autonomous executive run (always
// investigates — never chat-escapes a delegated task) with no session, so its
// internal steps don't pollute the calling conversation's memory.
func (t *AgentTool) Execute(ctx context.Context, params map[string]any) (string, error) {
	task, _ := params["task"].(string)
	if task == "" {
		return "", fmt.Errorf("agent: 'task' parameter is required")
	}
	data, err := json.Marshal(map[string]string{"query": task})
	if err != nil {
		return "", fmt.Errorf("agent: marshal task: %w", err)
	}
	trigger := Trigger{
		Type:          "api_query",
		AlertID:       fmt.Sprintf("agent-tool-%d", time.Now().UnixNano()),
		Data:          data,
		Source:        "agent_tool",
		ExecutionMode: "autonomous", // always investigate; never chat-escape a delegated task
	}
	res, err := t.agent.RunDAGSync(ctx, trigger)
	if err != nil {
		// A conversational fallback (trivial task) isn't a failure — return its text.
		var convErr *ExecutiveConversationalError
		if errors.As(err, &convErr) {
			return convErr.Text, nil
		}
		return "", err
	}
	if res == nil {
		return "", nil
	}
	return res.Verdict, nil
}
