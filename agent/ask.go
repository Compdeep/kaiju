package agent

import (
	"context"

	"github.com/Compdeep/kaiju/agent/llm"
)

// One door in front of the model.
//
// A call to a model is never just a call. Which model answers depends on the
// lane; how big a reply may be depends on that model's published limits and on
// how much of its window the prompt already took; whether a truncated reply is
// a failure depends on whether the caller parses what comes back.
//
// Each of those was applied at the call site, so each was applied differently
// or not at all: nine calls sized their reply against the model and nine did
// not, including the one that asks for the largest reply this engine ever makes.
// Anything added later had to be added everywhere, and was not.
//
// So there is one function that sends, and it takes the lane as an argument.

// Lane names which model answers a call.
//
// The split is cost. A run makes one or two Heavy calls and a dozen Light ones,
// and sending the cheap work to the reasoning model multiplies the bill for no
// gain — while sending the planner to the cheap model produces plans that do
// not parse.
type Lane int

const (
	// Heavy is the reasoning model: the planner, Holmes, the microplanner, the
	// compute architect. Slow, expensive, and the only one trusted with a
	// forced tool call.
	Heavy Lane = iota

	// Light is the executor model: preflight, the reflector, the observer, the
	// context curator, the plan validator. Cheap and frequent.
	Light

	// Route is the one decision made before anything else — is this a
	// conversation or a piece of work. A small pinned model when configured,
	// otherwise Light, because the rest of the cheap background calls should
	// not be affected by pinning one.
	Route

	// Answer writes the final answer. Kept apart from Heavy so an operator's
	// answer model — which may be a thinking model — never drives the planner's
	// forced tool calls.
	Answer
)

func (l Lane) String() string {
	switch l {
	case Heavy:
		return "heavy"
	case Light:
		return "light"
	case Route:
		return "route"
	case Answer:
		return "answer"
	}
	return "unknown"
}

/*
 * lane resolves which client and model a lane reaches for this request.
 * desc: The per-request selection on the context wins where it names both a
 *       provider and a model and that provider is configured; otherwise the
 *       lane's own default applies. Route falls back to Light and Answer to
 *       Heavy, which is why neither is a plain lookup.
 * param: ctx - carries the per-request lane selection, if any.
 * param: l - the lane.
 * return: the client, and the model id to stamp on the request — empty when the
 *         client's own default is the right one.
 */
func (a *Agent) lane(ctx context.Context, l Lane) (*llm.Client, string) {
	switch l {
	case Light:
		return a.lightLane(ctx)
	case Route:
		return a.routeLane(ctx)
	case Answer:
		return a.answerLane(ctx)
	default:
		return a.heavyLane(ctx)
	}
}

/*
 * ask sends one completion through a lane.
 * desc: Four steps, in this order and only in this order: resolve the lane,
 *       stamp its model on the request, size the reply against that model, and
 *       send. The sizing has to come last because it measures the prompt, and
 *       the model has to be stamped before the sizing because the limits are
 *       looked up by model id.
 *
 *       Everything that applies to every model call belongs here. That is the
 *       point of it: a step added here is added for every stage at once, which
 *       is what the three near-identical doors this replaces could not do.
 * param: ctx - the run's context, carrying the lane selection.
 * param: l - which model answers.
 * param: req - the request. Its Model and MaxTokens are set in place.
 * return: the provider's response.
 */
func (a *Agent) ask(ctx context.Context, l Lane, req *llm.ChatRequest) (*llm.ChatResponse, error) {
	c, model := a.lane(ctx, l)
	if model != "" {
		req.Model = model
	}
	a.capReply(resolvedModel(req.Model, c), req)

	// Images ride the context so they re-attach on every heavy call this turn,
	// staying visible across follow-ups. Heavy only, as before: the model check
	// would make this safe on any lane, but widening it is a behaviour change
	// and belongs with the stage that moves the remaining callers here.
	if l == Heavy {
		if imgs := visionImagesFrom(ctx); len(imgs) > 0 && IsVisionModel(req.Model) {
			llm.AttachImages(req.Messages, imgs)
		}
	}

	return c.Complete(ctx, req)
}

/*
 * askParsed is ask for a caller that parses what comes back.
 * desc: A reply that stopped at the token cap is missing its end — the last
 *       JSON object has no closing brace — and a caller that parses it reports
 *       malformed input for a reply that was simply too big, then retries the
 *       same request and gets the same result.
 *
 *       Only for callers that parse. A stage writing prose for a person gets a
 *       short answer rather than an unusable one, and failing the run over it
 *       would throw away an answer that was fine.
 * param: as ask.
 * return: the response, and ErrReplyTruncated when the reply hit the cap.
 */
func (a *Agent) askParsed(ctx context.Context, l Lane, req *llm.ChatRequest) (*llm.ChatResponse, error) {
	resp, err := a.ask(ctx, l, req)
	if err != nil {
		return resp, err
	}
	if llm.Truncated(resp) {
		return resp, llm.TruncationError(req.MaxTokens)
	}
	return resp, nil
}
