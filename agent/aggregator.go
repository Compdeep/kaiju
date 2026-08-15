package agent

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/Compdeep/kaiju/agent/gates"
	"github.com/Compdeep/kaiju/agent/llm"
	"github.com/Compdeep/kaiju/agent/prompt"
)

// runAggregator synthesizes the final outcome on the given lane — Answer
// ordinarily, Light when a caller asked for the executor model. It took a
// client and a routed model id, resolved by its caller; the lane is one
// argument that says the same thing and goes through the door with everything
// else.
func (a *Agent) runAggregator(ctx context.Context, trigger Trigger, graph *Graph, intent gates.Intent, history []llm.Message, l Lane, gateCtx *ContextResponse) (string, []ActuatorAction, error) {

	// Assemble user prompt from gate context plus capability gaps from graph.
	userPrompt := a.assembleAggregatorPrompt(trigger, graph, gateCtx)

	intentStr := intent.String()
	// The doctrine this run selected, asked for the way every other stage asks
	// for it — through the gate, which resolves it, lays it out and holds it to
	// a character budget. A separate request from the evidence above, so
	// guidance and evidence do not compete for one budget.
	aggGuidance := ""
	if graph != nil && len(graph.ActiveCards) > 0 {
		if gr, err := graph.Context.Get(ctx, ContextRequest{
			ReturnSources:   Sources(LabelledGuidance("## Aggregator Guidance", "aggregator doctrine")),
			MaxBudget:       6000,
			OmitCurrentTime: true, // doctrine has no timestamps, and the evidence request above carries the line
		}); err == nil {
			aggGuidance = gr.Sources[SourceSkillGuidance]
		} else {
			log.Printf("[dag] aggregator guidance unavailable: %v", err)
		}
	}
	rolePrompt := fmt.Sprintf(prompt.Aggregator, aggGuidance, a.FormatRule(), intentStr)

	// Coverage edge (graph → aggregator): when gathering left gaps — tool steps
	// that came back empty or failed — frame them so absence is explicit and the
	// aggregator doesn't fabricate to fill the shape of the request. Gated: no
	// gaps → no edge, no cost.
	framed := a.FrameCoverage(ctx, graph, NewStagePrompts(rolePrompt, userPrompt))
	rolePrompt, userPrompt = framed.Role, framed.User

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
	raw, err := a.askStream(ctx, l, &llm.ChatRequest{
		Messages:    messages,
		Temperature: a.cfg.Temperature,
		MaxTokens:   aggMaxTokens,
	}, func(chunk, kind string) {
		evType := "outcome"
		if kind == "reasoning" {
			evType = "reasoning"
		}
		a.broadcastDAGEvent(graph, DAGEvent{Type: evType, Text: chunk})
	})

	trace := LLMTrace{
		RunID:    runIDFrom(ctx),
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
func (a *Agent) assembleAggregatorPrompt(trigger Trigger, graph *Graph, gateCtx *ContextResponse) string {
	var sb strings.Builder

	// Original Request — anchor the aggregator to what the user ACTUALLY
	// asked for. Without this, the aggregator drifts toward whatever narrative
	// dominates the worklog, which can include entries from prior investigations
	// in the same session. The worklog stays as secondary context (cross-query
	// continuity matters), but the query is the primary signal.
	if q := a.formatTrigger(trigger); q != "" {
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
