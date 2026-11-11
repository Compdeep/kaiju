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

// Llama and Nova are reached through Bedrock's Converse; the open-weight
// Bedrock models through Chat Completions.
func TestBedrockAPIs(t *testing.T) {
	for id, want := range map[string]string{
		"us.meta.llama4-maverick-17b-instruct-v1:0": APIConverse,
		"us.amazon.nova-2-lite-v1:0":                APIConverse,
		"qwen.qwen3-32b":                            "",
	} {
		if m, ok := Find(id); !ok || m.API != want {
			t.Errorf("%s: found %v, api %q, want %q", id, ok, m.API, want)
		}
	}
	nova, _ := Find("us.amazon.nova-2-lite-v1:0+high")
	if got := nova.Variants(); !slices.Equal(got, []string{"low", "medium", "high"}) {
		t.Errorf("Nova 2 Lite variants = %v", got)
	}
}

// Each model is followed by its variants, each named and marked for what it
// does, with no thinking switch of its own: the variant is the setting.
func TestWithVariants(t *testing.T) {
	qwen, _ := Find("qwen.qwen3-32b")
	got := WithVariants([]Info{qwen})
	if len(got) != 2 || got[0].ID != "qwen.qwen3-32b" || got[1].ID != "qwen.qwen3-32b+high" {
		t.Fatalf("got %v", got)
	}
	v := got[1]
	if !v.Thinks() || v.ReasoningOptional || v.Name != "Qwen3 32B (thinking, high)" || v.Provider != "bedrock" {
		t.Errorf("variant = %+v", v)
	}
	oss, _ := Find("openai/gpt-oss-120b")
	if n := len(WithVariants([]Info{oss})); n != 4 {
		t.Errorf("gpt-oss on OpenRouter: %d entries, want itself and three efforts", n)
	}
}
