package llm

import (
	"encoding/json"
	"testing"
	"time"
)

// The order of the rules is the design, so each is tested as a rule rather than
// as a number.

func forced(maxTokens int) *ChatRequest {
	return &ChatRequest{
		Messages:   []Message{{Role: "user", Content: "x"}},
		Tools:      []ToolDef{{Type: "function", Function: FunctionDef{Name: "one", Parameters: json.RawMessage(`{"type":"object"}`)}}},
		ToolChoice: "required",
		MaxTokens:  maxTokens,
	}
}

func prose() *ChatRequest {
	return &ChatRequest{Messages: []Message{{Role: "user", Content: "x"}}, MaxTokens: 4096}
}

// hybrid is the ordinary model of 2026: thinks by default, can be told not to,
// acts on nothing else.
var hybrid = ModelFacts{
	ContextTokens: 262144, MaxOutputTokens: 32768,
	Thinking: Thinking{Default: true, Optional: true},
}

// A call that forces one shape does not think unless it says so.
//
// Its reply is bounded by a schema and its thinking is not: over 257 calls on a
// trivial prompt, reasoning ran to a median of 139 tokens and a maximum of
// 16,002. No comparison against the cap survives that tail, so the rule is
// about what the call IS, not how big it is.
func TestAForcedShapeDoesNotThinkByDefault(t *testing.T) {
	for _, cap := range []int{128, 2048, 8192, 65536} {
		got := resolveReasoning(forced(cap), hybrid, true)
		if !got.Say || got.Think {
			t.Errorf("cap %d: Say=%v Think=%v, want thinking switched off", cap, got.Say, got.Think)
		}
	}
}

// And a stage that wants it reasoned about says so. The planner does.
func TestAForcedShapeThinksWhenItAsks(t *testing.T) {
	req := forced(8192)
	req.Think = &Reasoning{Want: WantOn}
	if got := resolveReasoning(req, hybrid, true); !got.Say || !got.Think {
		t.Errorf("Say=%v Think=%v, want thinking on — the caller asked", got.Say, got.Think)
	}
}

// Prose says nothing and takes the model's own default. Saying "off" here would
// let a call that never considered the question change how the answer is
// written.
func TestProseSaysNothing(t *testing.T) {
	if got := resolveReasoning(prose(), hybrid, true); got.Say {
		t.Errorf("Say=%v, want nothing said about thinking", got.Say)
	}
}

// Telling a model that cannot stop to stop is an instruction that reads as
// success and changes nothing.
func TestOffIsNotSentToAModelThatCannotStop(t *testing.T) {
	locked := hybrid
	locked.Thinking.Optional = false

	req := prose()
	req.Think = &Reasoning{Want: WantOff}
	if got := resolveReasoning(req, locked, true); got.Say {
		t.Error("a locked model was told to stop reasoning; nothing should be sent")
	}
	// The same model, asked to think, is told so — that instruction can be met.
	req.Think = &Reasoning{Want: WantOn}
	if got := resolveReasoning(req, locked, true); !got.Say || !got.Think {
		t.Error("a locked model was not told to think, which it can honour")
	}
}

// An effort the model was never measured to act on is a control that appears to
// work. It is not sent.
func TestAnEffortIsSentOnlyWhereItWasMeasured(t *testing.T) {
	acts := hybrid
	acts.Thinking.Efforts = []Effort{EffortLow, EffortHigh, EffortMax}

	req := prose()
	req.Think = &Reasoning{Want: WantOn, Effort: EffortMedium}
	if got := resolveReasoning(req, acts, true); got.Effort != EffortUnset {
		t.Errorf("effort %v was sent; the catalog says this model acts on low, high and max", got.Effort)
	}
	req.Think = &Reasoning{Want: WantOn, Effort: EffortHigh}
	if got := resolveReasoning(req, acts, true); got.Effort != EffortHigh {
		t.Errorf("effort = %v, want high — the catalog says it acts on it", got.Effort)
	}
}

// Same for a token budget.
func TestABudgetIsSentOnlyWhereItIsHonoured(t *testing.T) {
	req := prose()
	req.Think = &Reasoning{Want: WantOn, Budget: 2000}
	if got := resolveReasoning(req, hybrid, true); got.Budget != 0 {
		t.Errorf("budget %d was sent to a model that does not act on one", got.Budget)
	}

	honours := hybrid
	honours.Thinking.Budget = true
	if got := resolveReasoning(req, honours, true); got.Budget != 2000 {
		t.Errorf("budget = %d, want 2000", got.Budget)
	}
}

