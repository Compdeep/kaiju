package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/user/kaiju/internal/agent/gates"
	"github.com/user/kaiju/internal/agent/llm"
)

/*
 * runAggregator makes a single LLM call to synthesize all results into a verdict.
 * desc: Delegates to runAggregatorWithIntent using trigger.Intent() directly.
 *       Only valid when intent is not auto.
 * param: ctx - context for the LLM call.
 * param: trigger - the investigation trigger.
 * param: graph - the completed investigation graph.
 * param: history - conversation history for multi-turn coherence.
 * return: verdict string, actuator actions slice, and error.
 */
func (a *Agent) runAggregator(ctx context.Context, trigger Trigger, graph *Graph, history []llm.Message) (string, []ActuatorAction, error) {
	return a.runAggregatorWithIntent(ctx, trigger, graph, trigger.Intent(), history)
}

/*
 * runAggregatorWithIntent synthesizes results using the resolved intent.
 * desc: Collects all results from the graph, builds an evidence prompt with
 *       the user's question or alert data, capability gaps, and gathered evidence,
 *       then streams the LLM response while broadcasting chunks for live display.
 *       History is conversation context for multi-turn coherence (aggregator-only).
 * param: ctx - context for the LLM call.
 * param: trigger - the investigation trigger.
 * param: graph - the completed investigation graph.
 * param: intent - the resolved IBE intent level.
 * param: history - conversation history for multi-turn coherence.
 * return: verdict string, actuator actions slice, and error.
 */
func (a *Agent) runAggregatorWithIntent(ctx context.Context, trigger Trigger, graph *Graph, intent gates.Intent, history []llm.Message) (string, []ActuatorAction, error) {
	return a.runAggregatorWithClient(ctx, trigger, graph, intent, history, a.llm)
}

func (a *Agent) runAggregatorWithClient(ctx context.Context, trigger Trigger, graph *Graph, intent gates.Intent, history []llm.Message, client *llm.Client) (string, []ActuatorAction, error) {
	results := graph.AllResults()

	var sb strings.Builder

	// For chat queries, extract and highlight the user's question
	if trigger.Type == "chat_query" {
		var q struct{ Query string `json:"query"` }
		json.Unmarshal(trigger.Data, &q)
		if q.Query != "" {
			sb.WriteString(fmt.Sprintf("## User Question\n\n%s\n\n", q.Query))
		}
	} else {
		sb.WriteString(fmt.Sprintf("## Security Alert: %s\n\n", trigger.Type))
		if trigger.AlertID != "" {
			sb.WriteString(fmt.Sprintf("**Alert ID:** %s\n", trigger.AlertID))
		}
		if len(trigger.Data) > 0 {
			sb.WriteString(fmt.Sprintf("**Alert Data:**\n```json\n%s\n```\n\n", string(trigger.Data)))
		}
	}

	// Inject declared capability gaps so the aggregator addresses them
	if len(graph.Gaps) > 0 {
		sb.WriteString("### Capability Gaps\n")
		sb.WriteString("The following capabilities were not available during this investigation. Acknowledge these in your response.\n\n")
		for _, gap := range graph.Gaps {
			sb.WriteString(fmt.Sprintf("- %s\n", gap))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("### Gathered Evidence\n\n")
	if len(results) == 0 {
		sb.WriteString("(no evidence gathered — all nodes failed or were skipped)\n")
	} else {
		for label, result := range results {
			sb.WriteString(fmt.Sprintf("**%s:**\n%s\n\n", label, result))
		}
	}

	intentStr := intent.String()
	// Inject card-specific aggregator guidance if available
	aggGuidance := ""
	if len(a.activeCards) > 0 {
		aggGuidance = a.capabilities.ComposeAggregatorGuidance(a.activeCards)
	}
	rolePrompt := fmt.Sprintf(defaultAggregatorRolePrompt, aggGuidance, intentStr)
	messages := BuildMessagesWithHistory(
		ComposeSystemPrompt(a.soulPrompt, rolePrompt),
		sb.String(),
		history,
	)

	// Stream the aggregator response, broadcasting each chunk for live display.
	// Use 2x configured MaxTokens — the aggregator synthesizes all evidence
	// into a full response and needs more output room than individual tool calls.
	aggMaxTokens := a.cfg.MaxTokens * 2
	if aggMaxTokens < 8192 {
		aggMaxTokens = 8192
	}
	raw, err := client.CompleteStream(ctx, &llm.ChatRequest{
		Messages:    messages,
		Temperature: a.cfg.Temperature,
		MaxTokens:   aggMaxTokens,
	}, func(chunk string) {
		a.broadcastDAGEvent(DAGEvent{Type: "verdict", Text: chunk})
	})
	if err != nil {
		return "", nil, fmt.Errorf("aggregator LLM call: %w", err)
	}
	if raw == "" {
		return "", nil, fmt.Errorf("aggregator LLM returned empty response")
	}

	log.Printf("[dag] aggregator output: %s", Text.TruncateLog(raw, 300))

	// Aggregator outputs plain markdown — no JSON parsing needed.
	return raw, nil, nil
}
