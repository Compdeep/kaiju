package agent

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/Compdeep/kaiju/internal/agent/gates"
	"github.com/Compdeep/kaiju/internal/agent/llm"
)

/*
 * runAggregator makes a single LLM call to synthesize all results into a verdict.
 * desc: Delegates to runAggregatorWithIntent using trigger.Intent() directly.
 *       Only valid when intent is not auto.
 * param: ctx - context for the LLM call.
 * param: trigger - the investigation trigger.
 * param: graph - the completed investigation graph.
 * param: history - conversation history for multi-turn coherence.
 * param: gateCtx - assembled context from ContextGate (node returns + worklog + skill guidance).
 * return: verdict string, actuator actions slice, and error.
 */
func (a *Agent) runAggregator(ctx context.Context, trigger Trigger, graph *Graph, history []llm.Message, gateCtx *ContextResponse) (string, []ActuatorAction, error) {
	return a.runAggregatorWithIntent(ctx, trigger, graph, trigger.Intent(), history, gateCtx)
}

func (a *Agent) runAggregatorWithIntent(ctx context.Context, trigger Trigger, graph *Graph, intent gates.Intent, history []llm.Message, gateCtx *ContextResponse) (string, []ActuatorAction, error) {
	return a.runAggregatorWithClient(ctx, trigger, graph, intent, history, a.llm, gateCtx)
}

func (a *Agent) runAggregatorWithClient(ctx context.Context, trigger Trigger, graph *Graph, intent gates.Intent, history []llm.Message, client *llm.Client, gateCtx *ContextResponse) (string, []ActuatorAction, error) {

	// Assemble user prompt from gate context plus capability gaps from graph.
	userPrompt := assembleAggregatorPrompt(trigger, graph, gateCtx)

	intentStr := intent.String()
	// Per-investigation cards live on the graph; fall back to agent field
	// if no graph is provided (defensive).
	cards := []string{}
	if graph != nil && len(graph.ActiveCards) > 0 {
		cards = graph.ActiveCards
	} else {
		cards = a.activeCards
	}
	aggGuidance := ""
	if len(cards) > 0 {
		aggGuidance = a.capabilities.ComposeAggregatorGuidance(cards)
	}
	rolePrompt := fmt.Sprintf(defaultAggregatorRolePrompt, aggGuidance, a.FormatRule(), intentStr)

	messages := BuildMessagesWithHistory(
		ComposeSystemPrompt(a.soulPrompt, rolePrompt),
		userPrompt,
		history,
	)

	// Stream the aggregator response, broadcasting each chunk for live display.
	// Use 2x configured MaxTokens — the aggregator synthesizes all evidence
	// into a full response and needs more output room than individual tool calls.
	aggMaxTokens := a.cfg.MaxTokens * 2
	if aggMaxTokens < 8192 {
		aggMaxTokens = 8192
	}
	started := time.Now()
	raw, err := client.CompleteStream(ctx, &llm.ChatRequest{
		Messages:    messages,
		Temperature: a.cfg.Temperature,
		MaxTokens:   aggMaxTokens,
	}, func(chunk string) {
		a.broadcastDAGEvent(DAGEvent{Type: "verdict", Text: chunk})
	})

	trace := LLMTrace{
		AlertID:  trigger.AlertID,
		NodeID:   "aggregator",
		NodeType: "aggregator",
		Tag:      "synthesize",
		Started:  started,
		Input: map[string]string{
			"intent": intentStr,
		},
		System:    ComposeSystemPrompt(a.soulPrompt, rolePrompt),
		User:      userPrompt,
		LatencyMS: time.Since(started).Milliseconds(),
	}
	if gateCtx != nil {
		trace.GateReturned = gateCtx.Sources
	}

	if err != nil {
		trace.Err = err.Error()
		WriteLLMTrace(trace)
		return "", nil, fmt.Errorf("aggregator LLM call: %w", err)
	}
	if raw == "" {
		trace.Err = "empty response"
		WriteLLMTrace(trace)
		return "", nil, fmt.Errorf("aggregator LLM returned empty response")
	}

	trace.Output = raw
	WriteLLMTrace(trace)

	log.Printf("[dag] aggregator output: %s", Text.TruncateLog(raw, 300))

	// Aggregator outputs plain markdown — no JSON parsing needed.
	return raw, nil, nil
}

// assembleAggregatorPrompt builds the aggregator's user message from a gate
// response and graph state. Sections: Original Request, Capability Gaps,
// Failed Steps, Skipped Steps, All Results, Worklog.
func assembleAggregatorPrompt(trigger Trigger, graph *Graph, gateCtx *ContextResponse) string {
	var sb strings.Builder

	// Original Request — anchor the aggregator to what the user ACTUALLY
	// asked for. Without this, the aggregator drifts toward whatever narrative
	// dominates the worklog, which can include entries from prior investigations
	// in the same session. The worklog stays as secondary context (cross-query
	// continuity matters), but the query is the primary signal.
	if q := formatTrigger(trigger); q != "" {
		sb.WriteString("## Original Request\n\n")
		sb.WriteString(q)
		sb.WriteString("\n\n")
	}

	// Capability gaps from the graph (set by the executive when no tool exists).
	if graph != nil && len(graph.Gaps) > 0 {
		sb.WriteString("## Capability Gaps\n\n")
		sb.WriteString("The following capabilities were not available. Acknowledge these in your response.\n\n")
		for _, gap := range graph.Gaps {
			sb.WriteString(fmt.Sprintf("- %s\n", gap))
		}
		sb.WriteString("\n")
	}

	// Failed and skipped step warnings — must be prominent so the aggregator
	// doesn't claim success for things that didn't happen.
	if graph != nil {
		failed := graph.FailedNodes()
		if len(failed) > 0 {
			sb.WriteString("## FAILED STEPS\n\n")
			sb.WriteString("The following steps FAILED. Address these honestly in your response — do NOT claim them as completed.\n\n")
			for _, f := range failed {
				label := f.Tag
				if label == "" {
					label = f.ToolName
				}
				errMsg := Text.TailTruncate(extractFailureDetail(f), 1200)
				sb.WriteString(fmt.Sprintf("- **%s** (%s): %s\n", label, f.ToolName, errMsg))
			}
			sb.WriteString("\n")
		}
		skipped := graph.SkippedNodes()
		if len(skipped) > 0 {
			sb.WriteString("## SKIPPED STEPS\n\n")
			sb.WriteString(fmt.Sprintf("%d nodes never ran because a dependency failed. Do NOT report these as completed.\n\n", len(skipped)))
			for _, s := range skipped {
				label := s.Tag
				if label == "" {
					label = s.ToolName
				}
				sb.WriteString(fmt.Sprintf("- %s\n", label))
			}
			sb.WriteString("\n")
		}
	}

	// Node returns from the gate (full evidence). The gate filters and formats.
	if gateCtx != nil {
		if returns := gateCtx.Sources[SourceNodeReturns]; returns != "" {
			sb.WriteString("## Evidence\n\n")
			sb.WriteString(returns)
			sb.WriteString("\n\n")
		}
		if wl := gateCtx.Sources[SourceWorklog]; wl != "" {
			sb.WriteString("## Execution Timeline\n\n```\n")
			sb.WriteString(wl)
			sb.WriteString("\n```\n\n")
		}
	}

	if sb.Len() == 0 {
		return "(no evidence gathered — all nodes failed or were skipped)\n"
	}
	return sb.String()
}
