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
}

// ChatResult is the outcome of a chat-lane turn.
type ChatResult struct {
	Content   string
	ToolCalls int
	LLMCalls  int
	Tokens    int
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
	// Intent for tool gating: chat tools are conversational helpers. Use the
	// registry default rank; per-tool impact + scope still gate at execution.
	intent := gates.Intent(a.intentRegistry.DefaultRank())

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
