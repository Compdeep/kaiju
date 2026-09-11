package llm

import (
	"slices"
	"time"
)

// Deciding what to ask of a model's thinking, once, for one request.
//
// The caller says what it wants in its own terms; the catalog says what this
// model does with any of it; this turns the two into what goes on the wire.
// Nothing below is asked of a model the catalog cannot answer for.

// resolved is the answer: what to say about thinking, and how long to wait.
type resolved struct {
	// Say is whether to send an instruction at all. False leaves the model's
	// own default alone, which is not the same as asking for none.
	Say bool
	// Think is what that instruction says, when Say.
	Think bool
	// Effort and Budget are asked for only where the catalog says this model
	// acts on them. Zero values ask for nothing.
	Effort Effort
	Budget int
	// Wait multiplies the ordinary deadline. Always 1 or more.
	Wait float64
}

// maxWait is the largest multiple a request deadline can reach here: the
// slowest model the catalog carries. The connection ceiling is set from it, so
// a deadline this package allows is never cut short by the transport under it.
//
// The EFFORT ladder is deliberately not part of this. The deadline here answers
// "has the provider stopped answering", and 300 or 600 seconds is that question
// whatever effort was asked for. How long a piece of WORK may take is a
// different question with a different owner — the round deadline, above this
// package — and putting the ladder here would also loosen the ceiling for every
// streamed call, which is bounded by it alone.
const maxWait = 2

/*
 * resolveReasoning decides what this request asks of the model's thinking.
 * desc: Four steps, and the order is the design.
 *
 *       1. A request that forces ONE shape does not think unless it says so.
 *          Its reply is bounded by a schema and its thinking is not: measured
 *          over 257 calls on a trivial prompt, reasoning ran to a median of 139
 *          tokens and a maximum of 16,002. The median fits any budget and the
 *          tail fits none, so this cannot be decided by comparing the cap to a
 *          number — whatever number is chosen, the tail beats it. It is decided
 *          by what the call is: a forced shape wants a small exact answer, and
 *          a stage that wants it reasoned about says WantOn. The planner does.
 *
 *       2. Then what the caller asked for.
 *
 *       3. Then what the model can do. An instruction it ignores is worse than
 *          none, because it reads as a setting that works.
 *
 *       4. Then the clock, which carries the effort for every model that does
 *          not act on the word itself.
 * param: req - the request as the caller wrote it.
 * param: f - what the catalog knows about the model that will answer.
 * param: known - false when the catalog cannot answer for it at all.
 * return: what to put on the wire, and how long to allow.
 */
func resolveReasoning(req *ChatRequest, f ModelFacts, known bool) resolved {
	out := resolved{Wait: 1}
	if req == nil {
		return out
	}

	want, effort, budget := WantAuto, EffortUnset, 0
	if req.Think != nil {
		want, effort, budget = req.Think.Want, req.Think.Effort, req.Think.Budget
	}

	// 1. A forced single shape, with nobody saying otherwise.
	if want == WantAuto && asksForOneShape(req) {
		want = WantOff
	}

	// Nothing is known about this model, so nothing is asked of it. Every
	// application that supplies no catalog stops here, unchanged.
	if !known {
		return out
	}

	// 2 and 3. What was asked, narrowed to what this model does with it.
	switch want {
	case WantOff:
		// Saying "off" to a model that cannot stop is a request that reads as
		// success and changes nothing. Better to say nothing and let the caller
		// find out from the trace than to record an instruction that was never
		// honoured.
		if f.Thinking.Optional {
			out.Say, out.Think = true, false
		}
	case WantOn:
		out.Say, out.Think = true, true
	}

	thinking := out.Say && out.Think || !out.Say && f.Thinking.Default
	if !thinking {
		// Nothing left to narrow: an effort and a budget bound thinking that is
		// going to happen, and none is.
		return out
	}

	if effort != EffortUnset && slices.Contains(f.Thinking.Efforts, effort) {
		out.Effort = effort
	}
	if budget > 0 && f.Thinking.Budget {
		out.Budget = budget
	}

	// 4. The clock: what this model's measured pace earns it.
	out.Wait = paceOf(f)
	return out
}

// paceOf is the model's own multiplier, never below 1: a measurement lengthens
// a deadline and never shortens one, so a wrong entry costs waiting rather than
// an answer.
func paceOf(f ModelFacts) float64 {
	if f.Thinking.Pace > 1 {
		return f.Thinking.Pace
	}
	return 1
}

/*
 * deadline is how long this request may take.
 * desc: The ordinary allowance, doubled when the reply will carry reasoning —
 *       the hidden tokens are generated at the same rate as the visible ones
 *       and the caller waits through every one — then multiplied by what the
 *       effort and the model's pace earn.
 * param: r - the resolved reasoning.
 * param: f - the catalog's answer for this model.
 * param: known - whether the catalog answered at all.
 * return: the deadline for one call.
 */
func deadline(r resolved, f ModelFacts, known bool) time.Duration {
	base := requestTimeout
	if r.Say && r.Think || !r.Say && known && f.Thinking.Default {
		base = thinkingRequestTimeout
	}
	if r.Wait <= 1 {
		return base
	}
	return time.Duration(float64(base) * r.Wait)
}

/*
 * applyReasoning settles what this request says about thinking.
 * desc: Run before the schema conversion in Complete, because that removes the
 *       Tools and ToolChoice which are how a request says it forces one shape.
 *
 *       It sets the wire field and never reads it: what a caller wants lives in
 *       Think, and ReasoningControl is this package's own answer to it.
 * param: req - the request, whose Reasoning is set in place.
 */
func (c *Client) applyReasoning(req *ChatRequest) {
	if c == nil || req == nil {
		return
	}
	model := req.Model
	if model == "" {
		model = c.model
	}
	f, known := c.facts(model)
	req.Reasoning = resolveReasoning(req, f, known).wire()
}

/*
 * wire is the resolved reasoning as the OpenAI-compatible parameter, or nil
 * when there is nothing to say.
 * desc: Nil rather than an empty object: a request that asks for nothing must
 *       carry no reasoning key at all, which is what leaves a model's own
 *       default alone.
 * return: the parameter, or nil.
 */
func (r resolved) wire() *ReasoningControl {
	if !r.Say && r.Effort == EffortUnset && r.Budget == 0 {
		return nil
	}
	out := &ReasoningControl{Effort: r.Effort.String(), MaxTokens: r.Budget}
	if r.Say {
		think := r.Think
		out.Enabled = &think
	}
	return out
}
