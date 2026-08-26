package agent

import "github.com/Compdeep/kaiju/agent/toolapi"

// Every cap on how much text reaches a model, in one place.
//
// There are two kinds of number in this engine and they were mixed together.
//
// The gate's per-stage budgets — 24000 for a reflection, 4000 for an observer —
// are RATIOS. They say the reflector needs six times what the observer does,
// they already scale together against the window, and they belong at the call
// site that knows what its stage is for.
//
// The four below are ABSOLUTE caps on content, and each was a constant in a
// different file, set at a different time, unaware of the others. A string
// tool's result was cut at 4096 while a typed tool's was cut at 8000 for the
// same work; a page fetched whole was cut to 8000 before the gate that decides
// what a prompt holds had seen it. Nothing said why, because the numbers had
// nowhere to be read together.
//
// They are here so they can be argued with as a set, and so a change to one is
// visibly a change relative to the rest.

// budgetSpec is one cap: what a deployment gets with no model catalog, how it
// grows with the window, and where it stops.
type budgetSpec struct {
	// Base is the floor, in characters. What every deployment gets, and what a
	// deployment with no catalog keeps — this must stay inert unless an
	// application opts in by supplying window sizes.
	Base int

	// Share is the denominator: one Share-th of the model's window, once that
	// is larger than Base. A bigger number is a smaller slice.
	Share int

	// Ceiling is the most it ever grows to, whatever the window.
	//
	// Not a capacity limit. A model reading more does not answer better past a
	// point, and a single step filling a prompt is worse than several steps
	// each keeping the part that mattered.
	Ceiling int

	// Bounds is what this cap cuts, in a line.
	Bounds string
}

// The table. Base values are what each was before this file existed, so a
// deployment with no catalog sees no change from any of it.
var (
	// evidenceBudget bounds ONE step's contribution to a prompt. A run with
	// twenty steps sends twenty of these, and the gate's own budget trims the
	// total afterwards by fair share.
	evidenceBudget = budgetSpec{
		Base: toolapi.EvidenceBudget, Share: 16, Ceiling: 32000,
		Bounds: "one step's result reaching a prompt",
	}

	// toolResultBudget bounds one string tool's result as it finishes. A typed
	// tool skips it — the typed branch sets isContextual — so this only ever
	// applied to half the tools, at half the evidence cap, for no stated reason.
	toolResultBudget = budgetSpec{
		Base: 4096, Share: 32, Ceiling: 16000,
		Bounds: "one string tool's result at dispatch",
	}

	// payloadBudget bounds a node's fields carried as data: the tool messages a
	// stage is given, and the payload the trace shows.
	payloadBudget = budgetSpec{
		Base: 12000, Share: 12, Ceiling: 48000,
		Bounds: "one node's fields, as data",
	}

	// toolIndexBudget bounds the planner's tool index — signatures and return
	// shapes. It decides how many tools a planner can see at all, which is the
	// whole question once a registry outgrows a prompt.
	toolIndexBudget = budgetSpec{
		Base: 24000, Share: 8, Ceiling: 96000,
		Bounds: "the tool index shown to the planner",
	}
)

/*
 * budget resolves one cap against the deployed model's window.
 * desc: Base ≤ window/Share ≤ Ceiling. An unknown window gives Base, so this is
 *       inert until an application supplies a catalog through Config.Limits.
 *
 *       Where two lanes run different models the smaller window decides, since
 *       one number serves whichever stage is asking and overshooting the
 *       smaller model is the failure that matters.
 * param: s - which cap.
 * return: the cap, in characters.
 */
func (a *Agent) budget(s budgetSpec) int {
	if a == nil {
		return s.Base
	}
	window := a.smallestKnownWindow()
	if window <= 0 {
		return s.Base
	}
	got := window * charsPerToken / s.Share
	if got < s.Base {
		return s.Base
	}
	if got > s.Ceiling {
		return s.Ceiling
	}
	return got
}
