package agent

import (
	"context"
	"slices"
	"testing"

	"github.com/Compdeep/kaiju/agent/llm"
)

// An effort is asked for only where the model acts on it.
//
// Every provider accepts the parameter and none errors on it, so a setting sent
// blindly appears to work and does not: on one prompt qwen3.6-35b-a3b reasoned
// 1,498 tokens by default and 1,548 at "low". A control that changes nothing is
// worse than one that is not offered, because somebody will trust it.

func reasoningAgent(effort string, budget int, catalog ModelReasoning) *Agent {
	return &Agent{cfg: Config{
		ModelConfig: ModelConfig{
			LLMReasoningEffort: effort,
			LLMReasoningBudget: budget,
			Reasoning:          catalog,
		},
	}}
}

func TestApplyReasoningBudget_SendsAnEffortTheModelActsOn(t *testing.T) {
	a := reasoningAgent("low", 0, func(string) ([]string, bool) {
		return []string{"low", "medium", "high"}, false
	})
	req := &llm.ChatRequest{}
	a.applyReasoningBudget(context.Background(), req, "deepseek/deepseek-v4-pro-0813")

	if req.Reasoning == nil || req.Reasoning.Effort != "low" {
		t.Fatalf("the effort was not sent: %+v", req.Reasoning)
	}
	if req.Reasoning.Enabled != nil {
		t.Error("asking for an effort said something about enabled, which it must not")
	}
}

func TestApplyReasoningBudget_WithholdsAnEffortTheModelIgnores(t *testing.T) {
	a := reasoningAgent("low", 0, func(string) ([]string, bool) { return nil, false })
	req := &llm.ChatRequest{}
	a.applyReasoningBudget(context.Background(), req, "qwen/qwen3.6-35b-a3b")

	if req.Reasoning != nil {
		t.Errorf("an effort was sent to a model that ignores it: %+v", req.Reasoning)
	}
}

// An effort outside what the model acts on is not sent either — a deployment
// set to "high" against a model measured only at "low" gets nothing.
func TestApplyReasoningBudget_WithholdsAnEffortOutsideTheMeasuredSet(t *testing.T) {
	a := reasoningAgent("high", 0, func(string) ([]string, bool) { return []string{"low"}, false })
	req := &llm.ChatRequest{}
	a.applyReasoningBudget(context.Background(), req, "m")
	if req.Reasoning != nil {
		t.Errorf("an unmeasured effort was sent: %+v", req.Reasoning)
	}
}

// A budget goes only where a budget is honoured as one.
func TestApplyReasoningBudget_BudgetOnlyWhereItIsHonoured(t *testing.T) {
	honoured := reasoningAgent("", 2048, func(string) ([]string, bool) { return nil, true })
	req := &llm.ChatRequest{}
	honoured.applyReasoningBudget(context.Background(), req, "anthropic/claude-sonnet-5")
	if req.Reasoning == nil || req.Reasoning.MaxTokens != 2048 {
		t.Fatalf("the budget was not sent: %+v", req.Reasoning)
	}

	ignored := reasoningAgent("", 2048, func(string) ([]string, bool) { return nil, false })
	req2 := &llm.ChatRequest{}
	ignored.applyReasoningBudget(context.Background(), req2, "qwen/qwen3.6-35b-a3b")
	if req2.Reasoning != nil {
		t.Errorf("a budget was sent to a model that overruns it: %+v", req2.Reasoning)
	}
}

// Nothing configured, nothing sent — a deployment that says nothing is
// unchanged.
func TestApplyReasoningBudget_SaysNothingWhenUnset(t *testing.T) {
	a := reasoningAgent("", 0, func(string) ([]string, bool) { return []string{"low"}, true })
	req := &llm.ChatRequest{}
	a.applyReasoningBudget(context.Background(), req, "m")
	if req.Reasoning != nil {
		t.Errorf("an unset deployment sent something: %+v", req.Reasoning)
	}
}

// No catalog means no measurement, so nothing is asked.
func TestApplyReasoningBudget_NoCatalogAsksNothing(t *testing.T) {
	a := reasoningAgent("low", 2048, nil)
	req := &llm.ChatRequest{}
	a.applyReasoningBudget(context.Background(), req, "m")
	if req.Reasoning != nil {
		t.Errorf("an unmeasured model was sent a setting: %+v", req.Reasoning)
	}
}

