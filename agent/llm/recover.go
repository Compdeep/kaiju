package llm

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
)

// Asking again, once, and only where asking again can help.
//
// A retry that sends the same request gets the same answer. So each ending is
// asked a different question: is the request wrong, is the moment wrong, or is
// nothing wrong and the provider simply failed?
//
//	the request is wrong   a reply came back empty, or our deadline expired
//	                       → change it: stop the thinking, or give it more room
//	the moment is wrong    the other end said it was busy
//	                       → the same request, after the wait it asked for
//	nothing is wrong       transport, a 5xx, a 200 with no reply
//	                       → the same request
//	nothing will help      bad credentials, a reply that was merely cut short
//	                       → report it
//
// Exactly one retry, never recursive. The breaker sits in front of both
// attempts, so a provider that is down still fails fast.

// maxRecoveredReasoning is the most of a cut thought worth handing back. The
// end is the part that matters — a model stopped mid-sentence was closest to an
// answer at the end, not at the beginning — so what is kept is the tail.
const maxRecoveredReasoning = 6000

// raisedCapFloor is the smallest second-attempt cap worth giving a model that
// cannot be asked to stop thinking.
//
// Measured on the planner prompt: the most any model spent thinking was 7,424
// tokens and the largest visible answer was 1,704. It is a good chance rather
// than a guarantee — on a trivial prompt one model reasoned for 16,002 tokens,
// so the tail is not bounded by anything a number here can express.
const raisedCapFloor = 9216

/*
 * recoverable decides whether asking again can help, and with what.
 * desc: The request comes back changed only where the request is what went
 *       wrong. Everywhere else it is returned as it was, because nothing about
 *       it is at fault and altering it would make the second attempt a
 *       different question.
 * param: req - the request as it was sent. Copied, never modified.
 * param: resp - the reply, where one arrived.
 * param: err - what the call returned.
 * return: the request to send, how long to wait first, and whether to bother.
 */
func (c *Client) recoverable(req *ChatRequest, resp *ChatResponse, err error) (*ChatRequest, time.Duration, bool) {
	if req == nil {
		return nil, 0, false
	}
	kind := Classify(err)
	if err == nil && emptyReply(resp) {
		kind = KindEmpty
	}

	switch kind {
	case KindCredentials, KindTruncated, KindNone:
		// Nothing to gain. A run that retries an invalid key spends its whole
		// budget failing identically; a reply that was cut short is an answer,
		// and what to do with it belongs to whoever asked.
		return nil, 0, false

	case KindTransport, KindUpstream, KindRateLimited:
		// Nothing about the request is wrong, so the same one goes again — but
		// only from a healthy state. A retry is for a blip; during an outage it
		// is the wrong thing twice over, doubling the traffic to a provider that
		// is already answering nothing and halving the breaker's tolerance.
		if !providerBreaker.healthy() {
			return nil, 0, false
		}
		wait, _ := Backoff(err)
		return req, wait, true

	case KindTimeout, KindEmpty:
		// The request IS what went wrong: it asked for something this model
		// could not fit in what it was given. Sending it again unchanged is the
		// first attempt a second time.
		return c.askDifferently(req, resp)
	}
	return nil, 0, false
}

/*
 * askDifferently builds a second attempt that can succeed where the first could
 * not.
 * desc: Two remedies, and which applies is a property of the model rather than
 *       of the request.
 *
 *       A model that can be asked to stop thinking is asked to stop. It cannot
 *       reason, so it must produce output, and it starts from the thinking
 *       already paid for rather than from nothing.
 *
 *       A model that cannot — the catalog's reasoning_optional — is given more
 *       room instead. Telling it to stop is the same call again: it spends the
 *       budget the same way and returns nothing twice.
 *
 *       Where neither is possible there is no second attempt, because there is
 *       no second question to ask.
 * param: req - the original request.
 * param: resp - the reply, for whatever reasoning it managed.
 * return: the retry, no wait, and whether one is worth making.
 */
