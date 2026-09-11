package agent

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/Compdeep/kaiju/agent/llm"
)

// One call to a model, start to finish.
//
// ask.go already said that everything applying to every model call belongs at
// one seam, and it was right about lane resolution, the reply cap and the
// reasoning parameters. The two steps that decide whether a reader gets an
// answer at all were not there. They were written at the call sites:
//
//	executive.go:1811,1835,1909   the planner, written out
//	chat.go:205,217,253           the chat lane, written out again
//	heavy_round.go:55,64,82       written a third time, generically, and used
//	                              only by compute.go
//
// and absent from every other lane. chat_node.go — which is the lane that
// answers a conversational turn on any deployment not running in chat mode, and
// therefore the lane a person is most often sitting in front of — had none of
// them. On 2026-09-11 a turn there spent its whole 4,096-token budget reasoning,
// wrote nothing, and the reader was told the request had produced no answer.
// aggregator.go, rca.go, microplanner.go and loop_react.go have the same gap.
//
// The guard that was supposed to police this read three filenames out of the
// source and checked them for the right function names. It passed.
//
// So the deadline, the capture and the second attempt move in here with
// everything else, and a lane gets them by being a lane. The verbs above this —
// ask, askParsed, askStream, askStreamResp and the three completeX shims — are
// now one line each, so no call site changed to gain any of it.
//
// What is NOT here, deliberately:
//
//	Whether a conversational turn should think. That is a property of the
//	message, decided by the router one call earlier and set on the request by
//	the two lanes that answer a person. The door respects what it is told and
//	applies the lane's own rule only where nothing said.
//
//	Asking for a SHORTER reply. A plan cut off with steps in it is a different
//	fault from a plan that never started, and the remedies are opposite. The
//	planner keeps salvageTruncatedPlan and its shorter-plan retry.

/*
 * modelCall is one call, as the stage making it can describe it.
 *
 * A struct rather than a growing parameter list, so a question asked later — a
 * stage that needs its own floor, a lane that wants a different retry — is one
 * field rather than a signature change at forty call sites. See
 * docs/design-notes.md, pattern 8.
 */
type modelCall struct {
	// Lane names which model answers. See Lane.
	Lane Lane

	// Req is the request. Its Model, MaxTokens and Reasoning are set in place.
	Req *llm.ChatRequest

	// Stage is which cap from the table in budgets.go bounds the reply. The
	// zero value is answered by defaultStage, which is chosen so that forgetting
	// is expensive rather than wrong.
	Stage budgetSpec

	// MinReply is what this stage's own work requires whatever the table says.
	// Zero for a stage with nothing to state; the planner is the one that has.
	MinReply int

	// OnChunk makes this a streamed call and receives each chunk as it arrives.
	//
	// Nil sends the call as one document, which is what most stages want. A
	// stage that wants it streamed but has no reader passes captureOnly:
	// streaming is the only way to keep the reasoning of a call that a deadline
	// cancels, because cancelling returns an error and no reply to read it off.
	OnChunk func(chunk, kind string)

	// Parsed says the caller parses what comes back, so a reply that stopped at
	// the token cap is an error rather than a short answer. A stage writing
	// prose for a person leaves this false: failing the run over a reply that
	// ran long throws away an answer that was fine.
	Parsed bool
}

/*
 * captureOnly makes a call streamed with nothing listening.
 * desc: The chunks are not discarded — send captures the reasoning from every
 *       streamed call — they simply have no second destination. The planner and
 *       the compute lane use it, because a deadline cancels the call and returns
 *       no reply, so what it had already thought exists only as chunks that went
 *       past.
 */
func captureOnly(string, string) {}

// The cap a second attempt is given when the model cannot be asked to stop
// thinking.
//
// Measured across 52 models on the real planner prompt: the most any of them
// spent thinking was 7,424 tokens (gpt-5-nano), and the largest visible answer
// was 1,704. 9,216 clears both with room, which is the least that can be called
// a fair second chance rather than the same call again.
const lockedRetryCap = 9216

