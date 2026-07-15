package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Compdeep/kaiju/internal/agent/llm"
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
	// Model-initiated delegation: the model wrote a self-contained task, so no
	// conversation history or session is passed.
	verdict, _, _, err := t.agent.RunAgentTask(ctx, fmt.Sprintf("agent-tool-%d", time.Now().UnixNano()), "", task, nil)
	return verdict, err
}

// RunAgentTask runs a task through the full executive synchronously — a fresh,
// autonomous run (always investigates, no chat-escape). history (may be nil)
// gives the agent conversation context so a follow-up like "summarise it" works.
// sessionID (may be "") is used ONLY to tag the run's DAG step events so a UI can
// show them under the originating conversation — the executive writes no memory,
// so this never pollutes that conversation. Returns the synthesized verdict plus
// the run's node/LLM counts. Shared by the agent tool and the chat front door's
// classifier-driven escalation.
func (a *Agent) RunAgentTask(ctx context.Context, alertID, sessionID, task string, history []llm.Message) (verdict string, nodes, llmCalls int, err error) {
	data, merr := json.Marshal(map[string]string{"query": task})
	if merr != nil {
		return "", 0, 0, fmt.Errorf("agent: marshal task: %w", merr)
	}
	trigger := Trigger{
		Type:          "api_query",
		AlertID:       alertID,
		Data:          data,
		Source:        "agent",
		ExecutionMode: "autonomous", // always investigate; never chat-escape a delegated task
		History:       history,
		SessionID:     sessionID, // event attribution only (executive writes no memory)
	}
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

// RouteChat classifies a chat message with the tuned router (chat / meta /
// investigate). The chat front door uses it to decide, reliably, whether a turn
// needs the agent — instead of leaving that to the chat model's tool-choice.
func (a *Agent) RouteChat(ctx context.Context, alertID, query string) string {
	return a.routeQuery(ctx, alertID, query)
}