func (c *Client) askDifferently(req *ChatRequest, resp *ChatResponse) (*ChatRequest, time.Duration, bool) {
	f, known := c.facts(modelOf(req, c))
	if !known {
		// Nothing is known about this model, so nothing can be changed with any
		// confidence. A client told nothing about its models is left alone here
		// as everywhere else.
		return nil, 0, false
	}

	retry := *req
	// A copy of the messages too: the caller may still hold the original, and a
	// retry that mutates what it retries cannot be run twice.
	retry.Messages = append(append([]Message{}, req.Messages...), Message{
		Role:    "user",
		Content: recoveryPrompt(reasoningOf(resp)),
	})

	if f.Thinking.Optional {
		retry.Think = &Reasoning{Want: WantOff}
		retry.Reasoning = nil // re-resolved on the way out
		return &retry, 0, true
	}

	raised := raisedCap(req.MaxTokens, f.MaxOutputTokens)
	if raised == 0 {
		return nil, 0, false
	}
	log.Printf("[llm] %s cannot be asked to stop reasoning — asking again at %d tokens instead of %d",
		modelOf(req, c), raised, req.MaxTokens)
	retry.MaxTokens = raised
	retry.Reasoning = nil
	return &retry, 0, true
}

// raisedCap is the larger reply budget a locked model's second attempt gets, or
// zero when the model has no more to give.
func raisedCap(current, maxOutput int) int {
	want := current * 2
	if want < raisedCapFloor {
		want = raisedCapFloor
	}
	if maxOutput > 0 && want > maxOutput {
		want = maxOutput
	}
	if want <= current {
		return 0
	}
	return want
}

// emptyReply reports whether a call succeeded and produced nothing: no content
// and no tool call. Reasoning does not count — it is what consumed the budget,
// not what the budget was for.
func emptyReply(resp *ChatResponse) bool {
	return resp != nil && len(resp.Choices) > 0 && nothingVisible(resp.Choices[0])
}

// reasoningOf is whatever thinking a reply managed before it ended.
func reasoningOf(resp *ChatResponse) string {
	if resp == nil || len(resp.Choices) == 0 {
		return ""
	}
	return strings.TrimSpace(resp.Choices[0].Message.Reasoning)
}

// modelOf is the model a request will reach: its own, or the client's default.
func modelOf(req *ChatRequest, c *Client) string {
	if req.Model != "" {
		return req.Model
	}
	return c.model
}

/*
 * recoveryPrompt tells the model what happened to its own thinking.
 * desc: Framed as unfinished on purpose. Handed back as a finished argument, a
 *       model treats a half-formed conclusion as settled and answers from it;
 *       told it was cut off, it closes the thought rather than trusting it.
 *
 *       Carried in a user turn rather than an assistant one, because an
 *       assistant message holding reasoning with no matching tool call is
 *       refused by some providers — and this has to work on whatever rig
 *       produced the dead call in the first place.
 * param: reasoning - what the first attempt thought, possibly none.
 * return: the text to append.
 */
func recoveryPrompt(reasoning string) string {
	var b strings.Builder
	b.WriteString("Your previous attempt returned nothing: it was still thinking when it " +
		"stopped. Do not think further — answer now, in the form the call asks for.\n")
	if reasoning != "" {
		if len(reasoning) > maxRecoveredReasoning {
			reasoning = "…" + reasoning[len(reasoning)-maxRecoveredReasoning:]
		}
		b.WriteString("\n## Previous reasoning\n")
		b.WriteString("This is your own thinking from that attempt. It was cut off before " +
			"it finished, so treat it as working notes rather than as a settled conclusion.\n\n")
		b.WriteString(reasoning)
		b.WriteString("\n")
	}
	return b.String()
}

/*
 * waitBefore serves a backoff without outliving the caller's patience.
 * param: ctx - the caller's context.
 * param: d - how long to wait; zero returns at once.
 * return: whether the wait completed rather than the context ending.
 */
func waitBefore(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// describeRetry is the log line for a second attempt: what went wrong, and what
// is being done differently.
func describeRetry(err error, resp *ChatResponse, wait time.Duration) string {
	kind := Classify(err)
	if err == nil && emptyReply(resp) {
		kind = KindEmpty
	}
	if wait > 0 {
		return fmt.Sprintf("%s — asking again in %s", kind, wait)
	}
	return fmt.Sprintf("%s — asking again", kind)
}
