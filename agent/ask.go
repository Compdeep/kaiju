package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

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
 * desc: Resolve the lane, stamp its model on the request, send. The client
 *       sizes the reply against that model as it sends, which is why the model
 *       has to be stamped first.
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
	c := a.prepare(ctx, l, req)
	started := time.Now()
	resp, err := c.Complete(ctx, req)
	a.writeTrace(ctx, req, resp, err, started)
	return resp, err
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

/*
 * askStream sends one completion through a lane and streams the reply.
 * desc: The same four steps as ask, and the same reason for their order. The
 *       chunks arrive through onChunk as they come; the assembled text is
 *       returned at the end.
 *
 *       Streaming has no truncation check. finish_reason arrives in the final
 *       frame and a stage that streams is showing text to a person as it
 *       lands — by the time the cut is known, the short answer has already been
 *       read, and there is nothing to fail.
 * param: as ask, plus onChunk, called for each chunk with its kind.
 * return: the assembled reply.
 */
func (a *Agent) askStream(ctx context.Context, l Lane, req *llm.ChatRequest,
	onChunk func(chunk, kind string)) (string, error) {

	c := a.prepare(ctx, l, req)
	started := time.Now()
	text, err := c.CompleteStream(ctx, req, onChunk)
	// A streamed reply carries no response to read, so the trace records what
	// was asked and what came back as text.
	a.writeTrace(ctx, req, streamedResponse(text), err, started)
	return text, err
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

	c := a.prepare(ctx, l, req)
	started := time.Now()
	resp, err := c.CompleteStreamResp(ctx, req, onChunk)
	a.writeTrace(ctx, req, resp, err, started)
	return resp, err
}

/*
 * prepare is everything the door does to a request before it is sent.
 * desc: Split out because four ways of sending share it — completion,
 *       completion-that-parses, and the two streaming forms — and a step added
 *       to one of them by hand is a step three of them do not get. That is the
 *       fault this whole door exists to remove, so it must not be reintroduced
 *       inside it.
 *
 *       Sizing the reply is not here: it is the client's, applied on every send
 *       whoever makes it, so a tool holding a bare client gets it too.
 * param: ctx, l, req - as ask.
 * return: the client to send with.
 */
func (a *Agent) prepare(ctx context.Context, l Lane, req *llm.ChatRequest) *llm.Client {
	c, model := a.lane(ctx, l)
	if model != "" {
		req.Model = model
	}

	// Fix the cap here rather than leaving it to the send, so the number stated
	// below is the number the provider stops at. capReply then finds nothing
	// left to lower.
	if cap := c.ReplyCap(req); cap >= budgetFloor {
		req.MaxTokens = cap
		stateBudget(req, cap)
	}

	// Images ride the context so they re-attach on every heavy call this turn,
	// staying visible across follow-ups. Heavy only, as before: the model check
	// would make this safe on any lane, but widening it is a behaviour change
	// and belongs with the stage that moves the remaining callers here.
	if l == Heavy {
		if imgs := visionImagesFrom(ctx); len(imgs) > 0 && IsVisionModel(req.Model) {
			llm.AttachImages(req.Messages, imgs)
		}
	}
	return c
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
// is final is after the lane is resolved and the cap settled — which is here.
// A stage building its prompt cannot state it, because at that point the number
// does not exist.

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
		req.Messages[i].Content += fmt.Sprintf(
			"\n\n%s about %d tokens. Generation stops there, so a longer reply is cut "+
				"off part-way and cannot be used. Plan the length before you start.",
			budgetMarker, cap)
		return
	}
}

// streamedResponse wraps assembled stream text as a response, so a streamed
// call traces the same shape as any other. Token counts are absent because a
// stream does not report them.
func streamedResponse(text string) *llm.ChatResponse {
	if text == "" {
		return nil
	}
	return &llm.ChatResponse{Choices: []llm.Choice{{Message: llm.Message{Content: text}}}}
}
