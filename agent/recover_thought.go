package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/Compdeep/kaiju/agent/llm"
)

// The most reasoning worth handing back. The end of a cut-off thought is the
// part that matters — a model stopped mid-sentence was closest to an answer at
// the end, not at the beginning — so what is kept is the tail.
const maxRecoveredReasoning = 6000

/*
 * recoverDeadThought re-asks a call whose reasoning consumed the whole budget.
 * desc: A fail-safe, and it should almost never fire.
 *
 *       Where a model honours reasoning.max_tokens the provider stops the
 *       thinking itself and leaves room for the reply; where it honours
 *       max_tokens the reply is at least cut off with something in it. This is
 *       for neither happening: a model that loops, and a rig behind an
 *       OpenRouter model id that ignores the parameters it was sent. The same
 *       id served by a different upstream behaves differently, so this cannot
 *       be gated on what the catalog believes about the model — it fires on
 *       what came back.
 *
 *       What came back is finish_reason "length" with nothing visible: the
 *       budget went on reasoning and the reply never started. One planner call
 *       in a live run spent 8,192 tokens that way and returned an empty string,
 *       59 seconds in.
 *
 *       The reasoning itself is not lost — the provider returns it on the
 *       message — so it is handed back with thinking switched OFF. That second
 *       call cannot reason, so it must produce output, and it starts from the
 *       thinking already paid for rather than from nothing.
 * param: ctx - the run context.
 * param: lane - the lane the original call was made on.
 * param: req - the original request. Copied, never modified.
 * param: cut - the reply that was cut off, carrying the reasoning it produced.
 * return: the second response, or an error if there was nothing to recover from.
 */
func (a *Agent) recoverDeadThought(ctx context.Context, lane Lane, req *llm.ChatRequest, cut *llm.ChatResponse) (*llm.ChatResponse, error) {
	retry := withoutThinkingRetry(req, cut)
	if retry == nil {
		return nil, fmt.Errorf("no request to recover")
	}
	return a.askParsed(ctx, lane, retry)
}

/*
 * withoutThinkingRetry is the second attempt, as a request.
 * desc: Separated from the send so the shape of the retry can be checked
 *       without a provider — what it asks for is the thing worth checking, not
 *       that a stub answered.
 * param: req - the original request. Never modified.
 * param: cut - the reply that was cut off, for whatever reasoning it managed.
 * return: the retry, or nil if there was no request to build one from.
 */
func withoutThinkingRetry(req *llm.ChatRequest, cut *llm.ChatResponse) *llm.ChatRequest {
	if req == nil {
		return nil
	}
	// A copy, and a copy of the messages: the caller may still hold the
	// original, and a retry that mutates the request it retries cannot be run
	// twice.
	retry := *req
	retry.Messages = append(append([]llm.Message{}, req.Messages...), llm.Message{
		Role:    "user",
		Content: recoveryPrompt(reasoningOf(cut)),
	})

	// The whole point. Thinking off means the budget belongs to the reply, and
	// a model that cannot think has nothing to spend it on but the answer.
	llm.WithoutReasoning(&retry)
	return &retry
}

// reasoningOf is what the cut reply managed to think, or "" when the provider
// returned none. Some report reasoning tokens in their usage and no reasoning
// text; the recovery is still worth making there, as a retry that cannot think.
func reasoningOf(resp *llm.ChatResponse) string {
	if resp == nil || len(resp.Choices) == 0 {
		return ""
	}
	return strings.TrimSpace(resp.Choices[0].Message.Reasoning)
}

/*
 * recoveryPrompt tells the model what happened to its own thinking.
 * desc: Framed as unfinished on purpose. Handed back as a finished argument, a
 *       model treats a half-formed conclusion as settled and plans from it;
 *       told it was cut off, it closes the thought rather than trusting it.
 *
 *       Carried in a user turn rather than an assistant one, because an
 *       assistant message holding reasoning with no matching tool call is
 *       refused by some providers — and this has to work on whatever rig
 *       produced the dead call in the first place.
 * param: reasoning - what the cut reply thought, possibly empty.
 * return: the text to append.
 */
func recoveryPrompt(reasoning string) string {
	var b strings.Builder
	b.WriteString("Your previous attempt spent its entire reply budget thinking and " +
		"returned nothing. Do not think further — answer now, in the form the call asks for.\n")
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
 * nothingVisible reports whether a reply produced no output at all.
 * desc: The difference between a plan that ran long and a plan that never
 *       started. Both arrive as finish_reason "length"; only one of them has
 *       anything in it, and the remedies are opposite — one needs a shorter
 *       plan, the other needs the thinking stopped.
 *
 *       Reasoning does not count as output. It is what consumed the budget, not
 *       what the budget was for, and treating it as output is the confusion this
 *       exists to prevent.
 * param: c - the choice as returned.
 * return: whether there is neither a tool call nor any content to read.
 */
func nothingVisible(c llm.Choice) bool {
	return len(c.Message.ToolCalls) == 0 && strings.TrimSpace(c.Message.Content) == ""
}
