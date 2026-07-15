package agent

import (
	"context"
	"fmt"

	"github.com/Compdeep/kaiju/internal/agent/gates"
	"github.com/Compdeep/kaiju/internal/agent/llm"
	"github.com/Compdeep/kaiju/internal/agent/prompt"
)

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
	// execution AND is passed to the agent when a turn is escalated, so the user's
	// chosen safety level is honoured wherever the turn is handled.
	MaxIntent *int
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
		verdict, nodes, llmCalls, err := a.RunAgentTask(ctx, t.AlertID, t.SessionID, t.Query, t.History, t.MaxIntent)
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

	res := ChatResult{}
	for turn := 0; turn < maxTurns; turn++ {
		req := &llm.ChatRequest{
			Model:       t.Model,
			Messages:    messages,
			Temperature: 0.7,
			MaxTokens:   1024,
		}
		if len(toolDefs) > 0 {
			req.Tools = toolDefs
			req.ToolChoice = "auto" // never force — chat may simply answer
		}
		resp, err := client.Complete(ctx, req)
		res.LLMCalls++
		if err != nil {
			return res, err
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
	resp, err := client.Complete(ctx, &llm.ChatRequest{
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
