package agent

import (
	"testing"

	"github.com/Compdeep/kaiju/agent/llm"
)

// The aggregator's thinking is the operator's to switch, and an unset or
// unknown value states nothing, so the lane's own rule applies.
func TestAggregatorReasoningSetting(t *testing.T) {
	for in, want := range map[string]llm.Want{
		"on": llm.WantOn, "off": llm.WantOff, " OFF ": llm.WantOff, "": llm.WantAuto, "maybe": llm.WantAuto,
	} {
		if got := wantOf(in); got != want {
			t.Errorf("wantOf(%q) = %v, want %v", in, got, want)
		}
	}
}