// It never turns thinking on. A request that turned it off keeps it off, and
// one that said nothing keeps saying nothing about that.
func TestApplyReasoningBudget_NeverTurnsThinkingOn(t *testing.T) {
	a := reasoningAgent("low", 0, func(string) ([]string, bool) { return []string{"low"}, false })
	req := llm.WithoutReasoning(&llm.ChatRequest{})
	a.applyReasoningBudget(context.Background(), req, "m")
	if req.Reasoning.On() {
		t.Error("a request with thinking off had it turned on")
	}
}

// A setting changed at run time reaches the next call, not the next restart.
//
// SetReasoning's own note records the fault this guards: that one shipped
// storing to the config file only, so the switch persisted and the running lane
// kept its previous answer. The file then disagreed with the behaviour, and the
// file is what an operator reads.
func TestSetReasoningEffort_ReachesTheNextRequest(t *testing.T) {
	a := reasoningAgent("", 0, func(string) ([]string, bool) {
		return []string{"low", "medium", "high"}, true
	})

	req := &llm.ChatRequest{}
	a.applyReasoningBudget(context.Background(), req, "m")
	if req.Reasoning != nil {
		t.Fatalf("something was asked before anything was set: %+v", req.Reasoning)
	}

	if !a.SetReasoningEffort("medium") {
		t.Fatal("medium was refused")
	}
	if !a.SetReasoningBudget(2048) {
		t.Fatal("a budget of 2048 was refused")
	}

	req2 := &llm.ChatRequest{}
	a.applyReasoningBudget(context.Background(), req2, "m")
	if req2.Reasoning == nil {
		t.Fatal("the change did not reach the next request")
	}
	if req2.Reasoning.Effort != "medium" {
		t.Errorf("effort = %q, want medium", req2.Reasoning.Effort)
	}
	if req2.Reasoning.MaxTokens != 2048 {
		t.Errorf("budget = %d, want 2048", req2.Reasoning.MaxTokens)
	}
}

// Clearing it stops asking, rather than being read as an absent field that
// leaves the previous value in place.
func TestSetReasoningEffort_EmptyStopsAsking(t *testing.T) {
	a := reasoningAgent("high", 0, func(string) ([]string, bool) {
		return []string{"low", "medium", "high"}, false
	})
	if !a.SetReasoningEffort("") {
		t.Fatal("empty was refused, but it is how the picker stops asking")
	}
	req := &llm.ChatRequest{}
	a.applyReasoningBudget(context.Background(), req, "m")
	if req.Reasoning != nil {
		t.Errorf("an effort was still asked for after being cleared: %+v", req.Reasoning)
	}
}

// A value that is not an effort is refused rather than corrected, and a
// negative budget is refused rather than clamped — it means the caller
// computed it, and a computed negative is a bug the caller should hear about.
func TestSetReasoning_RefusesWhatItCannotMean(t *testing.T) {
	a := reasoningAgent("", 0, func(string) ([]string, bool) { return nil, false })
	if a.SetReasoningEffort("maximum") {
		t.Error(`"maximum" was accepted`)
	}
	if a.SetReasoningBudget(-1) {
		t.Error("a negative budget was accepted")
	}
	if !a.SetReasoningBudget(0) {
		t.Error("zero was refused, but it is how a budget is cleared")
	}
}

// The vocabulary is the providers', and no model takes all of it.
//
// It was low/medium/high, which is what a scale usually is and what the OpenAI
// wire documents. Measuring the catalog found glm-5.2 takes "xhigh" and "high"
// and neither "low" nor "medium", and gemini-3.5-flash-lite takes "minimal" —
// so a three-value enum could not ask either model for anything it accepts.
func TestReasoningEfforts_CarryTheWholeVocabulary(t *testing.T) {
	for _, e := range []string{"minimal", "low", "medium", "high", "xhigh", "max"} {
		if _, ok := ParseReasoningEffort(e); !ok {
			t.Errorf("%q is refused, but it is a value some model in the catalog takes", e)
		}
	}
	if _, ok := ParseReasoningEffort("none"); ok {
		t.Error(`"none" was accepted; not thinking at all is SetReasoning's question, not this one`)
	}
	if _, ok := ParseReasoningEffort("hard"); ok {
		t.Error(`"hard" was accepted, so the vocabulary is not closed`)
	}
	if got, want := ReasoningEfforts(), []string{"minimal", "low", "medium", "high", "xhigh", "max"}; !slices.Equal(got, want) {
		t.Errorf("efforts = %v, want %v weakest first so a picker reads as a scale", got, want)
	}
	// A copy, so a caller building a picker cannot reorder the engine's own list.
	ReasoningEfforts()[0] = "clobbered"
	if ReasoningEfforts()[0] != "minimal" {
		t.Error("the caller's slice shares the engine's array")
	}
}

