package agent

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/Compdeep/kaiju/agent/llm"
)

// What a run spent on model calls, summed as the calls return.
//
// Each stage keeps its own tokens in its own place — the planner on its
// DebugRecord, a compute node on its Node, every call on its LLMTrace — and
// none of those is a total. The question asked when a bill moves is per run:
// how many calls, how many tokens in and out, and how much of the out was
// thinking. One live run answered it only after its nine rows were pulled from
// llm_debug and summed by hand: 76,762 in, 4,619 out, of which the planner's
// one call was 26% and the four edge calls another 23%. This answers it at the
// end of every run, on the line that already says the run is complete.
//
// Carried on the context, like the run id, so every call made through the lanes
// reaches it without a new parameter on any of them.

type runTokens struct {
	calls, in, out, think atomic.Int64
}

type runTokensKey struct{}

// withRunTokens gives a run its account. Called once, where the run begins.
func withRunTokens(ctx context.Context, t *runTokens) context.Context {
	if t == nil {
		return ctx
	}
	return context.WithValue(ctx, runTokensKey{}, t)
}

// runTokensFrom is the run's account, or nil outside a run. Every method is
// safe on nil, so a caller never has to check.
func runTokensFrom(ctx context.Context) *runTokens {
	if ctx == nil {
		return nil
	}
	t, _ := ctx.Value(runTokensKey{}).(*runTokens)
	return t
}

// add counts one reply. The thinking is the provider's own split of the
// completion, and zero when it did not say.
func (t *runTokens) add(u llm.Usage) {
	if t == nil {
		return
	}
	t.calls.Add(1)
	t.in.Add(int64(u.PromptTokens))
	t.out.Add(int64(u.CompletionTokens))
	t.think.Add(int64(u.ReasoningTokens()))
}

// String is the account as fixed key=value pairs, for the run-complete line.
// A nil account prints as zeros rather than nothing, so the line keeps its
// shape.
func (t *runTokens) String() string {
	if t == nil {
		return "calls=0 in=0 out=0 think=0"
	}
	return fmt.Sprintf("calls=%d in=%d out=%d think=%d",
		t.calls.Load(), t.in.Load(), t.out.Load(), t.think.Load())
}
