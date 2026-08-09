package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/Compdeep/kaiju/agent/llm"
	"github.com/Compdeep/kaiju/agent/prompt"
)

// The grounding edge's two prompts live in prompt/prompts.md (sections
// GROUNDING_GEN and GROUNDING_HOOK), host-overridable like every other stage —
// exposed as prompt.GroundingGen (the generator this file runs) and
// prompt.GroundingHook (appended to the reflector/planner role prompt when it
// fires).

// collectGrounded returns every handle the run surfaced — a URL from a search,
// an id from a listing, whatever a tool declared as a reference in its output
// schema. See references.go: the engine does not know what any of them mean, and
// no tool is named here.
func (a *Agent) collectGrounded(graph *Graph) []string {
	refs := a.collectReferences(graph)
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		out = append(out, r.Value)
	}
	return out
}

// unretrievedGrounded returns the handles no later step passed to anything.
func (a *Agent) unretrievedGrounded(graph *Graph) []string {
	refs := a.unresolvedReferences(graph)
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		out = append(out, r.Value)
	}
	return out
}

// conclusionFloor is the generic hook the scheduler consults before it lets a
// reflection CONCLUDE. A "floor" is a hard, structural precondition that — while
// unmet — blocks conclusion and returns remediation steps to run first. The
// scheduler stays domain-agnostic: it grafts whatever steps come back and loops
// once, knowing nothing about what they are. ALL domain knowledge lives here.
//
// Today one floor fires: a run that surfaced handles on things it could retrieve
// — URLs from a search, ids from a listing — and retrieved none of them. Its
// answer would rest on what the handles were listed beside rather than on what
// they lead to.
//
// The remediation is built from what the producing tool declared: the reference
// annotation names the tool that follows a handle and the parameter it goes in,
// so no tool is named here. A handle whose producer declared no follow-up is
// still reported by the coverage edge; it just cannot be acted on automatically.
// Self-limiting — a run that surfaced no handles has no floor to clear.
func (a *Agent) conclusionFloor(graph *Graph, maxSteps int) (steps []PlanStep, label string) {
	all := a.collectReferences(graph)
	unresolved := a.unresolvedReferences(graph)
	if len(all) == 0 || len(unresolved) != len(all) {
		return nil, "" // nothing surfaced, or something was already followed
	}
	found := len(unresolved)
	if maxSteps > 0 && len(unresolved) > maxSteps {
		unresolved = unresolved[:maxSteps]
	}
	for i, r := range unresolved {
		if r.Tool == "" || r.Param == "" {
			continue // declared as a handle, but not how to follow it
		}
		steps = append(steps, PlanStep{
			Tool:   r.Tool,
			Tag:    fmt.Sprintf("grounding_retrieve_%d", i+1),
			Params: map[string]any{r.Param: r.Value},
		})
	}
	if len(steps) > 0 {
		return steps, fmt.Sprintf("grounding: %d references surfaced, none retrieved — retrieving %d before concluding", found, len(steps))
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
