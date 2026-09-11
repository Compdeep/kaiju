package agent

import (
	"context"
	"log"
	"slices"
	"time"

	"github.com/Compdeep/kaiju/agent/llm"
)

// What one model call may spend, resolved in one place.
//
// There were two budget systems and they had never been introduced. replyBudget
// read the table in budgets.go and sized a stage against the smallest window
// among the configured lanes; chat_node.go, aggregator.go, loop_react.go and the
// planner each read cfg.MaxTokens and did their own arithmetic on it. A stage
// belonged to one or the other by accident of when it was written, and the
// operator's own number reached four call sites out of twenty.
//
// Three faults came out of that, all of them measured on a live deployment:
//
//	The window was the wrong model's. smallestKnownWindow reads LLMModel and
//	ExecutorModel and nothing else, so the answer lane's reply was sized against
//	the executor: min(1310720, 262144) = 262144, and replyDecisionBudget resolved
//	to 4,096 rather than its 8,192 ceiling. That is the cap a chat turn spent
//	entirely on reasoning before returning an empty string.
//
//	The thinking budget was never asked for. It was resolved from the lane's
//	model, which is empty whenever a lane falls back to its configured client —
//	every call on a deployment that does not pick models per request. So the
//	division between thinking and answering, which exists precisely to stop the
//	above, has never reached the wire.
//
//	The operator's ceiling was not a ceiling. cfg.MaxTokens was the literal
//	number at four sites, a doubled base at the planner, and invisible to every
//	stage that read the table.
//
// So: one function, one precedence, and the numbers it produces are the numbers
// that go on the wire.
//
//	catalog(model).context_tokens / stage.Share
//	  -> stage.Ceiling, floored at stage.Base      the table's own shape
//	  -> the stage's own stated minimum, if any
//	  -> catalog max_output_tokens                 the model cannot exceed this
//	  -> cfg.MaxTokens                             the operator's ceiling
//	  -> what the caller itself asked for          never raised above its ask
//	  -> llm.capReply against the live prompt      at send time, in the client
//
// Nothing here reaches for smallestKnownWindow. That function is still right for
// the PROMPT side (a.budget, contextgate.go): one prompt is built and may be
// sent to either lane, so the smaller window governs. A reply is written by one
// model, and the door knows which.

// bounds is the answer: what this call may spend, in tokens and in time.
type bounds struct {
	// Reply is the whole completion, in tokens — max_tokens on the wire.
	Reply int

	// Thinking is how much of Reply may go on hidden reasoning. Zero means say
	// nothing about it, which is not the same as asking for none: the catalog
	// decides whether the model acts on a budget at all, and one that does not
	// is better left alone than sent a control it will ignore.
	Thinking int

	// Effort is the reasoning effort to ask for, empty to ask for none. Narrowed
	// to what the catalog says this model has been MEASURED to act on — every
	// provider accepts the parameter and not every model does anything with it,
	// so an unnarrowed effort is a setting that appears to work.
	Effort string

	// Deadline is how long the call may take. Separate from Reply because
	// max_tokens does not bound time: a model that reasons before it answers
	// spends an unbounded amount of it, and a rig that ignores the token cap
	// spends more.
	Deadline time.Duration
}

// budgetAsk is the call being bounded, as the caller can describe it before the
// numbers exist.
//
// A struct rather than six positional parameters, so a later question — a lane
// that needs its own floor, a stage that knows its prompt size — is one field
// rather than a signature change at every call site. See docs/design-notes.md,
// pattern 8.
type budgetAsk struct {
	// Lane is which model answers. Carried for the deadline, which is per model,
	// and so that a future rule that differs by lane has somewhere to read it.
	Lane Lane

	// Stage is which cap from the table in budgets.go. The zero value is
	// answered by defaultStage.
	Stage budgetSpec

	// Model is the id the call will actually reach — the lane's selection where
	// there is one, the client's own default where there is not. Resolved by the
	// caller with resolvedModel, because the empty string here is what made the
	// reasoning budget dead code.
	Model string

	// MinReply is what this stage's own work requires, whatever the table says.
	// Zero for a stage with nothing to state.
	//
	// One stage has it: the planner is told it may write up to MaxNodes steps,
	// so a cap below that count invites a plan that cannot be written. Every
	// other stage picks a size for how much prose it wants, which is what the
	// table is for.
	MinReply int

	// AskedFor is the caller's own MaxTokens, zero when it did not say. Treated
	// as a ceiling and never raised — a caller that wants a short reply keeps
	// getting one, which is how llm.capReply has always behaved and why the
	// router's deliberately tiny budget survives this.
	AskedFor int
}

