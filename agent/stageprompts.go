package agent

import (
	"context"

	"github.com/Compdeep/kaiju/agent/prompt"
)

// StagePrompts is the system/user pair an LLM stage sends, with the edges
// applied to it.
//
// Every edge does the same two things: it prepends its block to the input, and
// it appends its hook to the system prompt so the stage is told how to read that
// block. Written out at each stage that wants one, that is three lines to get
// right per edge per stage — and getting one wrong is silent, because a stage
// given the block without the hook still answers, just without knowing the block
// is authoritative.
//
// It also fixes the input each edge reframes against. An edge is asked what the
// evidence does and does not back, so it must see the stage's own request and
// evidence — not the block a previous edge already prepended. Holding that
// string here is what keeps the second edge correct.
//
// Add an edge by writing one Frame method beside the two below. A stage adopts
// it by adding one line, and cannot half-adopt it.
type StagePrompts struct {
	// Role is the system prompt. Each applied edge appends its hook.
	Role string
	// User is the input. Each applied edge prepends its block, so the most
	// recently applied edge is read first.
	User string

	// input is the request and evidence as the stage assembled them, kept
	// unchanged as edges are applied.
	input string
}

/*
 * NewStagePrompts pairs a stage's two prompts so edges can be applied to them.
 * param: role - the system prompt for the stage.
 * param: user - the assembled request and evidence.
 * return: the pair, with no edges applied yet.
 */
func NewStagePrompts(role, user string) StagePrompts {
	return StagePrompts{Role: role, User: user, input: user}
}

/*
 * withEdge applies one edge's block and hook.
 * desc: An empty block means the edge did not fire — every edge is gated on the
 *       run's shape, and a clean run pays nothing — so the pair is returned
 *       unchanged and the stage runs exactly as it would have.
 * param: block - the edge's output, or "" when it did not fire.
 * param: hook - the wording that tells the stage how to read that block.
 * return: the pair with the edge applied.
 */
func (p StagePrompts) withEdge(block, hook string) StagePrompts {
	if block == "" {
		return p
	}
	p.User = block + "\n\n" + p.User
	p.Role = p.Role + "\n\n" + hook
	return p
}

/*
 * FrameCoverage applies the coverage edge: when gathering left gaps — tool steps
 * that came back empty or failed, or sources referenced but never retrieved —
 * the stage is told which parts of the request the evidence backs and which it
 * does not, so it reports the gap instead of inventing a detail to fill it.
 * desc: Exported because the stage that writes the answer is not always one of
 *       this package's. An application with its own answering stage — one that
 *       returns a verdict rather than prose, say — needs the same framing, and
 *       an answer written without it is the one most likely to be fabricated,
 *       because it is written on the evidence that failed to arrive.
 * param: ctx - cancelled with the run; the edge's own LLM call honours it.
 * param: graph - the run so far, read for empty, failed and unretrieved steps.
 * param: p - the stage's prompts.
 * return: the prompts, framed when there were gaps and unchanged when there
 *         were none.
 */
func (a *Agent) FrameCoverage(ctx context.Context, graph *Graph, p StagePrompts) StagePrompts {
	return p.withEdge(a.coverageEdge(ctx, graph, p.input), prompt.CoverageHook)
}

/*
 * FrameGrounding applies the grounding edge: it lists the URLs a real search
 * returned this run and which have not been read yet, so the next step fetches
 * what it already found instead of naming a source from memory.
 * param: ctx - cancelled with the run.
 * param: graph - the run so far, read for searched and fetched URLs.
 * param: p - the stage's prompts.
 * return: the prompts, framed when there were unread grounded URLs.
 */
func (a *Agent) FrameGrounding(ctx context.Context, graph *Graph, p StagePrompts) StagePrompts {
	return p.withEdge(a.groundingEdge(ctx, graph, p.input), prompt.GroundingHook)
}