// ErrNoReply is a call that produced nothing and could not be made to produce
// anything: the budget went on reasoning, the second attempt was made or could
// not be, and there is no answer to give.
//
// It WRAPS llm.ErrReplyTruncated, because that is what it is — a reply that
// stopped at the cap with nothing written. The stages that already tell "ran out
// of budget" from "the provider failed" keep doing so without learning a second
// name, and the remedy the wrapped message names is still the right one.
var ErrNoReply = fmt.Errorf("the model produced no reply within its budget: %w", llm.ErrReplyTruncated)

/*
 * send makes one call to a model and answers every way it can come back empty.
 * desc: The whole of it, in order: resolve the lane, bound the call, decide the
 *       thinking, send it under a deadline while keeping what it thinks, and ask
 *       again once if nothing came back.
 *
 *       A step added here is added for every stage at once. That is the point of
 *       the seam, and it is what the three hand-written copies of the last step
 *       could not do.
 * param: ctx - the run's context, carrying the lane selection and the trace.
 * param: mc - the call.
 * return: the reply, and any error left after the second attempt.
 */
func (a *Agent) send(ctx context.Context, mc modelCall) (*llm.ChatResponse, error) {
	c, laneModel := a.lane(ctx, mc.Lane)
	if laneModel != "" {
		mc.Req.Model = laneModel
	}

	// The model that will ACTUALLY answer, which is not always the one the lane
	// named: a lane falling back to its configured client returns an empty
	// string, and the client fills its own in later. Everything downstream
	// resolves that — capReply, applyParameters — and the step that sized the
	// thinking did not, which made the whole reasoning budget dead code on any
	// deployment that does not pick models per request.
	model := resolvedModel(laneModel, c)

	a.decideThinking(mc.Lane, mc.Req)
	b := a.boundsFor(ctx, budgetAsk{
		Lane:     mc.Lane,
		Stage:    mc.Stage,
		Model:    model,
		MinReply: mc.MinReply,
		AskedFor: mc.Req.MaxTokens,
	})
	applyBounds(mc.Req, b)

	// The client lowers it once more, against the live prompt and its own copy of
	// the catalog — a prompt that nearly fills the window leaves less room than
	// any table can know. It only ever lowers, so this cannot undo a bound above.
	//
	// The budget is stated to the model AFTER that, because the number in the
	// prompt has to be the number the provider stops at. Stating the resolved cap
	// and sending a smaller one tells the model it has room it does not have,
	// which is the fault stateBudget exists to prevent.
	if cap := c.ReplyCap(mc.Req); cap >= budgetFloor {
		mc.Req.MaxTokens = cap
		stateBudget(mc.Req, cap)
	}

	// Images ride the context so they re-attach on every heavy call this turn,
	// staying visible across follow-ups.
	if mc.Lane == Heavy {
		if imgs := visionImagesFrom(ctx); len(imgs) > 0 && IsVisionModel(mc.Req.Model) {
			llm.AttachImages(mc.Req.Messages, imgs)
		}
	}

	// A deadline, because max_tokens does not bound time. A thinking model
	// writes its hidden tokens into the same budget as its reply and the wait is
	// whatever that takes; a rig that ignores the budget is unbounded.
	callCtx, cancel := context.WithTimeout(ctx, b.Deadline)
	defer cancel()

	var thought thinkingCapture
	started := time.Now()
	resp, err := a.transmit(callCtx, c, mc, &thought)
	a.writeTrace(ctx, mc.Req, resp, err, started)

	// Our own deadline expiring is not the caller giving up. A run that was
	// abandoned must still be abandoned; a call we cut short is ours to re-ask.
	if err != nil && callCtx.Err() != nil && ctx.Err() == nil {
		log.Printf("[call] %s passed its %s deadline on %s — asking again, carrying %d chars of reasoning",
			stageName(ctx), b.Deadline, model, len(thinkingOf(resp, &thought)))
		return a.secondAttempt(ctx, mc, c, model, b, cutThought(thinkingOf(resp, &thought)), "recover_deadline")
	}
	if err != nil {
		return resp, err
	}
	if len(resp.Choices) == 0 {
		// What an empty reply means belongs to the caller: a forced tool call
		// reads it as a refusal, a prose stage as nothing to show.
		return resp, nil
	}

	// Nothing was cut off, because nothing was written. The budget went on
	// reasoning and the reply never started, so asking again under the same
	// bounds would spend it the same way.
	if nothingVisible(resp.Choices[0]) {
		log.Printf("[call] %s returned nothing — the reply budget of %d went on reasoning on %s",
			stageName(ctx), b.Reply, model)
		// The reply where it carries the reasoning, the capture where it does
		// not — the order thinkingOf explains. A streamed call may deliver its
		// thinking as chunks and assemble a message that does not repeat it, and
		// handing the retry the reply alone loses everything the first attempt
		// paid for.
		return a.secondAttempt(ctx, mc, c, model, b, cutThought(thinkingOf(resp, &thought)), "recover_thought")
	}

	// The reply ran into its cap WITH something in it, which is a different
	// thing and belongs to the caller — a half-written program written to disk
	// and run costs three debugging rounds on a script that was never whole.
	if mc.Parsed && llm.Truncated(resp) {
		return resp, llm.TruncationError(mc.Req.MaxTokens)
	}
	return resp, nil
}

