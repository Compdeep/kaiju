package agent

import (
	"fmt"
	"strings"
	"sync"

	"github.com/Compdeep/kaiju/agent/llm"
)

/*
 * thinkingCapture keeps what a model thought, as it thinks it.
 *
 * A cancelled call returns an error and no reply, so the reasoning it produced
 * is gone — the transport is torn down and there is nothing to read it off.
 * That is why the deadline recovery started cold: the planner's two minutes of
 * thinking existed only as tokens that streamed past and were never collected.
 *
 * Held here rather than taken from the response, so it survives however the
 * call ends. What arrived, arrived.
 */
type thinkingCapture struct {
	mu      sync.Mutex
	thought strings.Builder
	written strings.Builder
}

/*
 * onChunk is the stream callback, and it keeps both kinds of chunk.
 * desc: A model delivers its thinking one of two ways — as reasoning chunks, or
 *       written into the content between <think> and </think> — and a caller
 *       that keeps only the first can watch a model think for two minutes and
 *       collect nothing, because every chunk it sent was content.
 *
 *       Content is kept for that reason alone. What is taken from it is the
 *       thinking inside those tags and nothing else, so a half-written answer
 *       is never handed to a retry as though it were thought.
 * param: chunk - the text that arrived.
 * param: kind - "reasoning" or "content", as the client tags it.
 */
func (t *thinkingCapture) onChunk(chunk, kind string) {
	if chunk == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if kind == "reasoning" {
		t.thought.WriteString(chunk)
		return
	}
	t.written.WriteString(chunk)
}

// text is what was thought so far: the reasoning chunks, and the thinking
// written inline in the content. An unterminated <think> counts — a model cut
// off mid-thought never writes the closing tag.
func (t *thinkingCapture) text() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	got := t.thought.String()
	if _, inline := llm.LiftThinking(t.written.String()); inline != "" {
		if got != "" {
			got += "\n"
		}
		got += inline
	}
	return strings.TrimSpace(got)
}

// cutThought wraps captured thinking as a response, so a caller recovering from
// a cut reply and one recovering from a cancelled call hand the recovery the
// same thing — recoverDeadThought reads the reasoning off a response, and a
// cancelled call has none to read it off.
//
// Nil when nothing was thought: there is then nothing to hand on, and
// recoveryPrompt says only that the attempt produced nothing.
func cutThought(reasoning string) *llm.ChatResponse {
	if reasoning == "" {
		return nil
	}
	return &llm.ChatResponse{Choices: []llm.Choice{{
		FinishReason: "length",
		Message:      llm.Message{Role: "assistant", Reasoning: reasoning},
	}}}
}

/*
 * thinkingOf is what the model thought, taken from wherever it survived.
 * desc: The reply where there is one, and the capture where there is not.
 *
 *       That order matters, and getting it the other way round is why a whole
 *       run's planner reasoning went missing while the model was plainly
 *       thinking: the client assembles a reply two ways — reasoning chunks, and
 *       <think> lifted out of the finished content — and only the first of
 *       those passes through a callback. A call that completes has already had
 *       both done for it, on the reply.
 *
 *       The capture is for the call that never produces a reply at all, which
 *       is every call stopped by a deadline.
 * param: resp - the reply, or nil.
 * param: cap - what was collected while it streamed.
 * return: the thinking, or "" when there was none.
 */
func thinkingOf(resp *llm.ChatResponse, cap *thinkingCapture) string {
	if got := reasoningOf(resp); got != "" {
		return got
	}
	return cap.text()
}

// The most thinking worth putting on the wire for a reader.
//
// A planning call can reason for tens of thousands of characters, and every
// node event is sent to every open trace. What a reader wants is the shape of
// it — where it started, what it settled on — so both ends are kept and the
// middle is dropped, with the count of what went.
const maxShownThinking = 8000

/*
 * shownThinking is the reasoning as a reader receives it.
 * desc: Kept from both ends, unlike the copy handed to a retry, which keeps only
 *       the tail. A retry needs the conclusion it was about to reach; a person
 *       reading the trace sees the opening line first and needs it to be the
 *       model's opening line.
 * param: reasoning - what was thought, at whatever length.
 * return: the whole thing when it fits, and both ends when it does not.
 */
func shownThinking(reasoning string) string {
	if len(reasoning) <= maxShownThinking {
		return reasoning
	}
	half := maxShownThinking / 2
	dropped := len(reasoning) - maxShownThinking
	return reasoning[:half] +
		fmt.Sprintf("\n\n… %d characters of thinking not shown …\n\n", dropped) +
		reasoning[len(reasoning)-half:]
}
