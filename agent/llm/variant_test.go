package llm

import (
	"context"
	"testing"
)

func TestSplitVariant(t *testing.T) {
	cases := []struct {
		in, base string
		want     *Reasoning
	}{
		{"qwen.qwen3-32b", "qwen.qwen3-32b", nil},
		{"qwen.qwen3-32b+think", "qwen.qwen3-32b", &Reasoning{Want: WantOn}},
		{"qwen.qwen3-32b+nothink", "qwen.qwen3-32b", &Reasoning{Want: WantOff}},
		{"openai.gpt-oss-120b+low", "openai.gpt-oss-120b", &Reasoning{Want: WantOn, Effort: EffortLow}},
		// A suffix that is not a variant word is part of the id.
		{"vendor/model+v2", "vendor/model+v2", nil},
	}
	for _, tc := range cases {
		base, got := SplitVariant(tc.in)
		if base != tc.base {
			t.Errorf("%s: base = %q, want %q", tc.in, base, tc.base)
		}
		if (got == nil) != (tc.want == nil) || got != nil && *got != *tc.want {
			t.Errorf("%s: reasoning = %+v, want %+v", tc.in, got, tc.want)
		}
		if b := BaseModel(tc.in); b != tc.base {
			t.Errorf("%s: BaseModel = %q", tc.in, b)
		}
	}
}

// A variant id is sent as the base id, its thinking becomes the request's, and
// the catalog is asked about the base id.
func TestVariantBecomesTheRequestsThinking(t *testing.T) {
	var asked []string
	facts := func(m string) (ModelFacts, bool) {
		asked = append(asked, m)
		return qwenOnBedrock(m)
	}
	body := sendBedrock(t, "qwen.qwen3-32b+think", facts, nil)
	if body["model"] != "qwen.qwen3-32b" {
		t.Errorf("model sent = %v, want the base id", body["model"])
	}
	if body["reasoning_effort"] != "high" {
		t.Errorf("reasoning_effort = %v, want high", body["reasoning_effort"])
	}
	for _, m := range asked {
		if m != "qwen.qwen3-32b" {
			t.Errorf("catalog asked about %q", m)
		}
	}
}

// What the caller asked for wins over the variant: a lane that forces
// thinking off for a small call keeps it off.
func TestCallerWinsOverVariant(t *testing.T) {
	facts := func(string) (ModelFacts, bool) {
		return ModelFacts{Thinking: Thinking{Default: true, Optional: true, Efforts: []Effort{EffortLow, EffortHigh}}}, true
	}
	body := sendBedrock(t, "m+high", facts, &Reasoning{Want: WantOff})
	if body["reasoning_effort"] != "none" {
		t.Errorf("reasoning_effort = %v, want none", body["reasoning_effort"])
	}
	// An effort the caller left unset is taken from the variant.
	body = sendBedrock(t, "m+low", facts, &Reasoning{Want: WantOn})
	if body["reasoning_effort"] != "low" {
		t.Errorf("reasoning_effort = %v, want low", body["reasoning_effort"])
	}
}

// The client's own model may carry the variant too.
func TestClientModelVariant(t *testing.T) {
	var body map[string]any
	srv := bodyCapture(t, &body)
	defer srv.Close()
	c := NewClientWithProvider(ProviderBedrock, srv.URL+"/v1", "k", "qwen.qwen3-32b+think").Catalog(qwenOnBedrock)
	if _, err := c.Complete(context.Background(), &ChatRequest{Messages: []Message{{Role: "user", Content: "hi"}}}); err != nil {
		t.Fatal(err)
	}
	if body["model"] != "qwen.qwen3-32b" || body["reasoning_effort"] != "high" {
		t.Errorf("model %v effort %v", body["model"], body["reasoning_effort"])
	}
}
