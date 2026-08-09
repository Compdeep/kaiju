package llm

import (
	"context"
	"errors"
	"fmt"
)

// Noticing a reply that was cut off.
//
// A completion stops for one of two reasons: the model finished, or it reached
// max_tokens. The second is reported as finish_reason "length", and it means the
// reply is incomplete — the last sentence, or the last JSON object, is missing
// its end. A caller that parses the reply then sees malformed input and reports
// that instead: "invalid JSON" when the truth is "the answer did not fit".
//
// Every provider reports finish_reason, so this needs no model catalog, no
// configuration and no probing. It is the one check that works everywhere.

// ErrReplyTruncated reports that the model reached its reply cap before it
// finished. Wrapped with the cap that was in force, so the message says what to
// change.
var ErrReplyTruncated = errors.New("reply truncated at the token cap")

/*
 * CompleteChecked runs a completion and refuses to hide a truncated reply.
 * desc: Identical to Complete, except that a reply which stopped at the token
 *       cap comes back as ErrReplyTruncated. The response is returned alongside
 *       the error rather than discarded, so a caller that would rather keep a
 *       partial answer than none still can — but a caller doing the usual
 *       `if err != nil` gets the truncation named instead of a parse failure
 *       further down.
 *
 *       Use it wherever the reply is parsed. Where the reply is read as prose,
 *       a cut answer is short rather than unusable and Complete is fine.
 * param: ctx - as Complete.
 * param: req - as Complete; its MaxTokens appears in the error message.
 * return: the response, and ErrReplyTruncated when the reply hit the cap.
 */
func (c *Client) CompleteChecked(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	resp, err := c.Complete(ctx, req)
	if err != nil {
		return resp, err
	}
	if Truncated(resp) {
		return resp, fmt.Errorf("%w (max_tokens=%d): plan a shorter reply, or raise the cap", ErrReplyTruncated, req.MaxTokens)
	}
	return resp, nil
}

/*
 * Truncated reports whether a response stopped because it ran out of room.
 * desc: Exposed separately for callers that already have a response in hand —
 *       a streaming path, or one that wants to keep the partial text and only
 *       log the fact. Nil and empty responses are not truncated: a missing
 *       reply is a different fault.
 * param: resp - the response to inspect.
 * return: true when any choice reports finish_reason "length".
 */
func Truncated(resp *ChatResponse) bool {
	if resp == nil {
		return false
	}
	for _, ch := range resp.Choices {
		if ch.FinishReason == "length" {
			return true
		}
	}
	return false
}
