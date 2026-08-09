package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/Compdeep/kaiju/agent/llm"
	"github.com/Compdeep/kaiju/agent/prompt"
	agenttools "github.com/Compdeep/kaiju/agent/tools"
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
			continue // not a tool result — nothing structural to read
		}
		env := tb.Envelope()
		switch env.Status {
		case agenttools.StatusEmpty, agenttools.StatusError:
			gaps = append(gaps, toolGap{Tag: n.Tag, Kind: env.Type, Detail: env.Detail})
		case agenttools.StatusUnclassified:
			// The tool ran and returned something; it did not say whether that
			// something was a finding. Stating it is honest — the alternative is
			// silence, which reads to every consumer as "this one was fine".
			gaps = append(gaps, toolGap{Tag: n.Tag, Kind: env.Type,
				Detail: "the tool did not report whether it found anything; read its output directly"})
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

// runFanout counts how many tool nodes actually resolved — a structural proxy for
// how complex the run turned out to be, independent of the preflight's guess. A
// query that fanned out to many gather steps earned a real synthesis even if
// preflight underestimated it.
func (a *Agent) runFanout(graph *Graph) int {
	if graph == nil {
		return 0
	}
	return len(graph.ResolvedByType(NodeTool))
}

// hasUsableEvidence reports whether any resolved tool node returned a successful
// (ok) result. A run that gathered nothing usable has nothing to synthesize — the
// reflector's honest "couldn't get the data" verdict is the right answer, not an
// aggregator pass over emptiness.
func (a *Agent) hasUsableEvidence(graph *Graph) bool {
	if graph == nil {
		return false
	}
	for _, n := range graph.ResolvedByType(NodeTool) {
		tb, ok := n.Body.(toolMessageBody)
		if !ok {
			continue
		}
		switch tb.Envelope().Status {
		case agenttools.StatusOK, agenttools.StatusUnclassified:
			// Unclassified counts. The tool ran and returned something readable;
			// all that is missing is its own word on whether that counts as a
			// finding. Excluding it would tell decideAutoAggMode there is nothing
			// to aggregate on a run whose evidence came entirely from tools that
			// do not declare an outcome — which, until the tools are migrated, is
			// most of them.
			return true
		}
	}
	return false
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
	unretrieved := a.unretrievedGrounded(graph)
	if len(gaps) == 0 && len(unretrieved) == 0 {
		return "" // clean gathering — no edge; aggregator runs exactly as before
	}

	// Part 1 — gaps (steps that came back empty/failed). Same as before: code
	// supplies the structural gap list, the LLM reframes it against the request.
	var out string
	if len(gaps) > 0 {
		var gb strings.Builder
		for _, g := range gaps {
			label := g.Tag
			if label == "" {
				label = g.Kind
			}
			gb.WriteString(fmt.Sprintf("- %s (%s): %s\n", label, g.Kind, strings.TrimSpace(g.Detail)))
		}
		structural := "These gathering steps returned nothing usable — treat what they were meant to retrieve as unavailable, and do not fabricate to fill them:\n" + gb.String()

		out = "## Coverage — what the evidence can and can't back\n\n" + structural
		if client, model := a.lightLane(ctx); client != nil {
			user := fmt.Sprintf("REQUEST + EVIDENCE:\n%s\n\nGATHERING GAPS:\n%s", Text.TruncateEvidence(evidence), gb.String())
			covReq := &llm.ChatRequest{
				Model:       model,
				Messages:    []llm.Message{{Role: "system", Content: prompt.CoverageGen}, {Role: "user", Content: user}},
				Temperature: 0.2,
				MaxTokens:   700,
			}
			a.capReply(resolvedModel(model, client), covReq)
			resp, err := client.Complete(ctx, covReq)
			if err == nil && resp != nil && len(resp.Choices) > 0 && strings.TrimSpace(resp.Choices[0].Message.Content) != "" {
				out = "## Coverage — what the evidence can and can't back\n" + strings.TrimSpace(resp.Choices[0].Message.Content)
			}
		}
	}

	// Part 2 — referenced but not retrieved. The code half of the grounding edge
	// already knows which URLs a search surfaced that no fetch ever read. Hand that
	// structural fact to the aggregator so it can't present a merely-referenced
	// source as one it actually read/verified. Deterministic (not LLM-reframed):
	// it's a hard honesty constraint, so it must not be softened or dropped.
	if len(unretrieved) > 0 {
		note := "## Referenced but not retrieved\n\nThe gathering surfaced these as references but NEVER retrieved them, so their content and availability are unconfirmed. State nothing about what they contain, and do not describe them as verified, confirmed, checked, read, or accessible — list them only as unverified references if the request needs them:\n- " + strings.Join(unretrieved, "\n- ")
		if out == "" {
			out = note
		} else {
			out = out + "\n\n" + note
		}
	}
	return out
}
