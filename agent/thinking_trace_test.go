package agent

import (
	"testing"

	"github.com/Compdeep/kaiju/agent/llm"
)

// The failure mode of the reasoning layer is silence: somebody sets an effort,
// the catalog says this model was never measured to act on it, nothing is sent,
// and the run is unchanged with nothing saying why.
//
// Asked and Sent differ exactly when that happens. That is the whole reason
// both are on the trace rather than one.

func TestTheTraceShowsWhatWasAskedAndWhatWentOut(t *testing.T) {
	for _, c := range []struct {
		name   string
		intent *llm.Reasoning
		want   string
	}{
		{"nothing", nil, ""},
		{"off", &llm.Reasoning{Want: llm.WantOff}, "off"},
		{"on", &llm.Reasoning{Want: llm.WantOn}, "on"},
		{"on at an effort", &llm.Reasoning{Want: llm.WantOn, Effort: llm.EffortHigh}, "on high"},
		{"an effort alone", &llm.Reasoning{Effort: llm.EffortMax}, "max"},
		{"a budget", &llm.Reasoning{Want: llm.WantOn, Budget: 2000}, "on (2000 tokens)"},
	} {
		if got := describeThinking(c.intent); got != c.want {
			t.Errorf("%s: asked = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestTheTraceShowsWhatTheWireCarried(t *testing.T) {
	on, off := true, false
	for _, c := range []struct {
		name string
		sent *llm.ReasoningControl
		want string
	}{
		{"nothing", nil, ""},
		{"off", &llm.ReasoningControl{Enabled: &off}, "off"},
		{"on", &llm.ReasoningControl{Enabled: &on}, "on"},
		{"on at an effort", &llm.ReasoningControl{Enabled: &on, Effort: "high"}, "on high"},
		{"bounded without being switched on", &llm.ReasoningControl{MaxTokens: 2000}, " (2000 tokens)"},
	} {
		if got := describeSent(c.sent); got != c.want {
			t.Errorf("%s: sent = %q, want %q", c.name, got, c.want)
		}
	}
}

// The pair that matters: an effort the model was never measured on is asked for
// and does not travel, and the trace says so rather than showing a setting that
// appears to have worked.
func TestAWithheldEffortIsVisibleInThePair(t *testing.T) {
	intent := &llm.Reasoning{Want: llm.WantOn, Effort: llm.EffortHigh}
	on := true
	narrowed := &llm.ReasoningControl{Enabled: &on} // the catalog dropped the effort

	asked, sent := describeThinking(intent), describeSent(narrowed)
	if asked == sent {
		t.Fatalf("asked and sent both read %q, so a withheld effort is invisible", asked)
	}
	if asked != "on high" || sent != "on" {
		t.Errorf("asked=%q sent=%q, want the effort present in one and absent in the other", asked, sent)
	}
}
