package agent

import (
	"context"
	"log"

	"github.com/Compdeep/kaiju/agent/llm"
)

/*
 * heavyRound makes one heavy-lane call under the round deadline, and answers
 * both ways a reasoning model comes back with nothing.
 *
 * Written once because it was written twice already, on the planner and on the
 * chat lane, and the lane that had neither is the one that cost four minutes:
 *
 *   draft_pitches compute_coder moonshotai/kimi-k2.6 — 239,465 ms,
 *   14,276 output tokens, then "llm response: empty content and no tool calls"
 *
 * Four minutes, fourteen thousand tokens, no code. The whole reply budget went
 * on reasoning and the forced tool call never started; nothing bounded the wait
 * and nothing tried again, so the step failed and the run replanned into the
 * same call. Both remedies already existed one file away.
 *
 * The two endings it handles are not the same fault:
 *
 *   The deadline. Cancelling the call returns an error and NO
 *   reply — so the reasoning it had already produced is kept as it streams and
 *   handed to the retry, which is the only reason this call streams at all.
 *
 *   The budget. The reply arrives, reports finish_reason "length", and holds
 *   neither content nor a tool call: every token went on thinking. The reply
 *   carries that thinking, so the retry starts from it.
 *
 * Either way the retry is the same: ask again with thinking off, under the
 * run's own remaining time. A model that cannot think has nothing to spend the
 * budget on but the answer.
 */

/*
 * heavyRound sends a heavy-lane call with both guards on it.
 * desc: As above. The reply is checked for truncation the way askParsed checks
 *       it, so a caller that relied on that keeps it — a half-written program
 *       reported as a finished one is the fault that check exists for.
 * param: ctx - the stage's context, carrying its trace identity.
 * param: graph - the run's graph, for the trigger that names this run's effort.
 * param: req - the request.
 * return: the reply, and any error left after both recoveries.
 */
func (a *Agent) heavyRound(ctx context.Context, graph *Graph, req *llm.ChatRequest) (*llm.ChatResponse, error) {
	trigger := Trigger{}
	if t := triggerFrom(ctx, graph); t != nil {
		trigger = *t
	}
	budget := a.roundBudget(ctx, Heavy, trigger)

	callCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	resp, thought, err := a.completeHeavyStreaming(callCtx, req)

	// Our own deadline, not the caller giving up. A run that was abandoned must
	// still be abandoned.
	if err != nil && callCtx.Err() != nil && ctx.Err() == nil {
		log.Printf("[dag] %s passed its %s deadline — re-asking with thinking off, carrying %d chars of reasoning",
			stageName(ctx), budget, len(thought))
		recovered, rerr := a.recoverDeadThought(retracing(ctx, stageTag(ctx, "recover_deadline")),
			Heavy, req, cutThought(thought))
		if rerr == nil && len(recovered.Choices) > 0 {
			return recovered, nil
		}
	}
	if err != nil {
		return resp, err
	}
	if len(resp.Choices) == 0 {
		return resp, nil // the caller's own check says what an empty reply means to it
	}

	// Nothing was cut off, because nothing was written. Asking again under the
	// same budget with thinking still on would spend it the same way.
	if resp.Choices[0].FinishReason == "length" && nothingVisible(resp.Choices[0]) {
		log.Printf("[dag] %s returned nothing — the reply budget of %d went on reasoning; re-asking with thinking off",
			stageName(ctx), req.MaxTokens)
		recovered, rerr := a.recoverDeadThought(retracing(ctx, stageTag(ctx, "recover_thought")),
			Heavy, req, resp)
		if rerr == nil && len(recovered.Choices) > 0 {
			return recovered, nil
		}
	}

	// The reply ran into its cap with something in it, which is a different
	// thing from the case above and belongs to the caller: a half-written
	// program written to disk and run is three debugging rounds spent on a
	// script that was never whole.
	if llm.Truncated(resp) {
		return resp, llm.TruncationError(req.MaxTokens)
	}
	return resp, nil
}

// stageTag names a retry after the stage that made it, so a trace can tell the
// planner's recovery from a coder's. Falls back to the bare reason outside any
// stage, where nothing is traced anyway.
func stageTag(ctx context.Context, reason string) string {
	if id, named := traceIDFrom(ctx); named && id.Tag != "" {
		return id.Tag + "_" + reason
	}
	return reason
}

// stageName is what to call this stage in a log line.
func stageName(ctx context.Context) string {
	id, named := traceIDFrom(ctx)
	switch {
	case !named:
		return "a model call"
	case id.NodeType != "":
		return id.NodeType
	default:
		return id.Tag
	}
}
