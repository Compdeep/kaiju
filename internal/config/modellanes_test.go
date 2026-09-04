package config

import "strings"

import "testing"

// A thinking model on a lane that forces a tool call is the case this exists
// for: qwen3-32b drove both lanes of a live install for four hours and 36% of
// its calls failed, with nothing at startup naming the model.
func TestModelLaneWarnings_ThinkingOnForcedLane(t *testing.T) {
	c := &Config{}
	c.LLM.Model = "qwen/qwen3-32b"

	warnings := c.ModelLaneWarnings()
	if len(warnings) != 1 {
		t.Fatalf("got %d warnings, want 1: %v", len(warnings), warnings)
	}
	if !strings.Contains(warnings[0], "qwen/qwen3-32b") {
		t.Errorf("warning does not name the model: %q", warnings[0])
	}
	if !strings.Contains(warnings[0], "llm.model") {
		t.Errorf("warning does not name the lane: %q", warnings[0])
	}
}

// The answer lane is absent by design, so a thinking model pinned there is not
// a warning — it is the recommended choice.
func TestModelLaneWarnings_AnswerLaneExempt(t *testing.T) {
	c := &Config{}
	c.LLM.Model = "qwen/qwen3-30b-a3b-instruct-2507"
	c.Agent.AnswerModel = "qwen/qwen3-32b"

	if warnings := c.ModelLaneWarnings(); len(warnings) != 0 {
		t.Fatalf("a thinking answer model warned: %v", warnings)
	}
}

// One model on two lanes is one problem, and two lines describing it would send
// an operator looking for a second.
func TestModelLaneWarnings_SameModelBothLanesWarnsOnce(t *testing.T) {
	c := &Config{}
	c.LLM.Model = "qwen/qwen3-32b"
	c.Executor.Model = "qwen/qwen3-32b"

	if warnings := c.ModelLaneWarnings(); len(warnings) != 1 {
		t.Fatalf("got %d warnings, want 1: %v", len(warnings), warnings)
	}
}

// The catalog is curated, not exhaustive. A self-hosted model it has never heard
// of is the ordinary case, and warning about it would train an operator to
// ignore the line that matters.
func TestModelLaneWarnings_UnknownModelIsSilent(t *testing.T) {
	c := &Config{}
	c.LLM.Model = "our-own-finetune-v3"
	c.Executor.Model = ""

	if warnings := c.ModelLaneWarnings(); len(warnings) != 0 {
		t.Fatalf("an unknown model warned: %v", warnings)
	}
}

// A catalog model fit for every forced lane is the quiet case.
func TestModelLaneWarnings_ToolSafeModelIsSilent(t *testing.T) {
	c := &Config{}
	c.LLM.Model = "qwen/qwen3-30b-a3b-instruct-2507"
	c.Executor.Model = "openai/gpt-4.1-mini"

	if warnings := c.ModelLaneWarnings(); len(warnings) != 0 {
		t.Fatalf("a tool-safe pair warned: %v", warnings)
	}
}
