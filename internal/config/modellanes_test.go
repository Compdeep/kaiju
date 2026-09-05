package config

import (
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/models"
)

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

// The catalog is curated, not exhaustive, and an id it has never heard of is the
// ordinary case for a self-hosted endpoint. This used to be silent for that
// reason, and silence reads as approval. It says so now — as an absence of
// information rather than a judgement, because the operator's move is to test
// the model, not to change it.
func TestModelLaneWarnings_UnknownModelSaysItIsUntested(t *testing.T) {
	c := &Config{}
	c.LLM.Model = "our-own-finetune-v3"
	c.Executor.Model = ""

	warnings := c.ModelLaneWarnings()
	if len(warnings) != 1 {
		t.Fatalf("got %d lines, want 1: %v", len(warnings), warnings)
	}
	if !strings.Contains(warnings[0], "our-own-finetune-v3") {
		t.Errorf("the line does not name the model: %q", warnings[0])
	}
	if !strings.Contains(warnings[0], "may not work") {
		t.Errorf("the line does not say it may not work: %q", warnings[0])
	}
	// It must not read as a measured claim about the model. "reasons before it
	// answers" is the wording reserved for one the catalog has actually tested.
	if strings.Contains(warnings[0], "reasons before it answers") {
		t.Errorf("an untested model is described as though it had been measured: %q", warnings[0])
	}
}

// The two lines have to stay distinguishable. Something measured about a model
// and an admission that nothing is known are different things, and if both read
// the same an operator learns to skip both.
func TestModelLaneWarnings_MeasuredAndUntestedReadDifferently(t *testing.T) {
	measured := &Config{}
	measured.LLM.Model = "qwen/qwen3-32b"
	untested := &Config{}
	untested.LLM.Model = "our-own-finetune-v3"

	m, u := measured.ModelLaneWarnings(), untested.ModelLaneWarnings()
	if len(m) != 1 || len(u) != 1 {
		t.Fatalf("want one line each, got %d and %d", len(m), len(u))
	}
	if m[0] == u[0] {
		t.Fatal("a measured model and an unknown one produce the same line")
	}
	if strings.Contains(m[0], "may not work") {
		t.Errorf("a measured model is described as untested: %q", m[0])
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

// Every model this binary defaults to has to be one the catalog carries.
//
// The defaults and the catalog are edited in different files by different
// changes, and nothing connected them: a refresh that dropped gpt-4o left the
// compiled default naming a model the catalog had never heard of, so a fresh
// install printed "may not work — the catalog does not carry it" about its own
// choice. That is the shape of failure this catches.
func TestEveryDefaultModelIsInTheCatalog(t *testing.T) {
	d := Default()
	for _, c := range []struct{ lane, id string }{
		{"llm.model", d.LLM.Model},
		{"executor.model", d.Executor.Model},
		{"chat.model", d.Chat.Model},
		{"agent.route_model", d.Agent.RouteModel},
		{"agent.answer_model", d.Agent.AnswerModel},
		{"vision.model", d.Vision.Model},
	} {
		if c.id == "" {
			continue // an empty lane inherits, which is a choice and not an id
		}
		if _, ok := models.Find(c.id); !ok {
			t.Errorf("the default for %s is %q, which the catalog does not carry", c.lane, c.id)
		}
	}
}

// The lanes that force a small call must default to a model that can make one.
// A default that trips the engine's own filter is a default nobody chose.
//
// Thinking is deliberately NOT checked. Both lanes turn reasoning off before
// they send, and measured against real preflight prompts with it off, the three
// best models were all reason-capable — the previous non-thinking default was
// 25 times slower than the winner and was cut at the cap on one prompt of three.
// What matters here is that the model can emit the call, not what it would do
// if it were allowed to think.
func TestTheDefaultsSuitTheirLanes(t *testing.T) {
	d := Default()
	for _, c := range []struct{ lane, id string }{
		{"executor.model", d.Executor.Model},
		{"agent.route_model", d.Agent.RouteModel},
	} {
		if c.id == "" {
			continue
		}
		m, ok := models.Find(c.id)
		if !ok {
			continue // reported by the test above
		}
		if !m.ToolCallOK {
			t.Errorf("%s defaults to %q, which the catalog does not record as fit for a forced call", c.lane, c.id)
		}
		if !m.Tools {
			t.Errorf("%s defaults to %q, which cannot call tools at all", c.lane, c.id)
		}
	}
}

// And whatever the default is, the picker for that lane has to offer it.
// A default a picker filters out is a setting an operator cannot restore after
// changing it once.
func TestEachLanesDefaultAppearsInItsOwnPicker(t *testing.T) {
	d := Default()
	inList := func(list []models.Info, id string) bool {
		for _, m := range list {
			if m.ID == id {
				return true
			}
		}
		return false
	}
	if id := d.Executor.Model; id != "" && !inList(models.ForcedSmallCall(), id) {
		t.Errorf("executor defaults to %q, which its own picker does not offer", id)
	}
	if id := d.Agent.RouteModel; id != "" && !inList(models.RouterFit(), id) {
		t.Errorf("router defaults to %q, which its own picker does not offer", id)
	}
	if id := d.LLM.Model; id != "" && !inList(models.ToolSafe(), id) {
		t.Errorf("the reasoning lane defaults to %q, which its own picker does not offer", id)
	}
}
