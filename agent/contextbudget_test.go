package agent

import "testing"

func budgetAgent(heavy, executor string, limits ModelLimits) *Agent {
	return &Agent{cfg: Config{ModelConfig: ModelConfig{
		LLMModel: heavy, ExecutorModel: executor, Limits: limits,
	}}}
}

// The common case: no catalog, so nothing changes. Every deployment that
// supplies no model limits must behave exactly as it did before.
func TestScaleBudget_NoCatalogChangesNothing(t *testing.T) {
	a := budgetAgent("some/model", "", nil)
	for _, want := range []int{6000, 12000, 24000, 32000} {
		if got := a.scaleBudget(want); got != want {
			t.Errorf("scaleBudget(%d) = %d, want it untouched", want, got)
		}
	}
}

// A model the catalog does not carry is the same as no catalog.
func TestScaleBudget_UnknownModelChangesNothing(t *testing.T) {
	a := budgetAgent("selfhosted/qwen3-32b", "", limitsFor("openai/gpt-4.1", 1047576, 32768))
	if got := a.scaleBudget(12000); got != 12000 {
		t.Fatalf("got %d, want 12000 untouched", got)
	}
}

// At the reference window the budgets are what they always were — the whole
// point of choosing that reference.
func TestScaleBudget_ReferenceWindowIsAFixedPoint(t *testing.T) {
	a := budgetAgent("m", "", limitsOf(referenceWindowTokens, 4096))
	for _, want := range []int{6000, 12000, 24000} {
		if got := a.scaleBudget(want); got != want {
			t.Errorf("at the reference window scaleBudget(%d) = %d, want unchanged", want, got)
		}
	}
}

func TestScaleBudget_LargerWindowGetsMore(t *testing.T) {
	// 40,960 tokens — a plausible self-hosted qwen3-32b. 40960/32768 = 1.25.
	a := budgetAgent("qwen3-32b", "", limitsOf(40960, 0))
	if got, want := a.scaleBudget(12000), 15000; got != want {
		t.Fatalf("got %d, want %d", got, want)
	}
}

func TestScaleBudget_SmallerWindowGetsLess(t *testing.T) {
	// 8192 tokens: a quarter of the reference, so a quarter of the budget.
	a := budgetAgent("small", "", limitsOf(8192, 0))
	if got, want := a.scaleBudget(24000), 6000; got != want {
		t.Fatalf("got %d, want %d", got, want)
	}
}

// Growth is bounded. A very large window must not multiply every call's cost.
func TestScaleBudget_GrowthIsCapped(t *testing.T) {
	a := budgetAgent("huge", "", limitsOf(1000000, 0))
	got := a.scaleBudget(12000)
	if want := int(12000 * maxBudgetScale); got != want {
		t.Fatalf("got %d, want the scale capped at %v → %d", got, maxBudgetScale, want)
	}
}

// And context never takes more than its share of the window, whatever the
// caller asked for.
func TestScaleBudget_NeverExceedsItsShareOfTheWindow(t *testing.T) {
	a := budgetAgent("small", "", limitsOf(8192, 0))
	got := a.scaleBudget(1000000)
	ceiling := int(float64(8192*charsPerToken) * contextShareOfWindow)
	if got != ceiling {
		t.Fatalf("got %d, want the ceiling %d", got, ceiling)
	}
}

// Two lanes, two windows: the smaller one decides, because one budget serves
// whichever stage is asking and overshooting the smaller model is the failure
// that matters.
func TestScaleBudget_SmallerLaneWins(t *testing.T) {
	limits := func(m string) (int, int) {
		switch m {
		case "big":
			return 200000, 0
		case "small":
			return 8192, 0
		}
		return 0, 0
	}
	a := budgetAgent("big", "small", limits)
	if got, want := a.scaleBudget(24000), 6000; got != want {
		t.Fatalf("got %d, want %d — the executor's window should bind", got, want)
	}
}

// A lane the catalog does not know is skipped rather than treated as zero.
func TestScaleBudget_UnknownLaneIsIgnoredNotZero(t *testing.T) {
	limits := func(m string) (int, int) {
		if m == "known" {
			return 40960, 0
		}
		return 0, 0
	}
	a := budgetAgent("known", "mystery", limits)
	if got, want := a.scaleBudget(12000), 15000; got != want {
		t.Fatalf("got %d, want %d — the unknown lane must not force the budget to zero", got, want)
	}
}

func TestScaleBudget_NilAgentAndZeroRequest(t *testing.T) {
	var a *Agent
	if got := a.scaleBudget(12000); got != 12000 {
		t.Errorf("nil agent: got %d", got)
	}
	b := budgetAgent("m", "", limitsOf(40960, 0))
	if got := b.scaleBudget(0); got != 0 {
		t.Errorf("zero request: got %d", got)
	}
}
