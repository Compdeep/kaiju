package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/Compdeep/kaiju/agent/gates"
	"github.com/Compdeep/kaiju/agent/llm"
	"github.com/Compdeep/kaiju/agent/prompt"
)

/*
 * systemPrompt returns the base system prompt for the ReAct loop.
 * desc: Composes the soul prompt with role description, ReAct role prompt,
 *       skill card context, and the environment section. Active skill cards are
 *       passed in per-run (the caller owns them), not read off the Agent.
 * param: cards - active skill card keys for this run (may be nil).
 * return: the fully composed system prompt string.
 */
func (a *Agent) systemPrompt(cards []string) string {
	cardContext := ""
	if g := a.composeGuidance(cards); g != "" {
		cardContext = "\n\n" + g
	}
	rolePrompt := fmt.Sprintf("You are an agent on node %s.\n%s\n\n%s%s%s",
		a.cfg.NodeID, roleDescription(a.cfg.NodeRole), prompt.React, cardContext, a.environmentSection())
	return ComposeSystemPrompt(a.soulPrompt, rolePrompt)
}

/*
 * RunReActSync runs the ReAct loop synchronously and returns a SyncResult.
 * desc: Runs the ReAct tool loop synchronously and returns the outcome.
 *       Used by the API when mode=react. Goes through the same pipeline as DAG
 *       (IGX gate, scope checks, audit, tool execution) but with sequential
 *       reason-act-observe dispatch instead of parallel DAG execution.
 * param: ctx - context for the investigation.
 * param: trigger - the investigation trigger.
 * return: SyncResult with outcome, or error.
 */
