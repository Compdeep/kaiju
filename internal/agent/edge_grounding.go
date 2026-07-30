package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Compdeep/kaiju/internal/agent/llm"
	"github.com/Compdeep/kaiju/internal/agent/prompt"
	agenttools "github.com/Compdeep/kaiju/internal/agent/tools"
)

// The grounding edge's two prompts live in prompt/prompts.md (sections
// GROUNDING_GEN and GROUNDING_HOOK), host-overridable like every other stage —
// exposed as prompt.GroundingGen (the generator this file runs) and
// prompt.GroundingHook (appended to the reflector/planner role prompt when it
// fires).

// collectGrounded is the CODE half of the grounding edge: it walks the graph and
// gathers every URL that actually came out of a web_search result — the only URLs
// a later step may safely fetch or cite. A URL NOT in this set, typed into a fetch
// or an answer, is invented. Purely structural: it reads the search envelopes and
// interprets no meaning, so the edge stays content-agnostic on the gate side.
func (a *Agent) collectGrounded(graph *Graph) []string {
	if graph == nil {
		return nil
	}
	var urls []string
	seen := map[string]bool{}
	for _, n := range graph.ResolvedByType(NodeTool) {
		tb, ok := n.Body.(toolMessageBody)
		if !ok {
			continue // not on the protocol — no structured results to read
		}
		env := tb.Envelope()
		if env.Kind != "search" || env.Status != agenttools.StatusOK || len(env.Data) == 0 {
			continue
		}
		var d struct {
			Results []struct {
				URL string `json:"url"`
			} `json:"results"`
		}
		if json.Unmarshal(env.Data, &d) != nil {
			continue
		}
		for _, r := range d.Results {
			u := strings.TrimSpace(r.URL)
			if u != "" && !seen[u] {
				seen[u] = true
				urls = append(urls, u)
			}
		}
	}
	return urls
}

// groundingEdge frames the hand-off from GATHERING into the REFLECTOR / next PLAN
// — the moment a planner, seeing a thin result, is tempted to fill the gap with
// URLs from memory. When a search came back empty or a step failed, it prepends a
// "## Grounding" note listing the ONLY URLs a real search actually returned, so
// the next step fetches/cites from those or searches for more, never invents.
// Gated on gaps: clean gathering pays nothing.
//
// Composition (same shape as the coverage edge): code decides WHETHER a gap exists
// and supplies the grounded set; the LLM reframes it against the request. On any
// LLM error it fails open to the structural grounded list alone.
func (a *Agent) groundingEdge(ctx context.Context, graph *Graph, request string) string {
	gaps := a.collectGaps(graph)
	if len(gaps) == 0 {
		return "" // clean gathering — no fabrication pressure, no reframe
	}
	grounded := a.collectGrounded(graph)

	var gb strings.Builder
	if len(grounded) == 0 {
		gb.WriteString("(none — no URL has come back from a real search yet)\n")
	} else {
		for _, u := range grounded {
			gb.WriteString("- " + u + "\n")
		}
	}
	structural := "URLs that actually came from a search — the ONLY ones safe to fetch or cite:\n" + gb.String() +
		"\nDo not fetch or cite any URL not in this list. To add a source, SEARCH for it — never type a URL from memory."

	client, model := a.lightLane(ctx)
	if client == nil {
		return "## Grounding — the only real leads so far\n\n" + structural
	}

	var gapb strings.Builder
	for _, g := range gaps {
		label := g.Tag
		if label == "" {
			label = g.Kind
		}
		gapb.WriteString(fmt.Sprintf("- %s (%s): %s\n", label, g.Kind, strings.TrimSpace(g.Detail)))
	}
	user := fmt.Sprintf("REQUEST:\n%s\n\nGROUNDED URLS (from real searches):\n%s\nGATHERING GAPS (returned nothing usable):\n%s",
		Text.TruncateEvidence(request), gb.String(), gapb.String())
	resp, err := client.Complete(ctx, &llm.ChatRequest{
		Model:       model,
		Messages:    []llm.Message{{Role: "system", Content: prompt.GroundingGen}, {Role: "user", Content: user}},
		Temperature: 0.2,
		MaxTokens:   600,
	})
	if err != nil || resp == nil || len(resp.Choices) == 0 || strings.TrimSpace(resp.Choices[0].Message.Content) == "" {
		return "## Grounding — the only real leads so far\n\n" + structural // fail open
	}
	return "## Grounding — the only real leads so far\n" + strings.TrimSpace(resp.Choices[0].Message.Content)
}
