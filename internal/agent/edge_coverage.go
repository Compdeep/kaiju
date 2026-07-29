package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/Compdeep/kaiju/internal/agent/llm"
	agenttools "github.com/Compdeep/kaiju/internal/agent/tools"
)

// coverageHook is appended to the aggregator's role prompt when the coverage
// edge fires: the "## Coverage" block is authoritative about what the evidence
// backs, so an absence is reported, never fabricated.
const coverageHook = "\n\nA \"## Coverage\" block is prepended to your input. It is authoritative about which parts of the request the gathered evidence backs and which it does not. Support the backed parts from the evidence; for anything it marks as NOT backed, say plainly it could not be found or retrieved — never invent a detail, link, figure, quote, or date to fill the gap. A gap honestly reported is the correct, complete answer, not a failure."

// coverageGenPrompt is the content-agnostic generator: it writes the coverage
// checklist for THIS request against THIS evidence, using the structural gaps
// code already found. It never answers the request and adds nothing of its own.
const coverageGenPrompt = `You are the glue between an evidence-gathering stage and a final answer-writer. You are given the user's REQUEST + EVIDENCE and the GATHERING GAPS (steps that returned nothing usable). Do NOT answer the request. Write a short checklist so the answer-writer reports only what the evidence backs and never invents the rest.

For each concrete thing the request asks for, decide from the EVIDENCE whether it is backed (written out in the evidence) or not — the GATHERING GAPS tell you which parts could not be retrieved. Output exactly:

BACKED: <each requested thing the evidence literally contains>
NOT BACKED: <each requested thing the evidence does not contain — to be reported as not found, never invented>

Reference only what is in the evidence. Add nothing of your own.`

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

// gapBlock renders the content-agnostic structural gap list — the gathering
// steps that came back empty or failed — or "" when gathering was clean. This
// is code-only (no LLM), so it is cheap enough to attach to every reflection.
func (a *Agent) gapBlock(graph *Graph) string {
	gaps := a.collectGaps(graph)
	if len(gaps) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Gaps — gathering steps that returned nothing usable\n")
	b.WriteString("Treat what these were meant to retrieve as unavailable; report it as not found, never fabricate to fill it:\n")
	for _, g := range gaps {
		label := g.Tag
		if label == "" {
			label = g.Kind
		}
		b.WriteString(fmt.Sprintf("- %s (%s): %s\n", label, g.Kind, strings.TrimSpace(g.Detail)))
	}
	return b.String()
}

// reflectorGapHook reminds the reflector that a conclude verdict must honour the
// gaps — the reflector often writes the final answer directly, bypassing the
// aggregator (and its coverage edge). This is the reflector-conclude counterpart
// to the aggregator's coverageHook.
const reflectorGapHook = "\n\nSome gathering steps returned nothing usable (see \"## Gaps\"). If you conclude, your verdict must report those as not found or not retrieved — never invent a link, figure, quote, or date to fill a gap. A gap honestly reported is a complete, correct answer, not a failure."

// coverageEdge frames the graph→aggregator hand-off. When gathering left gaps
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
		Messages:    []llm.Message{{Role: "system", Content: coverageGenPrompt}, {Role: "user", Content: user}},
		Temperature: 0.2,
		MaxTokens:   700,
	})
	if err != nil || resp == nil || len(resp.Choices) == 0 || strings.TrimSpace(resp.Choices[0].Message.Content) == "" {
		return "## Coverage — what the evidence can and can't back\n\n" + structural // fail open
	}
	return "## Coverage — what the evidence can and can't back\n" + strings.TrimSpace(resp.Choices[0].Message.Content)
}
