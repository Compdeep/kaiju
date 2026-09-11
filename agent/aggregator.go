package agent

import (
	"context"
	"fmt"
	"log"
	"strings"

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

	// Assemble user prompt from gate context and graph state.
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

	// The same account of the run the reflector gets, written for a stage that
	// is writing rather than deciding: what the evidence holds, and what it was
	// asked for and does not.
	rolePrompt, userPrompt = WithReframe(rolePrompt, userPrompt,
		a.EdgeReFrame(ctx, graph, userPrompt,
			ReframeToAnswer))

	// The arcs, alongside the prose. This stage writes the answer a person
	// reads, and it wrote it from a rendering OF the evidence rather than from
	// the evidence — so a step that returned {"count":0} reached it as a header
	// row with no rows, and the answer said the data was unusable.
	//
	// The history still leads, and the prose still carries the content: a
	// payload holds a tool's declared fields and never its text. The two are
	// disjoint by construction — see NodeBody.Payload and Evidence.
	messages := BuildMessagesWithResults(
		ComposeSystemPrompt(a.soulPrompt, rolePrompt),
		userPrompt,
		history,
		graph.Arcs(),
	)

	// Stream the aggregator response, broadcasting each chunk for live display.
	// Use 2x configured MaxTokens — the aggregator synthesizes all evidence
	// into a full response and needs more output room than individual tool calls.
	aggMaxTokens := a.cfg.MaxTokens * 2
	if aggMaxTokens < 8192 {
		aggMaxTokens = 8192
	}
	aggID := TraceID{
		NodeID:   "aggregator",
		NodeType: "aggregator",
		Tag:      "synthesize",
		Input:    map[string]string{"intent": intentStr},
	}
	if gateCtx != nil {
		aggID.GateReturned = gateCtx.Sources
	}
	raw, err := a.askStream(withTrace(ctx, aggID), l, &llm.ChatRequest{
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

	// The stage that writes the answer a person reads, recorded like any other —
	// see debugrecord.go. It is not a node, so it records itself.
	rec := DebugRecord{
		ID: "aggregator", Kind: "aggregator", Label: "synthesize", Round: graph.Round(),
		System: ComposeSystemPrompt(a.soulPrompt, rolePrompt), User: userPrompt,
	}
	if gateCtx != nil {
		rec.Gate = gateCtx.Sources
	}
	if err != nil {
		rec.Err = err.Error()
		graph.recordStage(rec)
		return "", nil, fmt.Errorf("aggregator LLM call: %w", err)
	}
	if raw == "" {
		rec.Err = "empty response"
		graph.recordStage(rec)
		return "", nil, fmt.Errorf("aggregator LLM returned empty response")
	}
	rec.Reply, rec.Text = raw, raw
	graph.recordStage(rec)

	log.Printf("[dag] aggregator output: %s", Text.TruncateLog(raw, 300))

	// Aggregator outputs plain markdown — no JSON parsing needed.
	return raw, nil, nil
}

// assembleAggregatorPrompt builds the aggregator's user message from a gate
// response and graph state. Sections: Original Request, Failed Steps,
// Skipped Steps, All Results, Worklog.
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
