package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/Compdeep/kaiju/agent/llm"
)

// The verbs in front of the model.
//
// A call to a model is never just a call. Which model answers depends on the
// lane; how big a reply may be depends on that model's published limits and on
// how much of its window the prompt already took; how long it may take depends
// on what the model has been measured at; whether a reply that came back empty
// is a failure or a retry depends on whether the model can be asked to stop
// thinking. Each of those was applied at the call site, so each was applied
// differently or not at all.
//
// All of it is in call.go now, behind send. What is left here is the four ways
// a stage can ask — one document or streamed, parsed or prose — and the Lane
// they ask on. Each is one line, on purpose: a verb with a body is where the
// next divergence starts.

// Lane names which model answers a call.
//
// The split is cost. A run makes one or two Heavy calls and a dozen Light ones,
// and sending the cheap work to the reasoning model multiplies the bill for no
// gain — while sending the planner to the cheap model produces plans that do
// not parse.
type Lane int

const (
	// Heavy is the reasoning model: the planner, Holmes, the microplanner, the
	// compute architect and its coder. Slow, expensive, and the only one
	// trusted with a forced tool call.
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
 * param: ctx - the run's context, carrying the lane selection.
 * param: l - which model answers.
 * param: req - the request. Its Model, MaxTokens and Reasoning are set in place.
 * return: the provider's response.
 */
func (a *Agent) ask(ctx context.Context, l Lane, req *llm.ChatRequest) (*llm.ChatResponse, error) {
	return a.send(ctx, modelCall{Lane: l, Req: req})
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
	return a.send(ctx, modelCall{Lane: l, Req: req, Parsed: true})
}

/*
 * askStream sends one completion through a lane and streams the reply.
 * desc: The chunks arrive through onChunk as they come; the assembled text is
 *       returned at the end.
 *
 *       A PARTIAL reply is never failed. finish_reason arrives in the final
 *       frame and a stage that streams is showing text to a person as it
 *       lands — by the time the cut is known, the short answer has already been
 *       read, and there is nothing to fail.
 *
 *       An EMPTY one is different, and used to be reported as though it were
 *       the same. Nothing was shown, so nothing was read. send now asks again
 *       rather than reporting it, and only what survives that reaches here.
 * param: as ask, plus onChunk, called for each chunk with its kind.
 * return: the assembled reply.
 */
func (a *Agent) askStream(ctx context.Context, l Lane, req *llm.ChatRequest,
	onChunk func(chunk, kind string)) (string, error) {

	resp, err := a.send(ctx, modelCall{Lane: l, Req: req, OnChunk: onChunk})
	return streamedText(resp), err
}

/*
 * askStreamResp is askStream for a caller that needs the whole response.
 * desc: Same call, but the provider's response comes back rather than only the
 *       assembled text — token counts, finish reason, tool calls.
 * param: as askStream.
 * return: the response.
 */
func (a *Agent) askStreamResp(ctx context.Context, l Lane, req *llm.ChatRequest,
	onChunk func(chunk, kind string)) (*llm.ChatResponse, error) {

	return a.send(ctx, modelCall{Lane: l, Req: req, OnChunk: onChunk})
}

// streamedText is the assembled reply from a streamed response, and "" when
// there is none — a nil response, no choice, or a choice with no content.
func streamedText(resp *llm.ChatResponse) string {
	if resp == nil || len(resp.Choices) == 0 {
		return ""
	}
	return resp.Choices[0].Message.Content
}

// Telling the model its budget.
//
// max_tokens is not a hint. The model is never shown the number; the provider
// counts tokens as they are generated and stops at it, mid-sentence and
// mid-object. So a model writing to its own sense of length is cut wherever
// that lands, and every stage that parses the reply then reports malformed
// input for an answer that was simply too long.
//
// The only channel to the model is the prompt, and the only moment the number
// is final is after the lane is resolved and the cap settled — which is in
// applyBounds. A stage building its prompt cannot state it, because at that
// point the number does not exist.

// budgetMarker opens the line, and identifies it again on a retry. The planner
// builds its second attempt from the same message slice as its first, so
// without this the line would be appended twice.
const budgetMarker = "Reply budget:"

// budgetFloor is the smallest cap worth stating. Below it a caller wants a
// token or two — a forced route() call takes 16 — and the sentence would be
// larger than the budget it describes.
const budgetFloor = 256

/*
 * stateBudget appends the reply budget to a request's system message.
 * desc: The first system message only, and nothing at all when there is none:
 *       a request with no system message is a caller talking to the model
 *       directly, and this package does not edit that.
 * param: req - the request, whose system message is extended in place.
 * param: cap - the number the provider will stop at.
 */
func stateBudget(req *llm.ChatRequest, cap int) {
	for i := range req.Messages {
		if req.Messages[i].Role != "system" {
			continue
		}
		if strings.Contains(req.Messages[i].Content, budgetMarker) {
			return
		}
		// The ceiling AND a target, because stating only the ceiling reads as
		// permission to fill it. Measured: a working plan is 700 to 900 tokens
		// against a 4,096 cap, and the models that failed were not wrong but
		// verbose — one wrote 1,140 tokens for a job another did in 155. Half
		// the cap is well above what a good reply has ever needed and still
		// leaves the model room to be told it went long.
		// Short, because it is read on every call and paid for every time. The
		// two numbers are the point; the reason for them is one clause.
		req.Messages[i].Content += fmt.Sprintf(
			"\n\n%s the max size is %d tokens, but you should aim for about half of that.",
			budgetMarker, cap)
		return
	}
}
