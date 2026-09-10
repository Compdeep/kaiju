package agent

import (
	"context"
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
	mu sync.Mutex
	b  strings.Builder
}

// onChunk is the stream callback. Reasoning is kept; visible content is not —
// that comes back on the response when there is one, and duplicating it here
// would hand the retry its own half-written answer as though it were thought.
func (t *thinkingCapture) onChunk(chunk, kind string) {
	if kind != "reasoning" || chunk == "" {
		return
	}
	t.mu.Lock()
	t.b.WriteString(chunk)
	t.mu.Unlock()
}

// text is what was thought so far.
func (t *thinkingCapture) text() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(t.b.String())
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
 * completeHeavyStreaming sends a heavy-lane call and keeps the reasoning.
 * desc: The same call completeHeavy makes, streamed, so the thinking is
 *       collected as it arrives instead of being read off a reply that a
 *       cancelled call never produces.
 *
 *       Nothing is broadcast. The planner's reasoning is not an outcome for a
 *       reader — it goes to the trace and, when the call is cut, to the retry
 *       that has to finish the job.
 * param: ctx - the run context, which may carry a deadline.
 * param: req - the request.
 * return: the reply, whatever was thought before it ended, and any error.
 */
func (a *Agent) completeHeavyStreaming(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, string, error) {
	var cap thinkingCapture
	resp, err := a.askStreamResp(ctx, Heavy, req, cap.onChunk)
	return resp, cap.text(), err
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
