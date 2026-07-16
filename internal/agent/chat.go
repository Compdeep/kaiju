package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/Compdeep/kaiju/internal/agent/gates"
	"github.com/Compdeep/kaiju/internal/agent/llm"
	"github.com/Compdeep/kaiju/internal/agent/prompt"
)

// isToolUnsupported reports whether an LLM error means the model/endpoint cannot
// use tools at all (as opposed to a transient failure worth surfacing). Providers
// signal this differently; OpenRouter returns a 404 "No endpoints found that
// support tool use." When the chat lane sees this, it retries the turn with no
// tools instead of failing — so a tool-less model still answers.
func isToolUnsupported(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "support tool use") ||
		strings.Contains(msg, "does not support tool") ||
		strings.Contains(msg, "tool use is not supported") ||
		strings.Contains(msg, "tools are not supported")
}

// ChatTurn is the input to the chat lane. History is the conversation so far
// (from memory), INCLUDING the current user message as its last entry; Query is
// a fallback used only when there is no memory/session and History is empty.
type ChatTurn struct {
	Provider  string
	Model     string
	History   []llm.Message
	Query     string
	ToolNames []string // explicit, API-driven allowlist; empty ⇒ pure chat (no tools)
	Images    []string // data URIs; attached only if Model is vision-capable
	Scope     *ResolvedScope
	AlertID   string
	MaxTurns  int
	// SessionID correlates a classifier-routed agent sub-run's step events back to
	// this conversation so the UI can show the agent working live. It is used for
	// event attribution only — the sub-run writes no memory to it.
	SessionID string
	// MaxIntent is the resolved IGX safety rank for this turn (already capped by
	// JWT/scope by the caller). nil ⇒ the registry default. It gates chat-lane tool
	// execution.
	MaxIntent *int
	// Base is the request's Trigger. When the turn escalates to the agent, the
	// sub-run is a COPY of this — so it inherits everything the request specified
	// (models, intent, scope, session, history) with nothing to thread by hand.
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
// CLI). It decides, via the tuned classifier rather than the model's tool-choice,
// whether a turn needs the full agent: when "agent" is in the turn's tools and the
// classifier says investigate, it runs the agent (with history for context) and
// returns its answer (its steps stream as DAG events for live progress);
// otherwise it answers on the chat lane (Converse) with the agent removed from the
// tool list. Light tools (e.g. web_fetch) stay model-driven in the chat lane — only
// the agent decision is gated by the classifier, because that's the unreliable one.
func (a *Agent) Chat(ctx context.Context, t ChatTurn) (ChatResult, error) {
	agentEnabled := false
	for _, n := range t.ToolNames {
		if n == agentToolName {
			agentEnabled = true
			break
		}
	}
	if agentEnabled && a.RouteChat(ctx, t.AlertID, t.Query) == "investigate" {
		verdict, nodes, llmCalls, err := a.RunAgentTask(ctx, t.Base, t.Query)
		return ChatResult{Content: verdict, Nodes: nodes, LLMCalls: llmCalls}, err
	}
	if agentEnabled {
		kept := t.ToolNames[:0]
		for _, n := range t.ToolNames {
			if n != agentToolName {
				kept = append(kept, n)
			}
		}
		t.ToolNames = kept
	}
	return a.Converse(ctx, t)
}

// chatMaxTurns bounds the reason-act loop when tools are in play. Chat is not an
// investigation — a few turns is plenty; the cap is a liveness backstop.
const chatMaxTurns = 6

// Converse runs the CHAT lane: a planner-less conversational turn with OPTIONAL
// tool use. This is the unified chat path.
//
//   - Persona = the agent's soul (operator SOUL override honoured) + prompt.Chat.
//   - Memory is supplied by the caller in History (verbatim; no planner truncation).
//   - Tools = ONLY the explicitly-granted ToolNames. Empty ⇒ a single completion
//     (pure chat). Non-empty ⇒ a bounded reason-act loop (model ⇄ tool ⇄ model).
//   - There is NO planner, preflight, reflection, or Holmes here, so the lane
//     cannot get stuck in an investigation, and it never sees the planner's full
//     tool surface — nothing here can rummage a workspace.
//   - Vision: images attach to the turn when Model is vision-capable, so an image
//     message is handled on this same lane (no separate vision path needed).
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

	// Tools: expose only the explicitly-listed set. The scope passed to
	// executeToolCall is derived from the same list, so "offered" == "permitted"
	// for the chat lane — it never inherits a broader tool scope.
	var toolDefs []llm.ToolDef
	if len(t.ToolNames) > 0 {
		toolDefs = a.registry.ToolDefsForNames(t.ToolNames)
	}
	maxTurns := t.MaxTurns
	if maxTurns <= 0 {
		maxTurns = chatMaxTurns
	}
	// Intent for tool gating: the caller's resolved request intent if set, else
	// the registry default. Per-tool impact + scope still gate at execution.
	intent := gates.Intent(a.intentRegistry.DefaultRank())
	if t.MaxIntent != nil {
		intent = gates.Intent(*t.MaxIntent)
	}

	// broadcast emits text to the frontend as a verdict event on this turn's
	// session — the same channel the agent lane streams answers on.
	broadcast := func(text string) {
		if t.SessionID != "" && text != "" {
			a.broadcastDAGEvent(nil, DAGEvent{Type: "verdict", Text: text, SessionID: t.SessionID})
		}
	}
	// stream runs a TOOL-LESS completion and broadcasts each text delta as it
	// arrives, so the frontend renders the answer token-by-token. Only tool-less
	// turns stream: a tool-capable ("tool-deciding") turn is run non-streamed via
	// client.Complete instead, because some models emit their tool call as plain
	// `content` rather than structured tool_calls — streaming that would broadcast
	// raw tool-call JSON to the UI as if it were the answer (the web_fetch bug).
	stream := func(r *llm.ChatRequest) (*llm.ChatResponse, error) {
		return client.CompleteStreamResp(ctx, r, func(chunk string) {
			broadcast(chunk)
		})
	}

	res := ChatResult{}
	for turn := 0; turn < maxTurns; turn++ {
		req := &llm.ChatRequest{
			Model:       t.Model,
			Messages:    messages,
			Temperature: 0.7,
			MaxTokens:   1024,
		}
		toolsThisTurn := len(toolDefs) > 0
		if toolsThisTurn {
			req.Tools = toolDefs
			req.ToolChoice = "auto" // never force — chat may simply answer
		}

		// Tool-capable turn ⇒ NON-streaming (client.Complete): the model's tool
		// call comes back as structured tool_calls and nothing is broadcast, so
		// tool-call JSON can never leak to the UI as answer text. Tool-less turn ⇒
		// stream, so a plain answer renders token-by-token.
		var resp *llm.ChatResponse
		var err error
		if toolsThisTurn {
			resp, err = client.Complete(ctx, req)
		} else {
			resp, err = stream(req)
		}
		res.LLMCalls++
		if err != nil {
			// The chat lane may be pointed at a model that can't use tools at all —
			// a roleplay/uncensored fine-tune, or any endpoint without tool support.
			// Offering tools to such a model hard-fails (OpenRouter 404: "no
			// endpoints found that support tool use"). That must NOT fail the turn:
			// answering tool-less models is the chat lane's whole reason to exist.
			// Drop the tools for the rest of this conversation and retry as pure chat.
			if toolsThisTurn && isToolUnsupported(err) {
				toolDefs = nil
				toolsThisTurn = false
				req.Tools = nil
				req.ToolChoice = ""
				resp, err = stream(req)
				res.LLMCalls++
			}
			if err != nil {
				return res, err
			}
		}
		res.Tokens += resp.Usage.TotalTokens
		if len(resp.Choices) == 0 {
			return res, nil
		}
		msg := resp.Choices[0].Message
		messages = append(messages, msg)

		// No tool calls ⇒ this is the answer.
		if len(msg.ToolCalls) == 0 {
			res.Content = msg.Content
			// A tool-capable turn was fetched non-streamed, so its text never
			// reached the UI. Push the finished answer now (once).
			if toolsThisTurn {
				broadcast(msg.Content)
			}
			return res, nil
		}

		// Execute the requested tools and feed results back for the next turn.
		for _, tc := range msg.ToolCalls {
			res.ToolCalls++
			result, execErr := a.executeToolCall(ctx, tc, t.AlertID, intent, t.Scope)
			toolMsg := llm.Message{
				Role:       "tool",
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
				Content:    result,
			}
			if execErr != nil {
				toolMsg.Content = fmt.Sprintf("error: %v", execErr)
			}
			messages = append(messages, toolMsg)
		}
	}

	// Hit the turn cap with tools still in flight — take one final, tool-less
	// completion so the user gets a real answer instead of nothing.
	resp, err := stream(&llm.ChatRequest{
		Model:       t.Model,
		Messages:    messages,
		Temperature: 0.7,
		MaxTokens:   1024,
	})
	res.LLMCalls++
	if err != nil {
		return res, err
	}
	res.Tokens += resp.Usage.TotalTokens
	if len(resp.Choices) > 0 {
		res.Content = resp.Choices[0].Message.Content
	}
	return res, nil
}
