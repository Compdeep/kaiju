package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
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
	if Truncated(resp) && !KeepCappedObject(req, resp) {
		return resp, TruncationError(req.MaxTokens)
	}
	return resp, nil
}

/*
 * KeepCappedObject rescues a forced reply that finished its object and then
 *       did not stop.
 * desc: A reply cut at the cap is usually a reply that never finished. One kind
 *       did finish: run 77f395aa (2026-11-21), the reflector on gpt-6-luna wrote
 *       one complete, valid decision and then the same decision 28 more times
 *       until the cap. Failing the call threw away a good answer, and the run
 *       ended with none.
 *
 *       So when a call forced one shape — a tool, or a JSON schema — and every
 *       tool call's arguments (or the content, for a schema) open with one
 *       complete JSON object, that object is kept, the rest is dropped, and the
 *       reply is marked as having stopped. Anything less — an object still open
 *       at the cap, or a reply that is not an object — is left cut, and the
 *       caller gets ErrReplyTruncated as before.
 *
 *       Called by every parsing door (CompleteChecked here, the agent's
 *       askParsed), so no stage has to remember it.
 * param: req - what was asked; only a forced shape is rescued.
 * param: resp - the cut reply, changed in place when rescued.
 * return: true when the reply was rescued.
 */
func KeepCappedObject(req *ChatRequest, resp *ChatResponse) bool {
	if req == nil || resp == nil || len(resp.Choices) != 1 || !(asksForOneShape(req) || req.ResponseFormat != nil) {
		return false
	}
	ch := &resp.Choices[0]
	if ch.FinishReason != "length" {
		return false
	}
	var kept, dropped int
	if calls := ch.Message.ToolCalls; len(calls) > 0 {
		trimmed := make([]string, len(calls))
		for i, tc := range calls {
			obj, ok := firstObject(tc.Function.Arguments)
			if !ok {
				return false
			}
			trimmed[i] = obj
			kept += len(obj)
			dropped += len(tc.Function.Arguments) - len(obj)
		}
		for i := range calls {
			calls[i].Function.Arguments = trimmed[i]
		}
	} else if req.ResponseFormat != nil {
		obj, ok := firstObject(ch.Message.Content)
		if !ok {
			return false
		}
		kept, dropped = len(obj), len(ch.Message.Content)-len(obj)
		ch.Message.Content = obj
	} else {
		return false
	}
	ch.FinishReason = "stop"
	log.Printf("[llm] reply hit the %d-token cap after a complete object: kept the object (%d bytes), dropped %d bytes after it",
		req.MaxTokens, kept, dropped)
	return true
}

// firstObject is the complete JSON object raw opens with, as written, or false
// when raw does not open with one that closes.
func firstObject(raw string) (string, bool) {
	trimmed := strings.TrimLeft(raw, " \t\r\n")
	if !strings.HasPrefix(trimmed, "{") {
		return "", false
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(trimmed)))
	var v map[string]any
	if err := dec.Decode(&v); err != nil {
		return "", false
	}
	return trimmed[:dec.InputOffset()], true
}

/*
 * TruncationError is the error a truncated reply produces.
 * desc: Exported so a caller that already has a response — one routed through
 *       its own lane selection, say — reports the same thing in the same words
 *       as CompleteChecked, rather than inventing a second wording for one
 *       fault.
 * param: maxTokens - the cap that was in force, which is what to change.
 * return: an error wrapping ErrReplyTruncated.
 */
func TruncationError(maxTokens int) error {
	return fmt.Errorf("%w (max_tokens=%d): plan a shorter reply, or raise the cap", ErrReplyTruncated, maxTokens)
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
