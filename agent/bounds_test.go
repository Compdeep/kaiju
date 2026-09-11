package agent

import (
	"context"
	"testing"
	"time"
)

// The precedence, asserted as a chain rather than a set of unrelated numbers.
//
// Every step of it was a separate piece of code before this, and three of them
// disagreed on which model they were sizing against. The property worth holding
// is the ORDER: the catalog proposes, the stage shapes, the model's own ceiling
// and the operator's narrow, and the caller's own ask is never raised.

// The real catalog numbers for the model this deployment answers with, so the
// worked example in the file header is the thing under test rather than a
// paraphrase of it.
const (
	glmWindow    = 1310720
	glmMaxOutput = 262144
)

func boundsAgent(maxTokens int, limits ModelLimits, reasoning ModelReasoning) *Agent {
	return &Agent{cfg: Config{ModelConfig: ModelConfig{
		MaxTokens: maxTokens,
		Limits:    limits,
		Reasoning: reasoning,
	}}}
}

func glmLimits(string) (int, int) { return glmWindow, glmMaxOutput }

func TestReplyBound_ThePrecedenceChain(t *testing.T) {
	for _, c := range []struct {
		name      string
		maxTokens int
		limits    ModelLimits
		ask       budgetAsk
		want      int
		why       string
	}{
		{
			name:      "the catalog proposes and the stage's ceiling shapes",
			maxTokens: 16384,
			limits:    glmLimits,
			ask:       budgetAsk{Lane: Answer, Stage: replyDecisionBudget, Model: "z-ai/glm-5.3"},
			want:      8192,
			why: "1310720/64 is 20480, which the decision ceiling cuts to 8192. Sized " +
				"against the model that ANSWERS: the old resolver took the smallest " +
				"configured window and produced 4096 here, which is the cap a live " +
				"chat turn spent entirely on reasoning.",
		},
		{
			name:      "the operator's ceiling narrows it",
			maxTokens: 4096,
			limits:    glmLimits,
			ask:       budgetAsk{Lane: Answer, Stage: replyDecisionBudget, Model: "z-ai/glm-5.3"},
			want:      4096,
			why:       "max_tokens is a ceiling on every stage now, not a literal at four call sites",
		},
		{
			name:      "an unset operator ceiling narrows nothing",
			maxTokens: 0,
			limits:    glmLimits,
			ask:       budgetAsk{Lane: Answer, Stage: replyDecisionBudget, Model: "z-ai/glm-5.3"},
			want:      8192,
			why:       "zero is 'not set', and clamping to it would send a reply cap of nothing",
		},
		{
			name:      "no catalog leaves the stage's own floor",
			maxTokens: 0,
			limits:    nil,
			ask:       budgetAsk{Lane: Answer, Stage: replyDecisionBudget, Model: "z-ai/glm-5.3"},
			want:      replyDecisionBudget.Base,
			why: "an application supplying no catalog must be unchanged by all of this — " +
				"a host may supply Limits for one model and no other lookup at all",
		},
		{
			name:      "an unknown model leaves the stage's own floor",
			maxTokens: 16384,
			limits:    func(string) (int, int) { return 0, 0 },
			ask:       budgetAsk{Lane: Answer, Stage: replyDecisionBudget, Model: "something-self-hosted"},
			want:      replyDecisionBudget.Base,
			why:       "a curated catalog does not carry every endpoint an operator points at",
		},
		{
			name:      "the model's published ceiling wins over the stage's",
			maxTokens: 0,
			limits:    func(string) (int, int) { return glmWindow, 1000 },
			ask:       budgetAsk{Lane: Answer, Stage: replyCodeBudget, Model: "small-output"},
			want:      1000,
			why:       "some providers reject a max_tokens above their own maximum rather than trimming it",
		},
		{
			name:      "a stage's stated minimum raises the table",
			maxTokens: 0,
			limits:    nil,
			ask: budgetAsk{Lane: Heavy, Stage: replyAnalysisBudget, Model: "m",
				MinReply: replyAnalysisBudget.Base + 904},
			want: replyAnalysisBudget.Base + 904,
			why:  "the planner is told it may write MaxNodes steps, so its cap has to fit that many",
		},
		{
			name:      "the caller's own ask is never raised",
			maxTokens: 16384,
			limits:    glmLimits,
			ask:       budgetAsk{Lane: Route, Model: "z-ai/glm-5.3", AskedFor: 128},
			want:      128,
			why: "the router's 128 tokens are a decision about the SHAPE of the reply. " +
				"Raising it to the 256 floor would overrule a caller that knows better.",
		},
		{
			name:      "a caller asking for more than it may have is still narrowed",
			maxTokens: 16384,
			limits:    glmLimits,
			ask:       budgetAsk{Lane: Answer, Stage: replyDecisionBudget, Model: "z-ai/glm-5.3", AskedFor: 999999},
			want:      8192,
			why:       "a ceiling only ever lowers; a caller cannot ask past the table",
		},
		{
			name:      "no stage named still bounds the call",
			maxTokens: 16384,
			limits:    glmLimits,
			ask:       budgetAsk{Lane: Answer, Model: "z-ai/glm-5.3"},
			want:      8192,
			why:       "forgetting to name a stage must be expensive, never wrong — defaultStage",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			a := boundsAgent(c.maxTokens, c.limits, nil)
			if got := a.replyBound(c.ask); got != c.want {
				t.Errorf("replyBound = %d, want %d\n%s", got, c.want, c.why)
			}
		})
	}
}

