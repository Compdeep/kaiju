package models

import "testing"

// The rule that decides which models a lane forcing one small tool call may
// use. It had four copies — here, both startup lane checks and both settings
// pages — and a change to it missed one for a day. This is the definition those
// four now read, so it is the one worth pinning.
func TestWhatDisqualifiesAModelFromAForcedSmallCall(t *testing.T) {
	yes, no := true, false
	cases := []struct {
		name string
		in   Info
		want bool
	}{
		{"a plain tool-caller", Info{Tools: true, ToolCallOK: true, Thinking: &no}, true},
		{"reasons by default but can be told to stop", Info{Tools: true, ToolCallOK: true, Thinking: &yes, ReasoningOptional: true}, true},
		{"reasons and cannot be told to stop", Info{Tools: true, ToolCallOK: true, Thinking: &yes}, false},
		{"cannot make a small forced call", Info{Tools: true, ToolCallOK: false, Thinking: &no}, false},
		{"cannot call tools at all", Info{Tools: false, ToolCallOK: true, Thinking: &no}, false},
	}
	for _, c := range cases {
		if got := c.in.FitsForcedSmallCall(); got != c.want {
			t.Errorf("%s: FitsForcedSmallCall() = %v, want %v", c.name, got, c.want)
		}
	}
}

// Reasoning that is merely ON is not the complaint — the lanes send it off and
// the model obeys. Only reasoning that cannot be switched off is.
func TestOnlyReasoningThatCannotStopIsLocked(t *testing.T) {
	yes, no := true, false
	if (Info{Thinking: &yes, ReasoningOptional: true}).ReasoningLocked() {
		t.Error("a model that can be told to stop reasoning is reported as locked")
	}
	if !(Info{Thinking: &yes}).ReasoningLocked() {
		t.Error("a model whose reasoning is mandatory is not reported as locked")
	}
	if (Info{Thinking: &no}).ReasoningLocked() {
		t.Error("a model with no reasoning phase is reported as locked")
	}
}

// The serialised field is the predicate, on every entry, without any serving
// path having to remember to compute it. More than one program serves this
// catalog, and only one of them would have remembered.
func TestTheServedFlagMatchesThePredicate(t *testing.T) {
	all := All()
	if len(all) == 0 {
		t.Fatal("the catalog is empty")
	}
	for _, m := range all {
		if m.FitsSmallCall != m.FitsForcedSmallCall() {
			t.Errorf("%s: fits_small_call = %v but the predicate says %v",
				m.ID, m.FitsSmallCall, m.FitsForcedSmallCall())
		}
	}
}

// And the list is exactly what the flag selects.
func TestTheForcedSmallCallListIsTheFlag(t *testing.T) {
	want := 0
	for _, m := range All() {
		if m.FitsSmallCall {
			want++
		}
	}
	if got := len(ForcedSmallCall()); got != want {
		t.Errorf("ForcedSmallCall() returned %d, the flag selects %d", got, want)
	}
}