/*
 * secondAttempt re-asks a call that produced nothing.
 * desc: Two remedies, and which one applies is a property of the model rather
 *       than of the stage.
 *
 *       A model that CAN be asked to stop is asked to stop. It cannot reason, so
 *       it must produce output, and it starts from the thinking already paid for
 *       rather than from nothing.
 *
 *       A model that cannot — the catalog's reasoning_optional, false for
 *       glm-5.3 among others — is given a larger cap instead. Telling it not to
 *       think is the same call again: it spends the budget the same way and
 *       returns nothing twice, which is what happened before anything read that
 *       field.
 *
 *       When there is no room left to give, that is reported rather than
 *       retried. The remedy is then an operator's and nothing here can apply it.
 * param: ctx - the run context. The retry runs under the run's remaining time,
 *        not the deadline that just expired.
 * param: mc - the original call. Its request is copied, never modified.
 * param: c - the client, already resolved; the retry must reach the same one.
 * param: model - the model that answered, for the catalog questions.
 * param: b - the bounds the first attempt ran under.
 * param: cut - what the first attempt managed to think, possibly nil.
 * param: reason - the trace tag: which of the two endings this is.
 * return: the second reply, or the first fault named.
 */
func (a *Agent) secondAttempt(ctx context.Context, mc modelCall, c *llm.Client,
	model string, b bounds, cut *llm.ChatResponse, reason string) (*llm.ChatResponse, error) {

	raised := 0
	if a.reasoningLocked(model) {
		if raised = a.raisedCap(model, b.Reply); raised == 0 {
			return nil, fmt.Errorf("%w — %s spent %d tokens reasoning, cannot be asked to stop, "+
				"and max_tokens (%d) leaves nothing more to give",
				ErrNoReply, model, b.Reply, a.cfg.MaxTokens)
		}
		log.Printf("[call] %s cannot be asked to stop reasoning — asking again at %d tokens instead of %d",
			model, raised, b.Reply)
	}
	retry := retryRequest(mc.Req, cut, raised)

	rc := mc
	rc.Req = retry
	// Sent as one document, as a retry always was. Streaming it would ask the
	// provider for a stream on a request built from one that was already
	// streamed once, which is the shape client.go warns about — and the reader
	// is served below instead.
	rc.OnChunk = nil
	// The retry is never itself recovered: a third attempt under bounds that
	// have not changed is the second attempt again. transmit rather than send.
	rctx := retracing(ctx, stageTag(ctx, reason))
	var thought thinkingCapture
	started := time.Now()
	resp, err := a.transmit(rctx, c, rc, &thought)
	a.writeTrace(rctx, retry, resp, err, started)
	if err != nil {
		return resp, err
	}
	if len(resp.Choices) == 0 || nothingVisible(resp.Choices[0]) {
		return resp, fmt.Errorf("%w — %s produced nothing twice, at %d tokens",
			ErrNoReply, model, retry.MaxTokens)
	}
	if mc.Parsed && llm.Truncated(resp) {
		return resp, llm.TruncationError(retry.MaxTokens)
	}

	// The first attempt streamed nothing a reader could use, so the recovered
	// answer has to reach them the way the first would have. Only for a lane
	// that streams: everywhere else the caller reads the reply it is handed.
	if mc.OnChunk != nil {
		if text := streamedText(resp); text != "" {
			mc.OnChunk(text, "content")
		}
	}
	return resp, nil
}