func (a *Agent) RunReActSync(ctx context.Context, trigger Trigger) (*SyncResult, error) {
	ctx = a.tagTokens(ctx, trigger)
	ctx = withLaneSelection(ctx, laneSelectionFromTrigger(trigger))
	// This loop builds no graph, so its run id lives only on the context.
	ctx = withRunID(ctx, newRunID(trigger.ID))
	// And what started it, for the same reason: a tool rule reads the trigger
	// off the graph, and there is no graph here.
	ctx = withTrigger(ctx, &trigger)
	log.Printf("[react] sync run: type=%s id=%s source=%s",
		trigger.Type, trigger.ID, trigger.Source)

	startTime := time.Now()

	// Get relevant tools and skills — same set the DAG planner sees
	relevant := a.relevantTools(ctx, trigger)
	toolDefs := a.registry.ToolDefsForNames(relevant)

	// Build initial messages — no skill guidance injection (same as native planner).
	// Skills are available as tool descriptions in the API tools array.
	messages := BuildMessagesWithHistory(
		a.systemPrompt(nil),
		a.formatTrigger(trigger),
		trigger.History,
	)

	log.Printf("[react] sees %d tools (%d callable, %d guidance-only)", len(relevant), len(toolDefs), len(relevant)-len(toolDefs))

	if len(toolDefs) == 0 {
		outcome := "No tools available."
		a.recordRun(trigger, startTime, nil, nil, trigger.Intent(),
			Conclusion{Outcome: outcome, Status: "failed"})
		return &SyncResult{Outcome: outcome}, nil
	}

	// IGX: derive intent. Auto falls back to the registry's default rank.
	intent := trigger.Intent()
	if intent == gates.IntentAuto {
		intent = gates.Intent(a.intentRegistry.DefaultRank())
	}

	var outcome string
	totalToolCalls := 0
	totalLLMCalls := 0

	for turn := 0; turn < a.cfg.MaxTurns; turn++ {
		if err := a.gate.CheckTurns(turn); err != nil {
			break
		}

		// First turn: force tool use so ReAct doesn't skip tools entirely.
		// Subsequent turns: let the model decide (auto).
		toolChoice := "auto"
		if turn == 0 {
			toolChoice = "required"
		}

		resp, err := a.completeHeavy(ctx, &llm.ChatRequest{
			Messages:    messages,
			Tools:       toolDefs,
			ToolChoice:  toolChoice,
			Temperature: a.cfg.Temperature,
			MaxTokens:   a.cfg.MaxTokens,
		})
		if err != nil {
			a.recordRun(trigger, startTime, nil, nil, intent, Conclusion{
				Outcome:  fmt.Sprintf("react LLM error turn %d: %v", turn, err),
				Status:   "failed",
				Nodes:    totalToolCalls,
				LLMCalls: totalLLMCalls,
			})
			return nil, fmt.Errorf("react LLM error turn %d: %w", turn, err)
		}
		totalLLMCalls++

		if len(resp.Choices) == 0 {
			break
		}

		choice := resp.Choices[0]
		assistantMsg := choice.Message
		messages = append(messages, assistantMsg)

		// Stream text content as outcome chunks
		if assistantMsg.Content != "" {
			a.broadcastDAGEvent(nil, DAGEvent{Type: "outcome", Text: assistantMsg.Content, SessionID: trigger.SessionID})
			outcome = assistantMsg.Content
		}

		if choice.FinishReason == "stop" || choice.FinishReason == "length" {
			log.Printf("[react] complete (reason=%s, turns=%d, tools=%d, llm=%d)",
				choice.FinishReason, turn+1, totalToolCalls, totalLLMCalls)
			break
		}

		if len(assistantMsg.ToolCalls) == 0 {
			break
		}

		// Broadcast tool calls as DAG events so the frontend can show them
		for _, tc := range assistantMsg.ToolCalls {
			totalToolCalls++
			nodeID := fmt.Sprintf("react_%d", totalToolCalls)

			// Compact params for display. Keep enough that a tool's URL survives —
			// web_fetch lists other keys (focus/format) before "url", so a tight cap
			// chops the URL off before the UI ever sees it.
			paramsStr := tc.Function.Arguments
			if len(paramsStr) > 512 {
				paramsStr = paramsStr[:512] + "..."
			}

			a.broadcastDAGEvent(nil, DAGEvent{Type: "node", SessionID: trigger.SessionID, NodeID: nodeID, Node: &NodeInfo{
				ID: nodeID, Type: "tool", State: "running",
				Tool: tc.Function.Name, Tag: tc.Function.Name,
				Params: paramsStr,
			}})

			toolStart := time.Now()
			result, execErr := a.executeToolCall(ctx, tc, trigger.ID, intent, trigger.Scope)
			toolMs := time.Since(toolStart).Milliseconds()

			toolMsg := llm.Message{
				Role:       "tool",
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
				Content:    result,
			}
			if execErr != nil {
				toolMsg.Content = fmt.Sprintf("error: %v", execErr)
				a.broadcastDAGEvent(nil, DAGEvent{Type: "node", SessionID: trigger.SessionID, NodeID: nodeID, Node: &NodeInfo{
					ID: nodeID, Type: "tool", State: "failed",
					Tool: tc.Function.Name, Tag: tc.Function.Name,
					Ms: toolMs, Error: execErr.Error(),
					Params: paramsStr,
				}})
			} else {
				truncResult := result
				if len(truncResult) > 200 {
					truncResult = truncResult[:200] + "..."
				}
				a.broadcastDAGEvent(nil, DAGEvent{Type: "node", SessionID: trigger.SessionID, NodeID: nodeID, Node: &NodeInfo{
					ID: nodeID, Type: "tool", State: "resolved",
					Tool: tc.Function.Name, Tag: tc.Function.Name,
					Ms: toolMs, ResultSize: len(result),
					Result: truncResult, Params: paramsStr,
				}})
			}
			messages = append(messages, toolMsg)
		}

		log.Printf("[react] turn %d: %d tool calls (tokens: %d)",
			turn, len(assistantMsg.ToolCalls), resp.Usage.TotalTokens)

		// Interjection check
		if interject := interjectFrom(ctx); interject != nil {
			select {
			case msg := <-interject:
				messages = append(messages, llm.Message{
					Role:    "user",
					Content: fmt.Sprintf("[Operator interjection]: %s", msg),
				})
			default:
			}
		}
	}

	elapsed := time.Since(startTime)
	log.Printf("[react] sync complete in %s (id=%s, tools=%d, llm=%d)",
		elapsed.Round(time.Millisecond), trigger.ID, totalToolCalls, totalLLMCalls)

	a.recordRun(trigger, startTime, nil, nil, intent, Conclusion{
		Outcome:  outcome,
		Status:   "completed",
		Nodes:    totalToolCalls,
		LLMCalls: totalLLMCalls,
	})

	return &SyncResult{
		Outcome:  outcome,
		Nodes:    totalToolCalls,
		LLMCalls: totalLLMCalls,
	}, nil
}

