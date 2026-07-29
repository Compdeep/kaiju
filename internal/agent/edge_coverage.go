package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/Compdeep/kaiju/internal/agent/llm"
	"github.com/Compdeep/kaiju/internal/agent/prompt"
	agenttools "github.com/Compdeep/kaiju/internal/agent/tools"
)

// The coverage edge's two prompts live in prompt/prompts.md like every other
// stage prompt (sections COVERAGE_GEN and COVERAGE_HOOK), so a host can override
// them without rebuilding. They are exposed as prompt.CoverageGen (the generator
// this file runs) and prompt.CoverageHook (appended to the aggregator/reflector
// role prompt when a gap is framed).

// toolGap is a gathering step that produced nothing usable.
type toolGap struct {
	Tag    string
	Kind   string
	Detail string
}

// collectGaps is the CODE half of the coverage edge: it reads the ToolMessage
// envelope Status signals (empty/error) and failed nodes off the graph — the
// structural shape of what ran, with no interpretation of meaning. This is what
// makes the edge content-agnostic on the gate side.
func (a *Agent) collectGaps(graph *Graph) []toolGap {
	if graph == nil {
		return nil
	}
	var gaps []toolGap
	for _, n := range graph.ResolvedByType(NodeTool) {
		tb, ok := n.Body.(toolMessageBody)
		if !ok {
			continue // not yet on the protocol — no structural signal to read
		}
		env := tb.Envelope()
		if env.Status == agenttools.StatusEmpty || env.Status == agenttools.StatusError {
			gaps = append(gaps, toolGap{Tag: n.Tag, Kind: env.Kind, Detail: env.Detail})
		}
	}
	for _, n := range graph.FailedNodes() {
		detail := ""
		if n.Error != nil {
			detail = n.Error.Error()
		}
		gaps = append(gaps, toolGap{Tag: n.Tag, Kind: n.ToolName, Detail: detail})
	}
	return gaps
}

// coverageEdge frames the hand-off into whichever node writes the answer. When gathering left gaps
// (tool steps that came back empty or failed), it produces a "## Coverage"
// checklist so absence is explicit to the aggregator and it does not fabricate
// to fill the shape of the request. Returns "" when gathering was clean — the
// edge is gated, so the common path pays nothing.
//
// Composition: code decides WHETHER (collectGaps reads structural Status
// signals); the LLM decides WHAT the gap means against the request. On any LLM
// error it fails open to the structural gap list alone — the aggregator still
// gets an explicit absence signal.
func (a *Agent) coverageEdge(ctx context.Context, graph *Graph, evidence string) string {
	gaps := a.collectGaps(graph)
	if len(gaps) == 0 {
		return "" // clean gathering — no edge; aggregator runs exactly as before
	}

	var gb strings.Builder
	for _, g := range gaps {
		label := g.Tag
		if label == "" {
			label = g.Kind
		}
		gb.WriteString(fmt.Sprintf("- %s (%s): %s\n", label, g.Kind, strings.TrimSpace(g.Detail)))
	}
	structural := "These gathering steps returned nothing usable — treat what they were meant to retrieve as unavailable, and do not fabricate to fill them:\n" + gb.String()

	client, model := a.lightLane(ctx)
	if client == nil {
		return "## Coverage — what the evidence can and can't back\n\n" + structural
	}
	user := fmt.Sprintf("REQUEST + EVIDENCE:\n%s\n\nGATHERING GAPS:\n%s", Text.TruncateEvidence(evidence), gb.String())
	resp, err := client.Complete(ctx, &llm.ChatRequest{
		Model:       model,
		Messages:    []llm.Message{{Role: "system", Content: prompt.CoverageGen}, {Role: "user", Content: user}},
		Temperature: 0.2,
		MaxTokens:   700,
	})
	if err != nil || resp == nil || len(resp.Choices) == 0 || strings.TrimSpace(resp.Choices[0].Message.Content) == "" {
		return "## Coverage — what the evidence can and can't back\n\n" + structural // fail open
	}
	return "## Coverage — what the evidence can and can't back\n" + strings.TrimSpace(resp.Choices[0].Message.Content)
}
