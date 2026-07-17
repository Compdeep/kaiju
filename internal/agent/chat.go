package agent

import (
	"context"

	"github.com/Compdeep/kaiju/internal/agent/llm"
	"github.com/Compdeep/kaiju/internal/agent/prompt"
)

// ChatTurn is the input to the chat lane. History is the conversation so far
// (from memory), INCLUDING the current user message as its last entry; Query is
// a fallback used only when there is no memory/session and History is empty.
type ChatTurn struct {
	Provider string
	Model    string
	History  []llm.Message
	Query    string
	// ToolNames is the request's API-driven tool allowlist. It drives ROUTING, not
	// chat-lane execution: if it names any real tool, Chat sends the turn to the
	// agent path (the chat lane itself is tool-less). Empty ⇒ pure chat.
	ToolNames []string
	Images    []string // data URIs; attached only if Model is vision-capable
	Scope     *ResolvedScope
	AlertID   string
	MaxTurns  int
	// SessionID correlates a routed agent sub-run's step events back to this
	// conversation so the UI can show the agent working live. It is used for event
	// attribution only — the sub-run writes no memory to it.
	SessionID string
	// MaxIntent is the resolved IGX safety rank for this turn (already capped by
	// JWT/scope by the caller). nil ⇒ the registry default. It rides along on Base
	// to gate an agent sub-run's tools.
	MaxIntent *int
	// Base is the request's Trigger. When the turn goes to the agent, the sub-run
	// is a COPY of this — so it inherits everything the request specified (models,
	// intent, scope, session, history) with nothing to thread by hand.
	Base Trigger
}

// ChatResult is the outcome of a chat turn.
type ChatResult struct {
	Content   string
	ToolCalls int
	LLMCalls  int
	Tokens    int
	Nodes     int // >0 when the turn was handled by the agent (a sub-DAG)
}

// Chat is the chat FRONT DOOR — call this, not Converse, from every surface (API,
// CLI). It routes a turn to one of two lanes:
//
//   - The AGENT path (RunAgentTask) for any turn that needs a tool. The chat lane
//     has no planner/reflection/validator/aggregator, so running tools there yields
//     hallucinated answers and silently-mishandled tool failures (a fetch 401s and
//     the model invents a result). The agent path has that correctness machinery —
//     it retries, reflects, and won't synthesize an answer that ignores a failed
//     tool. A turn goes to the agent when it names ANY real tool, or when the
//     "agent" tool is offered and the tuned classifier judges the query needs
//     investigation.
//   - The tool-less CHAT lane (Converse) for everything else — plain conversation
//     and non-tool (roleplay) models. It never receives tools.
//
// The agent's steps stream as DAG events for live progress; its models, intent,
// scope, and history are inherited from the request's Base trigger.
func (a *Agent) Chat(ctx context.Context, t ChatTurn) (ChatResult, error) {
	agentOffered := false
	wantsTool := false
	for _, n := range t.ToolNames {
		if n == agentToolName {
			agentOffered = true // the "agent" marker is escalation permission, not a tool
			continue
		}
		wantsTool = true
	}
	// Any real tool ⇒ the agent path. With no tool named, ask the classifier
	// whether an offered agent should still pick this turn up (e.g. "investigate X"
	// phrased as plain chat).
	if wantsTool || (agentOffered && a.RouteChat(ctx, t.AlertID, t.Query) == "investigate") {
		// Chat answers can be long. Force the aggregator (agg_mode=2, reasoning
		// lane, full synthesis budget) so a reflection-concluded run doesn't hand
		// back the 1024-token-capped reflection verdict truncated mid-sentence.
		t.Base.AggMode = 2
		verdict, nodes, llmCalls, err := a.RunAgentTask(ctx, t.Base, t.Query)
		return ChatResult{Content: verdict, Nodes: nodes, LLMCalls: llmCalls}, err
	}
	// Tool-less conversation.
	t.ToolNames = nil
	return a.Converse(ctx, t)
}

// Converse runs the CHAT lane: a planner-less, TOOL-LESS conversational turn —
// the unified path for plain conversation and non-tool (roleplay) models. Any
// turn that needs a tool is routed to the agent by Chat BEFORE it reaches here;
// the chat lane has no reflection/validator/aggregator, so it must not run tools
// (that produced confident, hallucinated answers when a tool failed).
//
//   - Persona = the agent's soul (operator SOUL override honoured) + prompt.Chat.
//   - Memory is supplied by the caller in History (verbatim; no planner truncation).
//   - Vision: images attach when Model is vision-capable, so an image message is
//     handled on this same lane (no separate vision path needed).
func (a *Agent) Converse(ctx context.Context, t ChatTurn) (ChatResult, error) {
	client := a.clientFor(t.Provider)

	system := ComposeSystemPrompt(a.soulPrompt, prompt.Chat)
	var messages []llm.Message
	if len(t.History) > 0 {
		// History already ends with the current user message (stored, then loaded).
		messages = append([]llm.Message{{Role: "system", Content: system}}, t.History...)
	} else {
		messages = BuildMessagesWithHistory(system, t.Query, nil)
	}
	if len(t.Images) > 0 && IsVisionModel(t.Model) {
		llm.AttachImages(messages, t.Images)
	}

	// One completion, streamed token-by-token to the frontend as verdict events
	// (the same channel the agent lane streams on). With no tools in play, no
	// tool-call JSON can ever reach the stream.
	res := ChatResult{LLMCalls: 1}
	resp, err := client.CompleteStreamResp(ctx, &llm.ChatRequest{
		Model:       t.Model,
		Messages:    messages,
		Temperature: 0.7,
		MaxTokens:   1024,
	}, func(chunk string) {
		if t.SessionID != "" {
			a.broadcastDAGEvent(nil, DAGEvent{Type: "verdict", Text: chunk, SessionID: t.SessionID})
		}
	})
	if err != nil {
		return res, err
	}
	res.Tokens += resp.Usage.TotalTokens
	if len(resp.Choices) > 0 {
		res.Content = resp.Choices[0].Message.Content
	}
	return res, nil
}