/*
 * retryRequest is the second attempt, as a request.
 * desc: Separated from the send so the SHAPE of a retry can be checked without a
 *       provider — what it asks for is the thing worth checking, not that a stub
 *       answered.
 * param: req - the original request. Copied, never modified: the caller may
 *        still hold it, and a retry that mutates what it retries cannot be run
 *        twice.
 * param: cut - the reply that produced nothing, for whatever it managed to
 *        think. Nil when a cancelled call left none.
 * param: raised - a larger reply cap for a model that cannot be asked to stop
 *        thinking, or zero to ask it to stop.
 * return: the retry.
 */
func retryRequest(req *llm.ChatRequest, cut *llm.ChatResponse, raised int) *llm.ChatRequest {
	if req == nil {
		return nil
	}
	retry := *req
	retry.Messages = append(append([]llm.Message{}, req.Messages...), llm.Message{
		Role:    "user",
		Content: recoveryPrompt(reasoningOf(cut)),
	})
	if raised <= 0 {
		// The ordinary remedy. Thinking off means the budget belongs to the
		// reply, and a model that cannot think has nothing to spend it on but
		// the answer.
		llm.WithoutReasoning(&retry)
		return &retry
	}
	// The model cannot be asked to stop, so it is given room to finish instead.
	retry.MaxTokens = raised
	// Divided against the LARGER cap, or the thinking keeps the share it was
	// given and the answer gains nothing from the raise.
	if retry.Reasoning != nil && retry.Reasoning.MaxTokens > 0 {
		if share, _ := splitBudget(raised); share > 0 {
			retry.Reasoning.MaxTokens = share
		}
	}
	return &retry
}

/*
 * transmit puts one prepared request on the wire and keeps what it thinks.
 * desc: The only place this package calls a client. Streamed when the call has
 *       an OnChunk, sent as one document otherwise — and the reasoning is
 *       captured either way, because a streamed call that a deadline cancels
 *       returns no reply to read it off.
 * param: ctx - the call's context, carrying its deadline.
 * param: c - the client.
 * param: mc - the call.
 * param: thought - filled with the reasoning as it arrives.
 * return: the reply and any transport error.
 */
func (a *Agent) transmit(ctx context.Context, c *llm.Client, mc modelCall,
	thought *thinkingCapture) (*llm.ChatResponse, error) {

	if mc.OnChunk == nil {
		return c.Complete(ctx, mc.Req)
	}
	return c.CompleteStreamResp(ctx, mc.Req, func(chunk, kind string) {
		thought.onChunk(chunk, kind)
		mc.OnChunk(chunk, kind)
	})
}

/*
 * decideThinking applies the lane's own rule about reasoning.
 * desc: Unchanged from prepare, including the order, which is load-bearing. A
 *       caller states what only it can know and the lane overrules it where the
 *       lane's rule is not a preference.
 * param: l - the lane.
 * param: req - the request, modified in place.
 */
func (a *Agent) decideThinking(l Lane, req *llm.ChatRequest) {
	// The two lanes that force a SMALL call get the model's thinking turned off,
	// whatever model that is.
	//
	// Not a default and not a setting: there is no deployment in which hidden
	// reasoning helps a 96-token routing decision or a preflight classification,
	// so there is nothing for an operator to decide. Measured on the real
	// preflight schema — with thinking on, three current models ran to the cap
	// and returned unparseable JSON; with it off, all three answered in 157 to
	// 292 tokens.
	//
	// Heavy and Answer are deliberately absent. Heavy forces a call too, but it
	// has the budget for the thinking and the thinking earns it: on one planner
	// prompt a thinking model returned complete plans three times of three where
	// a non-thinking one ran the cap out three times of three. What that costs is
	// time, and the deadline is widened for it rather than the thinking removed.
	// Answer writes prose, where thinking is simply better.
	if l == Light || l == Route {
		llm.WithoutReasoning(req)
		return
	}

	// Heavy is the one lane that asks. Reasoning helps the planning and costs
	// time, and which of those a deployment wants is not something the engine
	// can know — so the operator says, and an unset setting keeps the model's
	// own default, which is what every config file did before this existed.
	//
	// After the caller, on purpose: the planner asks to think on every call, and
	// an operator who switched reasoning off still wins.
	if l == Heavy {
		switch a.llmReasoning {
		case "on":
			llm.WithReasoning(req)
		case "off":
			llm.WithoutReasoning(req)
		}
	}

	// Answer keeps whatever it was told. Whether a conversational turn needs
	// reasoning is a property of the message, decided by the router one call
	// earlier; an empty decision leaves the model's own default alone, which is
	// what a router that tripped should not be able to change.
}