// replyFloor is the least a catalog-derived cap is allowed to settle on.
//
// A prompt that nearly fills the window, or a share of a small window, otherwise
// computes a cap of a few dozen tokens, and a reply cut off at that length looks
// like the model refusing to answer rather than like a budget.
//
// Applied BEFORE the caller's own ask, so it raises a number this package
// derived and never one a caller chose. The router asks for 128 on purpose.
const replyFloor = 256

/*
 * defaultStage is the cap a call gets when it names no stage.
 * desc: replyDecisionBudget rather than the smallest, because the two failure
 *       modes are not worth the same. A cap set too low cuts a reply in half and
 *       the stage that parses it reports malformed input for an answer that was
 *       fine; a cap set too high costs tokens the stage was never going to
 *       spend. Naming the stage is still the right thing to do — this is what
 *       happens when somebody forgets, chosen so that forgetting is expensive
 *       rather than wrong.
 * return: the spec to use.
 */
func defaultStage() budgetSpec { return replyDecisionBudget }

/*
 * boundsFor resolves what one call may spend.
 * desc: The whole precedence, in one place, in the order the file header states.
 *       Everything that decides how big a reply may be, how much of it may be
 *       thinking, and how long the call may take, is here — so a change to one
 *       is visibly a change relative to the others, and a stage added later
 *       cannot quietly opt out of any of them.
 * param: ctx - the run context, carrying this run's own effort and budget.
 * param: k - the call being bounded.
 * return: the numbers to send.
 */
func (a *Agent) boundsFor(ctx context.Context, k budgetAsk) bounds {
	reply := a.replyBound(k)
	thinking, effort := a.thinkingBound(ctx, k.Model, reply)
	return bounds{
		Reply:    reply,
		Thinking: thinking,
		Effort:   effort,
		Deadline: a.deadlineFor(ctx, k.Model),
	}
}

/*
 * replyBound resolves how many tokens the whole completion may be.
 * desc: The chain in the file header. Each step only ever LOWERS, except the
 *       stage's floor and MinReply, which raise a number the catalog produced —
 *       never one a caller chose.
 * param: k - the call being bounded.
 * return: the cap, in tokens.
 */
func (a *Agent) replyBound(k budgetAsk) int {
	s := k.Stage
	if s.Share <= 0 {
		s = defaultStage()
	}
	window, maxOutput := a.limitsOf(k.Model)

	// The catalog's answer for the model that will actually write this.
	//
	// Where the catalog does not carry it there is nothing to derive from, so
	// the caller's own number stands — an application supplying no Limits, or
	// supplying them for one model and not the rest, is unchanged by all of
	// this, which is the contract Config.Limits states. Only the spec's floor
	// answers when nobody said anything at all.
	got := s.Base
	switch {
	case window > 0:
		got = s.resolve(window/s.Share, a.promptScale())
	case k.AskedFor > 0:
		got = k.AskedFor
	}

	// What this stage's work requires. Stated by the stage rather than inferred,
	// because only the stage knows: the planner has a step count to write for.
	if k.MinReply > got {
		got = k.MinReply
	}
	if got < replyFloor {
		got = replyFloor
	}

	// The model's published ceiling. Some providers reject a max_tokens above
	// their own maximum rather than trimming it, so this is correctness and not
	// only tidiness.
	if maxOutput > 0 && maxOutput < got {
		got = maxOutput
	}

	// The operator's ceiling. One number, meaning one thing, reaching every
	// stage — which is what it did not do when four call sites read it as a
	// literal and the rest of the table had never heard of it.
	if a.cfg.MaxTokens > 0 && a.cfg.MaxTokens < got {
		// Said out loud, because the operator cannot infer it. A ceiling below
		// what a stage stated it needs is a setting quietly deciding that the
		// plan may not hold the steps the same config allows it to create.
		if k.MinReply > a.cfg.MaxTokens {
			log.Printf("[bounds] %s needs %d tokens and max_tokens is %d — the reply is cut to the setting",
				s.Bounds, k.MinReply, a.cfg.MaxTokens)
		}
		got = a.cfg.MaxTokens
	}

	// What the caller itself asked for, where it asked. Never raised: a stage
	// that wants a short reply keeps getting one, and the router's 128 tokens
	// are a decision about the shape of the reply rather than its size.
	if k.AskedFor > 0 && k.AskedFor < got {
		got = k.AskedFor
	}
	return got
}