// The division exists so that a model which thinks hard cannot spend the whole
// completion doing it and leave the answer unstarted. It is asked for only where
// the catalog says the model reads it.
func TestThinkingBound(t *testing.T) {
	honours := func(string) ([]string, bool) { return []string{"low", "high", "max"}, true }
	ignores := func(string) ([]string, bool) { return []string{"low", "high", "max"}, false }

	t.Run("divided out of this call's own allowance", func(t *testing.T) {
		a := boundsAgent(0, nil, honours)
		got, _ := a.thinkingBound(context.Background(), "m", 8192)
		if got != 6144 {
			t.Errorf("thinking = %d, want 6144 — 6144 of 8192, leaving the answer 2048 it cannot be robbed of", got)
		}
	})

	t.Run("scales with the reply rather than being a number", func(t *testing.T) {
		a := boundsAgent(0, nil, honours)
		got, _ := a.thinkingBound(context.Background(), "m", 4096)
		if got != 3072 {
			t.Errorf("thinking = %d, want 3072 — the proportion is fixed, the number is not. "+
				"Three catalog entries carried 6144 as an absolute and asked a 4096-token "+
				"call for half again as much thinking as it was allowed in total.", got)
		}
	})

	t.Run("nothing is asked of a model that ignores a budget", func(t *testing.T) {
		a := boundsAgent(0, nil, ignores)
		if got, _ := a.thinkingBound(context.Background(), "m", 8192); got != 0 {
			t.Errorf("thinking = %d, want 0 — a control that changes nothing is worse than one not offered", got)
		}
	})

	t.Run("no catalog asks nothing", func(t *testing.T) {
		a := boundsAgent(0, nil, nil)
		got, effort := a.thinkingBound(context.Background(), "m", 8192)
		if got != 0 || effort != "" {
			t.Errorf("thinking = %d/%q, want 0/\"\" — inert without a catalog", got, effort)
		}
	})

	t.Run("an empty model asks nothing", func(t *testing.T) {
		a := boundsAgent(0, nil, honours)
		if got, _ := a.thinkingBound(context.Background(), "", 8192); got != 0 {
			t.Errorf("thinking = %d, want 0 — nothing is known about a model nobody named", got)
		}
	})

	t.Run("an operator's budget that does not fit the reply is narrowed", func(t *testing.T) {
		a := boundsAgent(0, nil, honours)
		a.cfg.LLMReasoningBudget = 8000
		got, _ := a.thinkingBound(context.Background(), "m", 8192)
		if got != 6144 {
			t.Errorf("thinking = %d, want 6144 — 8000 of 8192 leaves the answer 192 tokens, "+
				"which is the fault this whole division exists to prevent", got)
		}
	})

	t.Run("an operator's budget that does fit is left alone", func(t *testing.T) {
		a := boundsAgent(0, nil, honours)
		a.cfg.LLMReasoningBudget = 2000
		if got, _ := a.thinkingBound(context.Background(), "m", 8192); got != 2000 {
			t.Errorf("thinking = %d, want 2000 — the operator said so and it fits", got)
		}
	})

	t.Run("an effort the model was not measured on is withheld", func(t *testing.T) {
		a := boundsAgent(0, nil, honours)
		a.cfg.LLMReasoningEffort = "medium"
		if _, effort := a.thinkingBound(context.Background(), "m", 8192); effort != "" {
			t.Errorf("effort = %q, want \"\" — glm-5.3 takes low, high and max, and not medium", effort)
		}
	})

	t.Run("an effort the model acts on travels", func(t *testing.T) {
		a := boundsAgent(0, nil, honours)
		a.cfg.LLMReasoningEffort = "high"
		if _, effort := a.thinkingBound(context.Background(), "m", 8192); effort != "high" {
			t.Errorf("effort = %q, want \"high\"", effort)
		}
	})
}

