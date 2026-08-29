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

	// Weight is how much of the deployment's prompt scale this cap feels, from
	// 0 to 1. It exists because the caps are not alike: some bound bulk that a
	// model reads and mostly ignores, and some bound the thing a stage came to
	// say. Scaling those together would trade a prompt that is too big for a
	// reply that is cut off.
	//
	//	1    bulk — evidence, a tool's result, a node's fields. Trimming these
	//	     shortens what a stage reads and changes nothing it can do.
	//	0    exempt — the tool index, a generated program, anything whose
	//	     truncation removes a capability or breaks a syntax rather than
	//	     making text shorter.
	//
	// Zero is the safe default: a spec that says nothing about weight does not
	// move when a deployment scales, so adding one here is a deliberate act.
	Weight float64
}

/*
 * resolve applies a deployment's prompt scale to a resolved cap.
 * desc: Base is a floor the scale may not breach — it is what a deployment with
 *       no model catalog is given, so it is the size this engine is known to
 *       work at, and no scaling argument is worth going below it.
 *
 *       A scale of 1 returns the cap unchanged whatever the weight, so a
 *       deployment that sets nothing sees exactly the numbers it saw before
 *       this field existed.
 * param: got - the cap resolved against the window, before scaling.
 * param: scale - the deployment's prompt scale, 0 < scale <= 1.
 * return: the cap to use.
 */
func (s budgetSpec) resolve(got int, scale float64) int {
	if got > s.Ceiling {
		got = s.Ceiling
	}
	if scale > 0 && scale < 1 && s.Weight > 0 {
		effect := 1 - s.Weight*(1-scale)
		got = int(float64(got) * effect)
	}
	if got < s.Base {
		got = s.Base
	}
	return got
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
		// The largest single contributor measured: one OSINT lookup reached a
		// prompt at 35,003 characters, sitting on this ceiling.
		Weight: 1,
	}

	// toolResultBudget bounds one string tool's result as it finishes. A typed
	// tool skips it — the typed branch sets isContextual — so this only ever
	// applied to half the tools, at half the evidence cap, for no stated reason.
	toolResultBudget = budgetSpec{
		Base: 4096, Share: 32, Ceiling: 16000,
		Bounds: "one string tool's result at dispatch",
		// One file_read of a shell script arrived at 15,925 characters, which is
		// this ceiling to within a rounding error.
		Weight: 1,
	}

	// payloadBudget bounds a node's fields carried as data: the tool messages a
	// stage is given, and the payload the trace shows.
	payloadBudget = budgetSpec{
		Base: 12000, Share: 12, Ceiling: 48000,
		Bounds: "one node's fields, as data",
		// Safe to scale hard: shortenPayloadValues cuts string VALUES and keeps
		// every key, nesting level and list length, so a scaled payload is a
		// smaller document rather than a broken one.
		Weight: 1,
	}

	// toolIndexBudget bounds the planner's tool index — signatures and return
	// shapes. It decides how many tools a planner can see at all, which is the
	// whole question once a registry outgrows a prompt.
	toolIndexBudget = budgetSpec{
		Base: 24000, Share: 8, Ceiling: 96000,
		Bounds: "the tool index shown to the planner",
		// Exempt, and the biggest thing in the prompt: measured at 28.6% of all
		// section bytes, up to 39,459 characters on every planner call. Cutting
		// it does not shorten text, it removes tools from the planner's view and
		// breaks the calls it then writes. The way to make this smaller is fewer
		// tools or terser signatures, not a percentage.
		Weight: 0,
	}
)