/*
 * applyBounds writes the resolved numbers onto the request.
 * desc: Separated from resolving them so the arithmetic can be read and tested
 *       without a client, and so this — the part that decides what the provider
 *       actually receives — is six lines rather than buried in forty.
 *
 *       It does NOT state the budget in the prompt. That number has to be the
 *       one the provider stops at, and the client lowers this again against the
 *       live prompt, so it is said after that — in send.
 *
 *       Never turns thinking ON. A request that said nothing about it keeps
 *       saying nothing; one that turned it off is left alone.
 * param: req - the request, modified in place.
 * param: b - what this call may spend.
 */
func applyBounds(req *llm.ChatRequest, b bounds) {
	if req == nil {
		return
	}
	req.MaxTokens = b.Reply

	if (b.Effort != "" || b.Thinking > 0) && (req.Reasoning == nil || req.Reasoning.On()) {
		if req.Reasoning == nil {
			req.Reasoning = &llm.ReasoningControl{}
		}
		if b.Effort != "" {
			req.Reasoning.Effort = b.Effort
		}
		if b.Thinking > 0 {
			req.Reasoning.MaxTokens = b.Thinking
		}
	}

}

/*
 * reasoningLocked reports whether this model reasons no matter what it is asked.
 * desc: Guarded here rather than at the one caller, for the reason every other
 *       catalog lookup is: the capability is an application's to supply and most
 *       do not.
 * param: model - the model id, possibly empty.
 * return: true only when the catalog says so.
 */
func (a *Agent) reasoningLocked(model string) bool {
	if a == nil || a.cfg.ReasoningLocked == nil || model == "" {
		return false
	}
	return a.cfg.ReasoningLocked(model)
}

/*
 * raisedCap is the larger reply budget a locked model's second attempt gets.
 * desc: Large enough to clear the most thinking anybody has been measured doing
 *       plus the largest answer, and never past what the model publishes or what
 *       the operator allows.
 *
 *       The operator's ceiling is not overruled. A cost control that a recovery
 *       may quietly exceed is not a control, so when there is no headroom this
 *       returns zero and the caller reports it — which tells the operator the
 *       one thing they can act on.
 * param: model - the model that will answer.
 * param: current - the cap the first attempt ran under.
 * return: the raised cap, or zero when there is no room to raise it.
 */
func (a *Agent) raisedCap(model string, current int) int {
	want := current * 2
	if want < lockedRetryCap {
		want = lockedRetryCap
	}
	if _, maxOutput := a.limitsOf(model); maxOutput > 0 && want > maxOutput {
		want = maxOutput
	}
	if a.cfg.MaxTokens > 0 && want > a.cfg.MaxTokens {
		want = a.cfg.MaxTokens
	}
	if want <= current {
		return 0
	}
	return want
}

// stageTag names a retry after the stage that made it, so a trace can tell the
// planner's recovery from a coder's. Falls back to the bare reason outside any
// stage, where nothing is traced anyway.
func stageTag(ctx context.Context, reason string) string {
	if id, named := traceIDFrom(ctx); named && id.Tag != "" {
		return id.Tag + "_" + reason
	}
	return reason
}

// stageName is what to call this stage in a log line.
func stageName(ctx context.Context) string {
	id, named := traceIDFrom(ctx)
	switch {
	case !named:
		return "a model call"
	case id.NodeType != "":
		return id.NodeType
	default:
		return id.Tag
	}
}
