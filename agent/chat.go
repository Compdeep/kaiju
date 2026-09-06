package agent

import (
	"context"

	"github.com/Compdeep/kaiju/agent/llm"
	"github.com/Compdeep/kaiju/agent/prompt"
	"github.com/Compdeep/kaiju/agent/toolapi"
)

// ChatTurn is the input to the chat lane. History is the conversation so far
// (from memory), INCLUDING the current user message as its last entry; Query is
// a fallback used only when there is no memory/session and History is empty.
type ChatTurn struct {
	Provider  string
	Model     string
	History   []llm.Message
	Query     string
	Images    []string // data URIs; attached only if Model is vision-capable
	TriggerID string
	MaxTurns  int
	// SessionID correlates a routed agent sub-run's step events back to this
	// conversation so the UI can show the agent working live. It is used for event
	// attribution only — the sub-run writes no memory to it.
	SessionID string
	// Agent permits escalation: nil/true ⇒ the router MAY escalate this turn to the
	// agent; false ⇒ stays in chat, never escalates. From the request's `agent`
	// field. (Run the agent directly via execute mode, not this flag.)
	Agent *bool
	// Base is the request's Trigger. When the turn goes to the agent, the sub-run
	// is a COPY of this — so it inherits everything the request specified (models,
	// intent, scope, session, history) with nothing to thread by hand.
	Base Trigger
	// Recalled is earlier messages of this conversation, from before the part
	// History carries, that the router said the answer needs. Empty on almost
	// every turn. Converse puts them in the request; nothing stores them, because
	// a stored copy would be summarised at the next compaction and recalled
	// again, and the conversation would fill with repetitions of itself.
	Recalled []toolapi.FoundMessage
	// RecallTerms is what those messages were found by looking for, so the model
	// is told what the search was and can judge a match that is not relevant.
	RecallTerms []string
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
	// Escalate to the agent only when this turn is PERMITTED to (t.Agent, default
	// true — nil means allowed) AND the tuned router — reading the latest message in
	// context (running summary + last exchange) — judges it needs more than a
	// conversational answer. agent=false ⇒ pure chat, never escalates. To run the
	// agent directly, callers use execute mode (chat_mode=false), not this lane.
	// ChatTools is the palette the agent uses if it escalates, never the trigger.
	mayEscalate := t.Agent == nil || *t.Agent
	mode, lacking := "chat", []string(nil)
	if mayEscalate {
		mode, lacking = a.routeQuery(ctx, t.TriggerID, t.Query, t.History)
	}
	if mode == "agent" {
		// Chat answers can be long. Force the aggregator (agg_mode=2, reasoning
		// lane, full synthesis budget) so a reflection-concluded run doesn't hand
		// back the 1024-token-capped reflection outcome truncated mid-sentence.
		t.Base.AggMode = 2
		outcome, nodes, llmCalls, err := a.RunAgentTask(ctx, t.Base, t.Query)
		return ChatResult{Content: outcome, Nodes: nodes, LLMCalls: llmCalls}, err
	}
	// Tool-less conversation. The router may have said the answer needs something
	// said before the part of the conversation the model is sent; the agent side
	// needs no such step, since message_search is a tool it can call.
	t.Recalled, t.RecallTerms = a.recall(ctx, t, lacking), lacking
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
	// The turn names its own provider and model, and this call writes an answer
	// for a person to read — so it is the answer lane, told what this turn
	// chose. Stamping the selection rather than resolving a client here is what
	// puts the call through the door, and with it the reply cap.
	//
	// The answer lane falls back to the heavy lane, which defaults to a.llm —
	// the same client clientFor returns for an empty provider — so a turn that
	// names nothing reaches what it always did.
	ctx = withLaneSelection(ctx, laneSelection{answerProvider: t.Provider, answerModel: t.Model})

	system := ComposeSystemPrompt(a.soulPrompt, prompt.Chat)
	var messages []llm.Message
	if len(t.History) > 0 {
		// History already ends with the current user message (stored, then loaded).
		messages = append([]llm.Message{{Role: "system", Content: system}}, t.History...)
	} else {
		messages = BuildMessagesWithHistory(system, t.Query, nil)
	}
	messages = withRecall(messages, recallBlock(t.Recalled, t.RecallTerms))
	if len(t.Images) > 0 && IsVisionModel(t.Model) {
		llm.AttachImages(messages, t.Images)
	}

	// One completion, streamed token-by-token to the frontend as outcome events
	// (the same channel the agent lane streams on). With no tools in play, no
	// tool-call JSON can ever reach the stream.
	res := ChatResult{LLMCalls: 1}
	resp, err := a.askStreamResp(ctx, Answer, &llm.ChatRequest{
		Model:       t.Model,
		Messages:    messages,
		Temperature: 0.7,
		MaxTokens:   a.replyBudget(replyBriefBudget),
	}, func(chunk, kind string) {
		if t.SessionID != "" {
			evType := "outcome"
			if kind == "reasoning" {
				evType = "reasoning"
			}
			a.broadcastDAGEvent(nil, DAGEvent{Type: evType, Text: chunk, SessionID: t.SessionID})
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
