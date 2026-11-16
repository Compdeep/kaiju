package agent

import "testing"

// The trace's nodes add up to what the run spent: a reflector keeps its own
// count, and each stage node takes what no node has shown yet.
func TestShownTokensAddUpToTheRun(t *testing.T) {
	acct := &runTokens{}
	s := shownTokens{account: acct}
	acct.in.Store(100)
	acct.out.Store(10)

	running := &NodeInfo{ID: "executive", Type: "executive", State: "running"}
	s.put(running)
	if running.TokensIn != 0 {
		t.Fatalf("a running stage took %d tokens", running.TokensIn)
	}
	refl := &NodeInfo{ID: "n2", Type: "reflection", State: "resolved", TokensIn: 30, TokensOut: 3}
	s.put(refl)
	s.put(&NodeInfo{ID: "n2", Type: "reflection", State: "resolved", TokensIn: 30, TokensOut: 3})

	exec := &NodeInfo{ID: "executive", Type: "executive", State: "resolved"}
	s.put(exec)
	if exec.TokensIn != 70 || exec.TokensOut != 7 {
		t.Fatalf("executive got %d/%d, want 70/7", exec.TokensIn, exec.TokensOut)
	}
	again := &NodeInfo{ID: "executive", Type: "executive", State: "failed"}
	s.put(again)
	if again.TokensIn != 70 || again.TokensOut != 7 {
		t.Fatalf("executive broadcast again got %d/%d, want 70/7", again.TokensIn, again.TokensOut)
	}

	acct.in.Store(150)
	acct.out.Store(20)
	agg := &NodeInfo{ID: "aggregator", Type: "aggregator", State: "resolved"}
	s.put(agg)
	if agg.TokensIn != 50 || agg.TokensOut != 10 {
		t.Fatalf("aggregator got %d/%d, want 50/10", agg.TokensIn, agg.TokensOut)
	}
	if sum := refl.TokensIn + exec.TokensIn + agg.TokensIn; sum != 150 {
		t.Fatalf("nodes add up to %d in, the run spent 150", sum)
	}
}

// A run with no account and a nil node change nothing.
func TestShownTokensWithoutAnAccount(t *testing.T) {
	var s shownTokens
	s.put(nil)
	n := &NodeInfo{ID: "aggregator", Type: "aggregator", State: "resolved"}
	s.put(n)
	if n.TokensIn != 0 || n.TokensOut != 0 {
		t.Fatalf("got %d/%d with no account", n.TokensIn, n.TokensOut)
	}
}
