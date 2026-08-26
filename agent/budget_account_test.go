package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// A cap that fires and a cap that does not look identical from outside, and
// that is the whole reason these numbers were impossible to judge. A run cut
// eleven times and a run cut none produced the same trace, the same log and the
// same answer shape — so "is 8000 too small" could only ever be argued, never
// measured.
func TestCapReport_CountsWhatWasCut(t *testing.T) {
	g := NewGraph()

	g.recordCut("evidence", 93000, 32000)
	g.recordCut("evidence", 12000, 8000)
	g.recordCut("tool result", 9000, 4096)

	report := g.CapReport()
	for _, want := range []string{"evidence cut 2", "tool result cut 1", "largest"} {
		if !strings.Contains(report, want) {
			t.Errorf("the report does not say %q:\n%s", want, report)
		}
	}
	// The largest single cut is named because an average hides one huge loss
	// among several small ones — and one huge loss is the case that changes an
	// answer.
	if !strings.Contains(report, "60KB") {
		t.Errorf("the largest cut (61000 chars) is not reported:\n%s", report)
	}
}

// Nothing cut, nothing said. A report that always prints something teaches a
// reader to skip it.
func TestCapReport_SilentWhenNothingWasCut(t *testing.T) {
	if got := NewGraph().CapReport(); got != "" {
		t.Errorf("a run that lost nothing reported %q", got)
	}
	if got := (*Graph)(nil).CapReport(); got != "" {
		t.Errorf("a nil graph reported %q", got)
	}
}

// A cut that removed nothing is not a cut. Recording one would make every run
// look starved.
func TestCapReport_ANonCutIsNotRecorded(t *testing.T) {
	g := NewGraph()
	g.recordCut("evidence", 500, 500)
	g.recordCut("evidence", 500, 900) // kept more than it was given: not a cut
	if got := g.CapReport(); got != "" {
		t.Errorf("a value that survived whole was recorded as cut: %q", got)
	}
}

// The ReAct path builds no graph, so it has nowhere to record. That must be a
// no-op rather than a panic — the accounting is an observation, and an
// observation must never be what breaks a run.
func TestCapReport_NoGraphIsNotAFailure(t *testing.T) {
	var g *Graph
	g.recordCut("evidence", 9000, 1000) // must not panic
}

// Counted across goroutines. Steps are cut on scheduler workers, and a per-run
// total that only sees one of them is worse than none.
func TestCapReport_CountsFromEveryWorker(t *testing.T) {
	g := NewGraph()
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func() {
			for j := 0; j < 50; j++ {
				g.recordCut("evidence", 100, 90)
			}
			done <- struct{}{}
		}()
	}
	for i := 0; i < 8; i++ {
		<-done
	}
	if !strings.Contains(g.CapReport(), "evidence cut 400") {
		t.Errorf("cuts from concurrent workers were lost: %s", g.CapReport())
	}
}

// A cap that measures tools must not report tools as characters. Twelve dropped
// tools rendered as "12c" reads as twelve characters — off by three orders of
// magnitude, in the direction that makes a real loss look like none.
func TestCapReport_ReportsEachCapInItsOwnUnit(t *testing.T) {
	g := NewGraph()
	g.recordCut("tool index", 27, 15)
	if got := g.CapReport(); !strings.Contains(got, "12 tools") {
		t.Errorf("the tool index cut is not reported in tools: %s", got)
	}
}

// The payload cap drops a payload whole rather than cutting it, so the loss is
// total and silent: the step returned fields, the stage downstream sees none,
// and the prose is read instead. That is the one cut that most needs counting.
func TestCapReport_ADroppedPayloadIsCounted(t *testing.T) {
	g := NewGraph()

	// Over the compiled floor, and over it in a way shortening cannot fix: many
	// keys rather than one long value. Every key is kept whatever its size —
	// which field exists must not depend on how much text another one holds —
	// so the only way past the cap is to drop the payload whole.
	big := strings.Repeat("x", 500)
	var fields []string
	for i := 0; i < 40; i++ {
		fields = append(fields, fmt.Sprintf("%q:%q", fmt.Sprintf("k%d", i), big))
	}
	body := toolMessageBody{msg: toolapi.ToolOK("probe", "",
		json.RawMessage("{"+strings.Join(fields, ",")+"}"))}

	if out := g.payloadOf(body); out != nil {
		t.Fatalf("a payload over the cap was returned anyway: %d chars", len(out))
	}
	if got := g.CapReport(); !strings.Contains(got, "payload cut 1") {
		t.Errorf("a dropped payload was not counted: %s", got)
	}
}

// A payload that fits is not a cut, and the run's cap is what decides — not the
// compiled floor, which is what a deployment with no model catalog gets.
func TestPayloadOf_TheRunsCapDecides(t *testing.T) {
	g := NewGraph()
	body := toolMessageBody{msg: toolapi.ToolOK("probe", "",
		json.RawMessage(`{"url":"https://example.test/a"}`))}
	if g.payloadOf(body) == nil {
		t.Fatal("a small payload was dropped")
	}
	if got := g.CapReport(); got != "" {
		t.Errorf("a payload that fit was counted as cut: %s", got)
	}
}
