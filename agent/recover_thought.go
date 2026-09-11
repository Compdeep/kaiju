package agent

import (
	"strings"

	"github.com/Compdeep/kaiju/agent/llm"
)

// The most reasoning worth handing back. The end of a cut-off thought is the
// part that matters — a model stopped mid-sentence was closest to an answer at
// the end, not at the beginning — so what is kept is the tail.
const maxRecoveredReasoning = 6000

// What the door does with all of this: send makes the call, notices that
// nothing came back, and asks once more — with thinking off, or at a larger cap
// for a model the catalog says cannot be asked to stop. recoverDeadThought and
// withoutThinkingRetry stood here and did the first half of that; they were
// called from four places and reached by five lanes out of sixteen. See call.go.
//
// What is left here is what the second attempt SAYS, which is this file's own
// subject: how a model is told its thinking was cut off, and how to tell a reply
// that ran long from one that never started.

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
