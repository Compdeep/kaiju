package agent

import (
	"context"
	"log"
	"strings"

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
	// This lane answers and stays answering. It used to ask the router first and
	// leave for the planner mid-turn when the router said the message needed
	// tools, which meant asking for chat bought a conversation that might not
	// stay one — and a caller who wanted the guarantee had to send a second flag
	// that no interface offered.
	//
	// A turn that should be allowed to become an investigation says so with the
	// auto mode, which is the one that asks the router. Chat is now the answer to
	// "keep this a conversation", and the only answer needed.
	//
	// One question, asked with this lane's own prompt: what does answering need
	// from earlier in this conversation? Nothing is classified here — the mode
	// already said this turn is a conversation — and the reply carries no mode
	// to ask about, which is what stops the words spending the budget the
	// decision used to need.
	//
	// This is the only way to reach back on this lane: there are no tools here,
	// so "what did we say about X earlier" is answerable only by looking.
	lacking := a.recallTerms(ctx, t.TriggerID, t.Query, t.History)
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
	//
	// The reasoning half comes off Base rather than being named here, because
	// this lane overrides only the MODEL. Built as a literal it silently dropped
	// everything else the request had said: a per-request effort travelled on
	// every other lane and vanished on the one a person actually talks to,
	// because that is the one path that does not go through
	// laneSelectionFromTrigger.
	sel := laneSelection{answerProvider: t.Provider, answerModel: t.Model}
	sel.effort, sel.budget = t.Base.ReasoningEffort, t.Base.ReasoningMaxTokens
	ctx = withLaneSelection(ctx, sel)

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

	stream := func(chunk, kind string) {
		if t.SessionID == "" {
			return
		}
		evType := "outcome"
		if kind == "reasoning" {
			evType = "reasoning"
		}
		a.broadcastDAGEvent(nil, DAGEvent{Type: evType, Text: chunk, SessionID: t.SessionID})
	}

	req := &llm.ChatRequest{
		Model:       t.Model,
		Messages:    messages,
		Temperature: 0.7,
		// replyDecisionBudget, not replyBriefBudget. This lane writes the answer a
		// person reads; brief bounds "one stage's judgement, in a sentence or
		// two" — an observer deciding whether a step is worth acting on. It was
		// the smallest user-facing cap in the engine, on the one lane where the
		// cap IS the answer, and its 4,096 ceiling is exactly what glm-5.3 spent
		// thinking before returning nothing.
		MaxTokens: a.replyBudget(replyDecisionBudget),
	}

	// A clock on the call, for the same reason the planner has one: max_tokens
	// bounds the reply, not the wait. A model that reasons before answering can
	// spend an unbounded amount of time doing it, and this is the lane where a
	// person is sitting and waiting.
	//
	// A deadline of ours returns an ERROR and no reply — not a cut-off one — so
	// it is answered below rather than reported. Reporting it hands the reader a
	// context error in place of an answer, which is the outcome the deadline
	// exists to avoid.
	chatCtx, cancelChat := context.WithTimeout(ctx, a.roundBudget(ctx, Answer, t.Base))
	defer cancelChat()

	// One completion, streamed token-by-token to the frontend as outcome events
	// (the same channel the agent lane streams on). With no tools in play, no
	// tool-call JSON can ever reach the stream.
	res := ChatResult{LLMCalls: 1}
	resp, err := a.askStreamResp(chatCtx, Answer, req, stream)

	// Our own clock ran out. Ask again with thinking off, under the run's
	// remaining time — the same answer an exhausted budget gets, because it is
	// the same problem arriving as an error rather than as an empty reply.
	if err != nil && chatCtx.Err() != nil && ctx.Err() == nil {
		log.Printf("[chat] %s passed its %s deadline — re-asking with thinking off",
			t.Model, a.roundBudget(ctx, Answer, t.Base))
		recovered, rerr := a.recoverDeadThought(retracing(ctx, "chat_recover_deadline"), Answer, req, nil)
		if rerr == nil && len(recovered.Choices) > 0 {
			res.LLMCalls++
			res.Tokens += recovered.Usage.TotalTokens
			res.Content = recovered.Choices[0].Message.Content
			stream(res.Content, "outcome")
			return res, nil
		}
	}
	if err != nil {
		return res, err
	}
	res.Tokens += resp.Usage.TotalTokens
	if len(resp.Choices) > 0 {
		res.Content = resp.Choices[0].Message.Content
	}

	// The whole budget went on reasoning and the reply never started.
	//
	// This lane had no answer for that. It returned the empty string, and the
	// caller turned it into "the request finished but produced no answer —
	// nothing usable was gathered", which describes a failure to gather and
	// tells the reader to rephrase. Neither was true: one live turn spent 112
	// seconds and all 4,096 tokens thinking, and rephrasing would not have
	// helped.
	//
	// So the same recovery the planner has: re-ask with thinking off, handing
	// back the reasoning already produced. A model that cannot think has
	// nothing to spend the budget on but the answer.
	if len(resp.Choices) > 0 && strings.TrimSpace(res.Content) == "" && nothingVisible(resp.Choices[0]) {
		log.Printf("[chat] %s returned nothing — the reply budget went on reasoning; re-asking with thinking off", t.Model)
		recovered, rerr := a.recoverDeadThought(retracing(ctx, "chat_recover_thought"), Answer, req, resp)
		if rerr == nil && len(recovered.Choices) > 0 {
			res.LLMCalls++
			res.Tokens += recovered.Usage.TotalTokens
			res.Content = recovered.Choices[0].Message.Content
			// The first attempt streamed nothing a reader could use, so the
			// recovered answer has to reach them the way the first would have.
			stream(res.Content, "outcome")
		}
	}
	return res, nil
}