// A run's own effort beats the node's setting, and does not outlive the run.
//
// This is what the per-request field is for. makeen picks a model per chat for
// one organisation among many on a shared node, so the effort has to be one
// run's choice; a PATCH to the node's config would change it for every other
// organisation on the same daemon.
func TestReasoningEffort_TheRunsChoiceWinsAndDoesNotPersist(t *testing.T) {
	a := reasoningAgent("low", 0, func(string) ([]string, bool) {
		return []string{"low", "medium", "high"}, true
	})

	ctx := withLaneSelection(context.Background(), laneSelectionFromTrigger(Trigger{
		ReasoningEffort:    "high",
		ReasoningMaxTokens: 4096,
	}))
	req := &llm.ChatRequest{}
	a.applyReasoningBudget(ctx, req, "m")
	if req.Reasoning == nil || req.Reasoning.Effort != "high" {
		t.Fatalf("the run's own effort did not win: %+v", req.Reasoning)
	}
	if req.Reasoning.MaxTokens != 4096 {
		t.Errorf("budget = %d, want the run's 4096", req.Reasoning.MaxTokens)
	}

	// The next run carries no selection and must see the node's setting again.
	plain := &llm.ChatRequest{}
	a.applyReasoningBudget(context.Background(), plain, "m")
	if plain.Reasoning == nil || plain.Reasoning.Effort != "low" {
		t.Errorf("the node's setting did not come back: %+v", plain.Reasoning)
	}
	if plain.Reasoning.MaxTokens != 0 {
		t.Errorf("a budget outlived the run that asked for it: %d", plain.Reasoning.MaxTokens)
	}
}

// A run that says nothing leaves the node's setting alone, rather than reading
// as a request for nothing.
//
// Empty and zero are what every existing caller sends, since neither field was
// there until now. Treating them as "ask for nothing" would silently disable a
// configured effort for every client that has not been updated.
func TestReasoningEffort_SayingNothingKeepsTheNodesSetting(t *testing.T) {
	a := reasoningAgent("medium", 2048, func(string) ([]string, bool) {
		return []string{"low", "medium", "high"}, true
	})
	ctx := withLaneSelection(context.Background(), laneSelectionFromTrigger(Trigger{
		Provider: "p", Model: "m", // a selection, but nothing about reasoning
	}))
	req := &llm.ChatRequest{}
	a.applyReasoningBudget(ctx, req, "m")
	if req.Reasoning == nil || req.Reasoning.Effort != "medium" || req.Reasoning.MaxTokens != 2048 {
		t.Errorf("a run that said nothing changed the setting: %+v", req.Reasoning)
	}
}

// The catalog still decides, whoever asked. A per-request effort a model does
// not act on is dropped exactly as a configured one is — otherwise the request
// field would be the way round the measurement.
func TestReasoningEffort_TheCatalogStillNarrowsARunsChoice(t *testing.T) {
	a := reasoningAgent("", 0, func(string) ([]string, bool) {
		return []string{"high", "xhigh"}, false // as z-ai/glm-5.2 measured
	})
	ctx := withLaneSelection(context.Background(), laneSelectionFromTrigger(Trigger{
		ReasoningEffort:    "low",
		ReasoningMaxTokens: 512,
	}))
	req := &llm.ChatRequest{}
	a.applyReasoningBudget(ctx, req, "z-ai/glm-5.2")
	if req.Reasoning != nil {
		t.Errorf("a value the model does not act on was sent anyway: %+v", req.Reasoning)
	}
}