/*
 * executeToolCall runs one of the model's tool calls.
 * desc: The gate pipeline, the execution and the audit are executeToolNode's,
 *       which is where the DAG runs a step too. This loop had its own copy of
 *       all three and had fallen behind it: the typed path was missing, so a
 *       tool returning a ToolMessage never took it; the application's own rule
 *       was never asked at all; rank-0 tools were throttled where the DAG
 *       exempts them; and the audit line carried the run but not the caller's
 *       reference.
 *
 *       What is left here is what is this loop's own. Turning the model's
 *       arguments into a parameter map, because only this path receives them as
 *       JSON text. And fitting the result to a chat message, because that is
 *       what a result becomes here — where a node's result is data the graph
 *       passes on, which is why executeToolNode leaves a structured envelope
 *       whole.
 * param: ctx - the run's context, carrying the run id and the trigger.
 * param: tc - the tool call the model made.
 * param: triggerID - the caller's own id for this run.
 * param: intent - the IGX intent level.
 * param: scope - resolved tool access scope (nil for full access).
 * return: the result the model reads, and any error.
 */
func (a *Agent) executeToolCall(ctx context.Context, tc llm.ToolCall,
	triggerID string, intent gates.Intent, scope *ResolvedScope) (string, error) {

	var params map[string]any
	if tc.Function.Arguments != "" {
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &params); err != nil {
			return "", fmt.Errorf("invalid parameters: %w", err)
		}
	}

	// No node, no graph, no budget: this loop builds none of them. The trigger a
	// tool rule needs travels on the context instead — see withTrigger.
	result, _, err := a.executeToolNode(ctx, nil, nil, nil,
		tc.Function.Name, params, triggerID, intent, scope)
	if err != nil {
		return "", err
	}

	// executeToolNode leaves a structured envelope at full length because the
	// scheduler unmarshals it for graft instructions. Nothing unmarshals it here
	// — it goes to the model as a chat message — so it is fitted to the cap like
	// any other result. truncateToolResult shrinks the longest string inside a
	// JSON object rather than splicing bytes, so the envelope stays readable.
	if len(result) > maxToolResultLen {
		result = truncateToolResult(result, maxToolResultLen, Text.HeadTail)
	}
	return result, nil
}

/*
 * formatTrigger converts a trigger into a human-readable message for the LLM.
 * desc: For chat queries, extracts and returns the user's question directly.
 *       For all other trigger types, states the type, the id, the source,
 *       and data fields.
 * param: t - the Trigger to format.
 * return: formatted trigger string for LLM consumption.
 */
/*
 * formatTrigger renders a run's starting point as the text the planner reads.
 * desc: Asks the application first, through Config.DescribeTrigger. An empty
 *       answer, or no callback at all, falls through to the built-in rendering
 *       below — so an application describes only the kinds of work it knows
 *       about and leaves chat queries and the generic case alone.
 * param: t - the trigger.
 * return: the text to put in front of the planner.
 */
func (a *Agent) formatTrigger(t Trigger) (out string) {
	if a != nil && a.describeTrigger != nil {
		// Wording that crashed leaves the built-in wording, which is what no
		// description gives. Every reasoning stage reads this, so an empty
		// answer would leave them all with nothing.
		panicked := false
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[dag] the trigger description panicked, using the built-in wording: %v", r)
					panicked = true
				}
			}()
			out = a.describeTrigger(t)
		}()
		if !panicked && out != "" {
			return out
		}
	}
	return defaultFormatTrigger(t)
}

/*
 * defaultFormatTrigger is the built-in rendering, covering the trigger kinds
 * this package defines for itself.
 * param: t - the trigger.
 * return: the rendered text.
 */
func defaultFormatTrigger(t Trigger) string {
	// A typed question is presented as itself. The planner should see "what
	// processes are running?" rather than that question wrapped in a header
	// and a correlation id.
	if t.Type == "chat_query" {
		var data map[string]string
		if json.Unmarshal(t.Data, &data) == nil {
			if q := data["query"]; q != "" {
				return q
			}
		}
		return string(t.Data)
	}

	var sb strings.Builder
	// The built-in wording says what this package knows and no more: a run
	// started, something caused it, here is what came with it. Its headings and
	// its instruction used to name one kind of payload, which told every model
	// driven by this loop what its input was — the one place this package
	// asserted what the payload means.
	//
	// An application that knows better supplies Config.DescribeTrigger, which
	// formatTrigger asks first and which this only backs up.
	sb.WriteString("## What started this run\n\n")
	sb.WriteString(fmt.Sprintf("**Type:** %s\n", t.Type))
	if t.ID != "" {
		sb.WriteString(fmt.Sprintf("**Correlation ID:** %s\n", t.ID))
	}
	if t.Source != "" {
		sb.WriteString(fmt.Sprintf("**Source:** %s\n", t.Source))
	}
	if len(t.Data) > 0 {
		sb.WriteString(fmt.Sprintf("\n**Data:**\n```json\n%s\n```\n", string(t.Data)))
	}
	sb.WriteString("\nUse your available tools to gather context, then decide what to do.")
	return sb.String()
}