// ── What a stage may WRITE ──────────────────────────────────────────────────
//
// The same shape, on the return leg. These were left out of the table when it
// was built, on the boundary "caps on content going INTO a prompt" — which put
// half the problem outside it. Both decide how much of a run reaches a model.
//
// Measured across 600 runs, tokens out per stage against its cap:
//
//	reflector    p50 214   p90 1024   max 1024   cap 1024   ← the p90 IS the cap
//	reframe      p50 188   p90  242   max  351   cap  400
//	holmes       p50 252   p90  443   max  443   cap 1024
//	coder        p50 422   p90 1008   max 1008   cap 16384
//
// More than one reflection in ten was being cut off mid-reply. The cap had no
// comment and no commit that chose it — it arrived in a wholesale move and was
// never revisited when the reflector was given the user's answer to write.
//
// Share is of the window in TOKENS here, not characters: what a model may write
// and what it can read are both counted the same way on the wire.
// Every cap below is exempt — Weight 0 — and that is a measurement, not
// caution. The fault this scale exists to fix is REQUEST size: p90 202,593
// bytes in, against replies whose p90 is a few hundred tokens out. Scaling the
// return leg would buy almost nothing and cost the one thing a stage came to
// say. The table above already records that more than one reflection in ten was
// being cut off at its cap before it was raised; scaling would put it back.
var (
	// replyDecisionBudget bounds a stage that returns a decision and, on the
	// paths where no aggregator follows, the answer with it. The largest of
	// these, because it is the only one that can be the whole reply to a user.
	replyDecisionBudget = budgetSpec{
		Base: 2048, Share: 64, Ceiling: 8192,
		Bounds: "one reflection's decision, and its answer when nothing follows",
		// It can be the whole reply to a user. Cutting it cuts the answer.
		Weight: 0,
	}

	// replyBriefBudget bounds a stage that reports a judgement in a sentence or
	// two: an observer deciding whether a step is worth acting on, a validator
	// saying whether output means what it claims.
	replyBriefBudget = budgetSpec{
		Base: 1024, Share: 128, Ceiling: 4096,
		Bounds: "one stage's judgement, in a sentence or two",
		// Already a sentence or two. There is nothing here to reclaim.
		Weight: 0,
	}

	// replyEdgeBudget bounds an edge. It carries; it does not add, so it should
	// be the smallest of these — and a paragraph that ran into the cap is short
	// rather than wrong, which is why this stage does not report truncation.
	replyEdgeBudget = budgetSpec{
		Base: 600, Share: 256, Ceiling: 2000,
		Bounds: "one edge's framing paragraph",
		// The smallest cap in the engine at 600 tokens. Not the problem.
		Weight: 0,
	}

	// replyAnalysisBudget bounds a stage that reasons at length before deciding:
	// Holmes across its iterations, the debugger writing a fix plan.
	replyAnalysisBudget = budgetSpec{
		Base: 4096, Share: 32, Ceiling: 16384,
		Bounds: "one investigation's reasoning and its conclusion",
		// A conclusion cut short is a conclusion nobody can act on.
		Weight: 0,
	}

	// replyCodeBudget bounds a stage that writes a program. The largest, because
	// a truncated program is not a shorter program — it is a broken one, and the
	// coder used plain ask rather than the checked variant, so a fragment was
	// written to disk and three Holmes iterations went into discovering that it
	// had been cut rather than being wrong.
	replyCodeBudget = budgetSpec{
		Base: 16384, Share: 16, Ceiling: 65536,
		Bounds: "one generated program",
		// The comment above is the reason: a truncated program is broken, not
		// shorter. This one must never scale.
		Weight: 0,
	}

	// replyStructuredBudget bounds a stage filling a declared shape rather than
	// writing prose — preflight's classification, the curator's selection.
	replyStructuredBudget = budgetSpec{
		Base: 2048, Share: 128, Ceiling: 8192,
		Bounds: "one stage's reply into a declared schema",
		// A declared shape half-filled does not parse. Never scale a schema.
		Weight: 0,
	}
)

/*
 * replyBudget resolves how much a stage may write, against the model's window.
 * desc: budget's sibling for the return leg. It does NOT multiply by
 *       charsPerToken: a reply cap is counted in tokens, which is what the
 *       window is counted in, so the conversion budget applies would give a cap
 *       four times too large.
 * param: s - which cap.
 * return: the cap, in tokens.
 */
func (a *Agent) replyBudget(s budgetSpec) int {
	if a == nil {
		return s.Base
	}
	window := a.smallestKnownWindow()
	if window <= 0 {
		return s.Base
	}
	return s.resolve(window/s.Share, a.promptScale())
}

/*
 * promptScale is how far this deployment narrows the caps that carry content.
 * desc: 1 means the numbers in the table above, which is what every deployment
 *       had before the setting existed — so an application that sets nothing is
 *       unchanged. Anything outside (0,1] is treated as 1 rather than refused:
 *       a mistyped setting should not make an engine send nothing.
 * return: the scale, in (0,1].
 */
func (a *Agent) promptScale() float64 {
	if a == nil {
		return 1
	}
	s := a.cfg.PromptScale
	if s <= 0 || s > 1 {
		return 1
	}
	return s
}

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
	return s.resolve(window*charsPerToken/s.Share, a.promptScale())
}
