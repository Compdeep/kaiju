package models

import (
	"slices"
	"testing"
)

// A variant id finds its model, and its limits.
func TestFindIgnoresTheVariant(t *testing.T) {
	m, ok := Find("qwen.qwen3-32b+think")
	if !ok || m.ID != "qwen.qwen3-32b" {
		t.Fatalf("Find(+think) = %q, %v", m.ID, ok)
	}
	if ctx, _ := Limits("qwen.qwen3-32b+nothink"); ctx == 0 {
		t.Error("Limits(+nothink) found nothing")
	}
}

// The variants a picker offers follow what the model was measured to do.
func TestVariants(t *testing.T) {
	cases := map[string][]string{
		"qwen.qwen3-32b":             {"high"},
		"openai.gpt-oss-120b":        {"low", "medium", "high"},
		"nvidia.nemotron-nano-9b-v2": nil,
		"qwen.qwen3-coder-next":      nil,
	}
	for id, want := range cases {
		m, ok := Find(id)
		if !ok {
			t.Fatalf("%s not in the catalog", id)
		}
		if got := m.Variants(); !slices.Equal(got, want) {
			t.Errorf("%s variants = %v, want %v", id, got, want)
		}
	}
	// One that can be switched on but has no measured effort is offered
	// "think"; one that thinks and can stop is offered "nothink".
	if got := (Info{ReasoningOptional: true}).Variants(); !slices.Equal(got, []string{"think"}) {
		t.Errorf("off by default, switchable = %v", got)
	}
	on := true
	if got := (Info{Thinking: &on, ReasoningOptional: true}).Variants(); !slices.Equal(got, []string{"nothink"}) {
		t.Errorf("on by default, switchable = %v", got)
	}
}
