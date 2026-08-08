package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Compdeep/kaiju/agent/llm"
	"github.com/Compdeep/kaiju/agent/prompt"
	agenttools "github.com/Compdeep/kaiju/agent/tools"
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

// collectFetched returns the set of URLs that web_fetch nodes have already read
// (web_fetch stamps the fetched URL into its envelope Data). Subtracting these
// from the grounded set gives the URLs found-but-not-yet-read — which the edge
// pushes the planner to FETCH before it searches again.
func (a *Agent) collectFetched(graph *Graph) map[string]bool {
	out := map[string]bool{}
	if graph == nil {
		return out
	}
	for _, n := range graph.ResolvedByType(NodeTool) {
		tb, ok := n.Body.(toolMessageBody)
		if !ok {
			continue
		}
		env := tb.Envelope()
		if env.Kind != "page" || len(env.Data) == 0 {
			continue
		}
		var d struct {
			URL string `json:"url"`
		}
		if json.Unmarshal(env.Data, &d) == nil && d.URL != "" {
			out[d.URL] = true
		}
	}
	return out
}

// unretrievedGrounded returns the URLs a search returned that no fetch has read
// yet — the references the run knows about but never actually retrieved. It's the
// structural "found but not verified" set, shared by both edges: the grounding
// edge pushes the planner to READ them; the coverage edge tells the aggregator not
// to vouch for them. Pure envelope reading — no interpretation of meaning.
func (a *Agent) unretrievedGrounded(graph *Graph) []string {
	grounded := a.collectGrounded(graph)
	if len(grounded) == 0 {
		return nil
	}
	fetched := a.collectFetched(graph)
	var out []string
	for _, u := range grounded {
		if !fetched[u] {
			out = append(out, u)
		}
	}
	return out
}

// conclusionFloor is the generic hook the scheduler consults before it lets a
// reflection CONCLUDE. A "floor" is a hard, structural precondition that — while
// unmet — blocks conclusion and returns remediation steps to run first. The
// scheduler stays domain-agnostic: it grafts whatever steps come back and loops
// once, knowing nothing about what they are. ALL domain knowledge lives here.
//
// Today there is one floor — grounding: a run that found real URLs from a search
// but read NONE of them must read some before concluding, or the answer would rest
// on snippets or invention. The remediation is deterministic web_fetch steps for
// the top unread URLs (sourced from the search, never model-invented). Self-
// limiting — a run with no search has no grounded URLs, so nothing fires outside
// web work. A future floor adds a branch HERE, never in the scheduler.
func (a *Agent) conclusionFloor(graph *Graph, maxSteps int) (steps []PlanStep, label string) {
	// Grounding floor: found URLs, read none.
	if len(a.collectFetched(graph)) == 0 {
		urls := a.unretrievedGrounded(graph)
		found := len(urls)
		if maxSteps > 0 && len(urls) > maxSteps {
			urls = urls[:maxSteps]
		}
		for i, u := range urls {
			steps = append(steps, PlanStep{
				Tool:   "web_fetch",
				Tag:    fmt.Sprintf("grounding_fetch_%d", i+1),
				Params: map[string]any{"url": u},
			})
		}
		if len(steps) > 0 {
			return steps, fmt.Sprintf("grounding: %d source URLs found, none read — reading %d before concluding", found, len(steps))
		}
	}
	return nil, ""
}

// groundingEdge frames the hand-off from GATHERING into the REFLECTOR / next PLAN
// — the moment a planner, seeing a thin result, is tempted to fill the gap with
// URLs from memory. It biases toward READING what's already found: when a search
// returned real URLs that haven't been fetched yet, it says FETCH those before
// searching again; only when there are no unread leads does it say re-search.
// Gated on gaps: clean gathering pays nothing.
//
// Composition (same shape as the coverage edge): code decides WHETHER a gap exists
// and supplies the grounded/unfetched sets; the LLM reframes them against the
// request. On any LLM error it fails open to the structural note.
func (a *Agent) groundingEdge(ctx context.Context, graph *Graph, request string) string {
	gaps := a.collectGaps(graph)
	if len(gaps) == 0 {
		return "" // clean gathering — no fabrication pressure, no reframe
	}
	grounded := a.collectGrounded(graph)
	unfetched := a.unretrievedGrounded(graph)

	var structural string
	switch {
	case len(unfetched) > 0:
		var b strings.Builder
		for _, u := range unfetched {
			b.WriteString("- " + u + "\n")
		}
		structural = "You already have real URLs from a search that you have NOT read yet. FETCH these next — do NOT search again until you have read them:\n" + b.String() +
			"\nOnly a URL in this list may be fetched or cited. Searching more before reading what you already found wastes the run."
	case len(grounded) == 0:
		structural = "No URL has come from a real search yet. The next move is to broaden the search — plainer keywords, no stacked operators. Never fetch or cite a URL you did not get from a search."
	default:
		structural = "Every URL a search returned has already been fetched. If that isn't enough, broaden the search for new leads or report plainly what's still missing — never fetch or cite a URL that isn't in the evidence."
	}

	client, model := a.lightLane(ctx)
	if client == nil {
		return "## Grounding — read what you already found\n\n" + structural
	}

	groundedList := "(none)"
	if len(grounded) > 0 {
		groundedList = "- " + strings.Join(grounded, "\n- ")
	}
	unfetchedList := "(none — all grounded URLs already read)"
	if len(unfetched) > 0 {
		unfetchedList = "- " + strings.Join(unfetched, "\n- ")
	}
	var gapb strings.Builder
	for _, g := range gaps {
		label := g.Tag
		if label == "" {
			label = g.Kind
		}
		gapb.WriteString(fmt.Sprintf("- %s (%s): %s\n", label, g.Kind, strings.TrimSpace(g.Detail)))
	}
	user := fmt.Sprintf("REQUEST:\n%s\n\nUNFETCHED GROUNDED URLS (found by a search, not yet read):\n%s\n\nALL GROUNDED URLS:\n%s\n\nGATHERING GAPS:\n%s",
		Text.TruncateEvidence(request), unfetchedList, groundedList, gapb.String())
	grdReq := &llm.ChatRequest{
		Model:       model,
		Messages:    []llm.Message{{Role: "system", Content: prompt.GroundingGen}, {Role: "user", Content: user}},
		Temperature: 0.2,
		MaxTokens:   600,
	}
	a.capReply(resolvedModel(model, client), grdReq)
	resp, err := client.Complete(ctx, grdReq)
	if err != nil || resp == nil || len(resp.Choices) == 0 || strings.TrimSpace(resp.Choices[0].Message.Content) == "" {
		return "## Grounding — read what you already found\n\n" + structural // fail open
	}
	return "## Grounding — read what you already found\n" + strings.TrimSpace(resp.Choices[0].Message.Content)
}
