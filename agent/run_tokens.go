package agent

import (
	"context"
	"fmt"
	"sync"
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

// shownTokens puts the run's tokens on the trace's nodes, so the trace's total
// is the run's total.
//
// Only a reflector node carried its tokens, so the trace summed three of a
// run's fifteen calls: run f8e43d3a (2026-11-16) showed 56.6k where the run
// spent 246k in and 11k out. The planner, preflight, reframe edges and the
// aggregator are drawn as stage nodes the scheduler builds by hand, and none
// had a count. Each stage node now takes, when it finishes, what the run spent
// that no node has shown yet: the planner's calls with the reframe edge before
// it, preflight's two calls, the aggregator with the edge that briefs it.
type shownTokens struct {
	mu      sync.Mutex
	account *runTokens
	in, out int64
	byNode  map[string][2]int
}

// stageNode reports whether a trace node is one the scheduler draws for a
// stage rather than a node of the graph.
func stageNode(info *NodeInfo) bool {
	switch info.Type {
	case "preflight", "executive", "aggregator":
		return true
	}
	return false
}

/*
 * put records a node's tokens, or gives a finished stage node the run's
 * tokens no node has shown.
 * desc: A node broadcast again keeps the count it was given, so nothing is
 *       counted twice. Safe on a nil node and on a run with no account.
 * param: info - the node as it is about to be broadcast; changed in place.
 */
func (s *shownTokens) put(info *NodeInfo) {
	if info == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byNode == nil {
		s.byNode = map[string][2]int{}
	}
	prev, seen := s.byNode[info.ID]
	if info.TokensIn > 0 || info.TokensOut > 0 {
		s.in += int64(info.TokensIn - prev[0])
		s.out += int64(info.TokensOut - prev[1])
		s.byNode[info.ID] = [2]int{info.TokensIn, info.TokensOut}
		return
	}
	if s.account == nil || !stageNode(info) || (info.State != "resolved" && info.State != "failed") {
		return
	}
	if seen {
		info.TokensIn, info.TokensOut = prev[0], prev[1]
		return
	}
	in := max(s.account.in.Load()-s.in, 0)
	out := max(s.account.out.Load()-s.out, 0)
	if in == 0 && out == 0 {
		return
	}
	info.TokensIn, info.TokensOut = int(in), int(out)
	s.in += in
	s.out += out
	s.byNode[info.ID] = [2]int{info.TokensIn, info.TokensOut}
}