/*
 * thinkingBound resolves how much of the reply may be hidden reasoning, and how
 * hard to think.
 * desc: Two questions with one answer each, resolved together because they are
 *       narrowed by the same catalog entry and sent in the same field.
 *
 *       Both are asked only where the catalog says the model acts on them. Every
 *       provider accepts both parameters and none errors on either, so sending
 *       one blindly gives a control that appears to work: qwen3.6-35b-a3b
 *       reasoned 1,498 tokens by default and 1,548 at "low".
 *
 *       Never turns thinking ON. A budget is a bound on thinking that is already
 *       happening; whether it happens at all is decided at the door, per lane.
 * param: ctx - the run context, which may carry this run's own choice.
 * param: model - the model this call will reach.
 * param: reply - the reply cap already resolved, which the thinking comes out of.
 * return: the thinking budget in tokens, and the effort, either possibly zero.
 */
func (a *Agent) thinkingBound(ctx context.Context, model string, reply int) (int, string) {
	if a == nil || a.cfg.Reasoning == nil || model == "" {
		return 0, ""
	}
	effort, budget := a.reasoningFor(ctx)
	allowed, takesBudget := a.cfg.Reasoning(model)

	if effort != "" && !slices.Contains(allowed, effort) {
		effort = ""
	}
	if !takesBudget {
		// The model ignores a budget, so nothing here can bound its thinking.
		// What saves the reply on this model is the deadline and the second
		// attempt, not a number it will not read.
		return 0, effort
	}
	if budget <= 0 {
		// Nobody set one, so it comes out of this call's own allowance.
		budget, _ = splitBudget(reply)
	}
	// A budget at or above the reply cap is the fault the catalog entries
	// carried: the thinking is allowed to consume everything the answer was
	// going to come out of, and the reader gets nothing rather than a shorter
	// reply. Narrowed to the share the division would have given, which is the
	// largest number that still leaves the answer room it cannot be robbed of.
	if share, _ := splitBudget(reply); share > 0 && budget > share {
		log.Printf("[bounds] thinking budget %d does not fit a reply cap of %d — narrowed to %d",
			budget, reply, share)
		budget = share
	}
	return budget, effort
}

/*
 * deadlineFor resolves how long this call may take.
 * desc: roundBudget's rule, reading the effort off the context rather than
 *       taking a Trigger.
 *
 *       That parameter is why the guards were scattered. roundBudget wanted the
 *       run's effort, the effort lived on the Trigger, the Trigger lived on the
 *       Graph — so heavyRound carried a *Graph for no other purpose, and any
 *       lane that could not produce one wrote its own recovery instead of using
 *       the shared one. The effort was on the context the whole time:
 *       laneSelectionFromTrigger copies it into the lane selection, and
 *       reasoningFor reads it back with exactly this precedence.
 *
 *       Floored, never ceilinged. What stops a long run is the operator's wall
 *       clock, which is a number somebody chose rather than the product of five
 *       others.
 * param: ctx - the run context, carrying this run's effort where it chose one.
 * param: model - the model that will answer, whose measured pace lengthens this.
 * return: the deadline, never below minRoundBudget except at the fast effort.
 */
func (a *Agent) deadlineFor(ctx context.Context, model string) time.Duration {
	effort, _ := a.reasoningFor(ctx)
	d, ok := effortBudget[effort]
	if !ok {
		d = effortBudget[EffortDefault]
	}
	// The floor protects a deadline nobody chose. Fast is chosen, and choosing
	// it is asking for the shorter one.
	if d < minRoundBudget && effort != EffortFast {
		d = minRoundBudget
	}
	// Applied after the floor, so a slow model's allowance is multiplied against
	// the deadline it would actually have been given.
	return llm.ScaleByPace(d, a.cfg.Pace, model)
}

/*
 * limitsOf is what the catalog says this model can hold and can write.
 * desc: Guarded here rather than at each caller: the catalog is an application's
 *       to supply and most do not, and a nil lookup dereferenced once already.
 * param: model - the model id, possibly empty.
 * return: the context window and the maximum reply, both zero when unknown.
 */
func (a *Agent) limitsOf(model string) (window, maxOutput int) {
	if a == nil || a.cfg.Limits == nil || model == "" {
		return 0, 0
	}
	return a.cfg.Limits(model)
}
