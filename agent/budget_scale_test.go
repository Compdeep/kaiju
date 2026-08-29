package agent

import "testing"

// A deployment that sets nothing gets the table exactly as written.
func TestPromptScaleUnsetChangesNothing(t *testing.T) {
	win := ModelLimits(func(string) (int, int) { return 262144, 8192 })
	plain := &Agent{cfg: Config{ModelConfig: ModelConfig{LLMModel: "m", Limits: win}}}
	one := &Agent{cfg: Config{ModelConfig: ModelConfig{LLMModel: "m", Limits: win, PromptScale: 1}}}
	for _, s := range []budgetSpec{evidenceBudget, toolResultBudget, payloadBudget, toolIndexBudget} {
		if a, b := plain.budget(s), one.budget(s); a != b {
			t.Errorf("%s: unset=%d scale-1=%d; they must agree", s.Bounds, a, b)
		}
	}
	// And an out-of-range value is read as 1 rather than refused, or a mistyped
	// setting would have the engine send nothing.
	for _, bad := range []float64{-1, 0, 2} {
		odd := &Agent{cfg: Config{ModelConfig: ModelConfig{LLMModel: "m", Limits: win, PromptScale: bad}}}
		if odd.budget(evidenceBudget) != plain.budget(evidenceBudget) {
			t.Errorf("PromptScale=%v should read as 1", bad)
		}
	}
}

// Half the scale halves the caps that carry bulk, and moves nothing else.
func TestPromptScaleMovesBulkAndExemptsTheRest(t *testing.T) {
	win := ModelLimits(func(string) (int, int) { return 262144, 8192 })
	full := &Agent{cfg: Config{ModelConfig: ModelConfig{LLMModel: "m", Limits: win}}}
	half := &Agent{cfg: Config{ModelConfig: ModelConfig{LLMModel: "m", Limits: win, PromptScale: 0.5}}}

	for _, c := range []struct {
		name string
		spec budgetSpec
		want int
	}{
		{"evidence", evidenceBudget, 16000},
		{"tool result", toolResultBudget, 8000},
		{"payload", payloadBudget, 24000},
	} {
		if got := half.budget(c.spec); got != c.want {
			t.Errorf("%s at scale 0.5 = %d, want %d (full is %d)", c.name, got, c.want, full.budget(c.spec))
		}
	}

	// The tool index is exempt: cutting it removes tools from the planner's
	// view and breaks the calls it writes, which is not a size question.
	if got, want := half.budget(toolIndexBudget), full.budget(toolIndexBudget); got != want {
		t.Errorf("the tool index moved with the scale: %d, want %d", got, want)
	}
	// So is every reply cap — the fault is request size, not reply size.
	for _, s := range []budgetSpec{
		replyDecisionBudget, replyBriefBudget, replyEdgeBudget,
		replyAnalysisBudget, replyCodeBudget, replyStructuredBudget,
	} {
		if got, want := half.replyBudget(s), full.replyBudget(s); got != want {
			t.Errorf("%q moved with the scale: %d, want %d", s.Bounds, got, want)
		}
	}
}

// Base is a floor the scale may not breach: it is the size this engine is known
// to work at, and no scaling argument is worth going below it.
func TestPromptScaleNeverGoesBelowBase(t *testing.T) {
	win := ModelLimits(func(string) (int, int) { return 262144, 8192 })
	tiny := &Agent{cfg: Config{ModelConfig: ModelConfig{LLMModel: "m", Limits: win, PromptScale: 0.01}}}
	for _, s := range []budgetSpec{evidenceBudget, toolResultBudget, payloadBudget} {
		if got := tiny.budget(s); got != s.Base {
			t.Errorf("%q at scale 0.01 = %d, want the Base floor %d", s.Bounds, got, s.Base)
		}
	}
}
