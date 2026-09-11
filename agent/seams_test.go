package agent

import (
	"context"
	"time"

	"github.com/Compdeep/kaiju/agent/llm"
)

// The seams these properties were written against.
//
// replyBudget, applyReasoningBudget and roundBudget were three resolvers in
// three files, each asking a different part of one question. boundsFor asks the
// whole of it now, at the one seam every call passes through.
//
// The tests that used those three are unchanged. That is deliberate: what they
// check — that a cap scales with the window, that the reply specs are exempt
// from the prompt scale, that an effort a model ignores is withheld, that the
// deadline never falls below the measured floor — is the same question after the
// move as before it, and a test rewritten in the same commit as the code it
// guards proves rather less than one that was not.
//
// So the three old names live on here, as the two or three lines each now takes
// against the new resolver. Each one exercises the real code beneath it; none
// reimplements any of it. When a test below is next edited for its own reasons,
// point it at boundsFor directly and delete the shim it used.

// replyBudget resolves one reply cap against the configured model, with no
// caller's own ask narrowing it.
func (a *Agent) replyBudget(s budgetSpec) int {
	return a.replyBound(budgetAsk{Stage: s, Model: a.cfg.LLMModel})
}

// applyReasoningBudget divides one request's OWN allowance, which is what the
// old function did: it ran after the cap was settled and read it off the
// request. boundsFor resolves the cap as well, so the allowance is handed in
// here rather than derived, and applyBounds — the real writer — does the rest.
func (a *Agent) applyReasoningBudget(ctx context.Context, req *llm.ChatRequest, model string) {
	thinking, effort := a.thinkingBound(ctx, model, req.MaxTokens)
	applyBounds(req, bounds{Reply: req.MaxTokens, Thinking: thinking, Effort: effort})
}

// roundBudget resolves the deadline from a Trigger, which is the parameter that
// kept this decision out of the door: the effort lived on the Trigger, the
// Trigger lived on the Graph, and only a caller holding one could set a
// deadline. deadlineFor reads the same effort off the context.
func (a *Agent) roundBudget(ctx context.Context, l Lane, t Trigger) time.Duration {
	if t.ReasoningEffort != "" {
		ctx = withLaneSelection(ctx, laneSelection{effort: t.ReasoningEffort})
	}
	c, model := a.lane(ctx, l)
	return a.deadlineFor(ctx, resolvedModel(model, c))
}

// prepare is everything the door does to a request before it is sent. It was a
// function; it is the first half of send.
func (a *Agent) prepare(ctx context.Context, l Lane, req *llm.ChatRequest) *llm.Client {
	c, laneModel := a.lane(ctx, l)
	if laneModel != "" {
		req.Model = laneModel
	}
	a.decideThinking(l, req)
	applyBounds(req, a.boundsFor(ctx, budgetAsk{
		Lane: l, Model: resolvedModel(laneModel, c), AskedFor: req.MaxTokens,
	}))
	return c
}

// withoutThinkingRetry is the second attempt for a model that CAN be asked to
// stop. retryRequest answers for both kinds now; zero means ask it to stop.
func withoutThinkingRetry(req *llm.ChatRequest, cut *llm.ChatResponse) *llm.ChatRequest {
	return retryRequest(req, cut, 0)
}

// planMaxTokens resolved the plan's cap from the configured value, raising it to
// fit the step count. replyPlanBudget and planFloor split that into a table
// entry and a stated minimum, and the operator's cap became a ceiling over both.
func (a *Agent) planMaxTokens(ctx context.Context) int {
	c, m := a.lane(ctx, Heavy)
	return a.replyBound(budgetAsk{
		Lane: Heavy, Stage: replyPlanBudget,
		Model: resolvedModel(m, c), MinReply: a.planFloor(),
	})
}

// completeHeavyStreaming sent a heavy call and returned what it thought. Every
// call streams its reasoning into a capture now; a stage that wants to READ it
// passes its own onChunk, which is what the planner does.
func (a *Agent) completeHeavyStreaming(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, string, error) {
	var think thinkingCapture
	resp, err := a.send(ctx, modelCall{Lane: Heavy, Req: req, OnChunk: think.onChunk})
	return resp, thinkingOf(resp, &think), err
}