// Nothing is asked of a model nobody can answer for. This is the ordinary case
// for a self-hosted endpoint, and the contract every embedding application that
// supplies no catalog relies on.
func TestAnUnknownModelIsAskedNothing(t *testing.T) {
	for _, req := range []*ChatRequest{prose(), forced(128)} {
		req.Think = &Reasoning{Want: WantOff, Effort: EffortHigh, Budget: 2000}
		got := resolveReasoning(req, ModelFacts{}, false)
		if got.Say || got.Effort != EffortUnset || got.Budget != 0 || got.Wait != 1 {
			t.Errorf("an unknown model was sent %+v", got)
		}
	}
}

// An effort and a budget bound thinking that is going to happen. A call that
// will not think is asked for neither.
func TestACallThatWillNotThinkIsAskedForNeither(t *testing.T) {
	honours := hybrid
	honours.Thinking.Budget = true
	honours.Thinking.Efforts = []Effort{EffortHigh}

	req := forced(8192) // defaults to off
	req.Think = &Reasoning{Effort: EffortHigh, Budget: 2000}
	got := resolveReasoning(req, honours, true)
	if got.Effort != EffortUnset || got.Budget != 0 {
		t.Errorf("a call with thinking off was sent effort=%v budget=%d", got.Effort, got.Budget)
	}
}

// The wire form omits what was not asked for, and omits itself entirely when
// nothing was.
func TestTheWireFormSaysOnlyWhatWasResolved(t *testing.T) {
	if w := (resolved{Wait: 1}).wire(); w != nil {
		t.Errorf("nothing resolved produced %+v, want no reasoning key at all", w)
	}
	w := resolved{Say: true, Think: false}.wire()
	if w == nil || w.Enabled == nil || *w.Enabled || w.Effort != "" || w.MaxTokens != 0 {
		t.Errorf("off resolved to %+v", w)
	}
	w = resolved{Effort: EffortHigh, Budget: 2000}.wire()
	if w == nil || w.Enabled != nil {
		t.Errorf("a call that only BOUNDS thinking said whether to think: %+v", w)
	}
	if w.Effort != "high" || w.MaxTokens != 2000 {
		t.Errorf("bounds resolved to %+v", w)
	}
}

// A model measured slow is given longer; every other model is unchanged. A pace
// lengthens a deadline and never shortens one.
func TestPaceLengthensTheDeadline(t *testing.T) {
	ordinary := deadline(resolved{Say: true, Think: true, Wait: 1}, hybrid, true)
	if ordinary != thinkingRequestTimeout {
		t.Errorf("a thinking call got %s, want %s", ordinary, thinkingRequestTimeout)
	}
	slow := hybrid
	slow.Thinking.Pace = 2
	got := deadline(resolved{Say: true, Think: true, Wait: paceOf(slow)}, slow, true)
	if got != 2*thinkingRequestTimeout {
		t.Errorf("a slow model got %s, want twice %s", got, thinkingRequestTimeout)
	}
	fast := hybrid
	fast.Thinking.Pace = 0.25
	if got := paceOf(fast); got != 1 {
		t.Errorf("a fast model shortened its deadline by %v; a measurement only ever lengthens", got)
	}
	// And a call that will not think keeps the ordinary allowance.
	if got := deadline(resolved{Say: true, Think: false, Wait: 1}, hybrid, true); got != requestTimeout {
		t.Errorf("a call with thinking off got %s, want %s", got, requestTimeout)
	}
}

// The connection ceiling has to sit at or above the longest deadline this
// package can produce, or a wait it deliberately allowed is cut by the
// transport and the error names the wrong thing.
func TestTheConnectionCeilingFitsTheLongestDeadline(t *testing.T) {
	slowest := ModelFacts{Thinking: Thinking{Default: true, Pace: maxWait}}
	longest := deadline(resolved{Say: true, Think: true, Wait: paceOf(slowest)}, slowest, true)
	ceiling := time.Duration(maxWait) * thinkingRequestTimeout
	if longest > ceiling {
		t.Errorf("the longest deadline is %s and the ceiling is %s", longest, ceiling)
	}
}
