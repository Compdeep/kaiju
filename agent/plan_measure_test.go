package agent

import (
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/llm"
)

// The line carries what a replay used to be needed for: whether the reply
// stopped or was cut, whether a shorter plan was asked for, whether the steps
// were salvaged, how many steps against how many distinct tags, how many came
// back typed compute, the tokens with the thinking split out, and the host.
func TestPlanMeasureLine_SaysWhatHappened(t *testing.T) {
	resp := &llm.ChatResponse{Provider: "DeepInfra"}
	resp.Usage.PromptTokens, resp.Usage.CompletionTokens = 19152, 5500
	resp.Usage.Details = &llm.UsageDetails{ReasoningTokens: 4962}
	steps := []PlanStep{
		{Tool: "get_process", Tag: "get_proc"},
		{Tool: "bash", Tag: "read_1", Type: "compute"},
		{Tool: "bash", Tag: "read_1"},
	}
	line := planMeasureLine(2, resp, "length", "stop", "stop", steps)
	for _, want := range []string{
		"[plan] ", "round=2", "first=length", "retry=stop", "salvaged=false",
		"steps=3", "distinct_tags=2", "compute=1",
		"in=19152", "out=5500", "think=4962", "host=DeepInfra",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("line lacks %q: %s", want, line)
		}
	}
	if strings.Count(line, "\n") != 0 {
		t.Errorf("more than one line: %q", line)
	}
}

// A first reply that stopped on its own asked for nothing, and a plan read from
// a cut reply was salvaged. Missing facts print as "-", never as a blank that
// breaks a key=value reader.
func TestPlanMeasureLine_AbsentFactsAreMarked(t *testing.T) {
	line := planMeasureLine(0, nil, "stop", "", "stop", nil)
	for _, want := range []string{"retry=-", "host=-", "salvaged=false", "steps=0", "think=0"} {
		if !strings.Contains(line, want) {
			t.Errorf("line lacks %q: %s", want, line)
		}
	}
	if line = planMeasureLine(0, nil, "length", "length", "length", nil); !strings.Contains(line, "salvaged=true") {
		t.Errorf("a plan read from a cut reply is salvaged: %s", line)
	}
}
