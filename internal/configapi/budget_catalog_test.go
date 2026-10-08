package configapi

import "testing"

// A thinking budget is claimed only where it was measured.
//
// The flag is read at llm/resolve.go, which sends a bound only where it is
// true, so the flag IS the claim that a host honours one — see
// llm.TestABudgetIsSentOnlyWhereItIsHonoured for the gate and
// llm.TestAThinkingBudgetReachesTheWire for what a true flag puts on the wire.
//
// qwen3.6-35b-a3b is here as a negative case, and it is the reason this test
// exists. The flag was set on it by inference from the two entries above —
// same family, same provider, same parameter — and then measured: twelve calls
// over two prompts, reasoning.max_tokens of 5120 against an allowance of 8192.
// DekaLLM and Darkbloom both reasoned to the full 8192 and returned an empty
// content field, and bounded runs were indistinguishable from unbounded ones.
// The bound is not honoured, so the claim is not made.
//
// Family resemblance is not measurement. That is the whole content of this
// test, and the next entry somebody is tempted to fill in by analogy is what
// it is here to stop.
func TestABudgetIsClaimedOnlyWhereItWasMeasured(t *testing.T) {
	honours := map[string]bool{
		"qwen/qwen3.7-flash":   true,
		"qwen/qwen3.8-flash":   true,
		"qwen/qwen3.6-35b-a3b": false,
	}
	for id, want := range honours {
		t.Run(id, func(t *testing.T) {
			f, ok := Facts(id)
			if !ok {
				t.Fatalf("%s is not in the catalog", id)
			}
			if f.Thinking.Budget != want {
				t.Errorf("Thinking.Budget = %v, want %v", f.Thinking.Budget, want)
			}
		})
	}
}

// The rest of the line's facts, which the deployed stages do rely on: thinking
// happens by default and can be switched off. The reframe edges ask for off and
// it reaches the wire — 177 live calls recorded thinking_sent: off against
// tokens_thought: 0 — which only holds while Optional is true.
func TestTheDeployedModelReasonsAndCanBeToldNotTo(t *testing.T) {
	f, ok := Facts("qwen/qwen3.6-35b-a3b")
	if !ok {
		t.Fatal("qwen/qwen3.6-35b-a3b is not in the catalog")
	}
	if !f.Thinking.Default {
		t.Error("Thinking.Default is false — this model reasons unless told not to")
	}
	if !f.Thinking.Optional {
		t.Error("Thinking.Optional is false — WantOff would be dropped at resolve")
	}
}