// The deadline reads the run's effort off the CONTEXT.
//
// That is the whole reason the guards could move into the door. roundBudget took
// a Trigger, the Trigger lived on the Graph, and heavyRound carried a *Graph for
// no other purpose — so every lane that could not produce one wrote its own
// recovery instead of using the shared one. The effort was on the context all
// along.
func TestDeadlineFor_ReadsTheRunsEffortOffTheContext(t *testing.T) {
	a := &Agent{}
	ctx := withLaneSelection(context.Background(), laneSelection{effort: EffortHigh})
	if got := a.deadlineFor(ctx, "m"); got != effortBudget[EffortHigh] {
		t.Errorf("deadline = %s, want %s — the run named its own effort", got, effortBudget[EffortHigh])
	}
}

func TestDeadlineFor_FallsBackToTheNodesSetting(t *testing.T) {
	a := &Agent{cfg: Config{ModelConfig: ModelConfig{LLMReasoningEffort: EffortXHigh}}}
	if got := a.deadlineFor(context.Background(), "m"); got != effortBudget[EffortXHigh] {
		t.Errorf("deadline = %s, want %s — no run choice leaves the node's setting in force", got, effortBudget[EffortXHigh])
	}
}

func TestDeadlineFor_NeverBelowTheFloorExceptWhenChosen(t *testing.T) {
	a := &Agent{}
	for _, effort := range []string{EffortMinimal, EffortLow, EffortDefault, EffortMedium, ""} {
		ctx := withLaneSelection(context.Background(), laneSelection{effort: effort})
		if got := a.deadlineFor(ctx, "m"); got < minRoundBudget {
			t.Errorf("effort %q gives %s, below the %s floor", effort, got, minRoundBudget)
		}
	}
	// Fast is the one value that goes below, because it is the one that asks to.
	ctx := withLaneSelection(context.Background(), laneSelection{effort: EffortFast})
	if got := a.deadlineFor(ctx, "m"); got >= minRoundBudget {
		t.Errorf("fast gives %s; it is a deliberate choice of a shorter deadline", got)
	}
}

func TestDeadlineFor_ASlowModelIsGivenLonger(t *testing.T) {
	a := &Agent{cfg: Config{ModelConfig: ModelConfig{
		Pace: func(model string) float64 {
			if model == "slow-one" {
				return 2
			}
			return 1
		},
	}}}
	ordinary := a.deadlineFor(context.Background(), "ordinary")
	slow := a.deadlineFor(context.Background(), "slow-one")
	if slow != ordinary*2 {
		t.Errorf("slow model gets %s against %s; a measured pace lengthens the deadline", slow, ordinary)
	}
	if ordinary != minRoundBudget {
		t.Errorf("ordinary model gets %s, want the %s floor — a pace never takes time away", ordinary, minRoundBudget)
	}
}

// The three numbers come back together, because they are one decision about one
// call and were three decisions in three files.
func TestBoundsFor_AnswersAllThree(t *testing.T) {
	a := boundsAgent(16384, glmLimits, func(string) ([]string, bool) { return []string{"high"}, true })
	a.cfg.LLMReasoningEffort = "high"

	b := a.boundsFor(context.Background(), budgetAsk{
		Lane: Answer, Stage: replyDecisionBudget, Model: "z-ai/glm-5.3",
	})
	if b.Reply != 8192 {
		t.Errorf("Reply = %d, want 8192", b.Reply)
	}
	if b.Thinking != 6144 {
		t.Errorf("Thinking = %d, want 6144", b.Thinking)
	}
	if b.Effort != "high" {
		t.Errorf("Effort = %q, want \"high\"", b.Effort)
	}
	if b.Deadline < 120*time.Second {
		t.Errorf("Deadline = %s, want at least the two-minute floor", b.Deadline)
	}
}
