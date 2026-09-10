package agent

import (
	"testing"
	"time"
)

// A stage that is not a graph node still records when it ran.
//
// preflight, the executive and the aggregator are broadcast as bare labels, so
// nothing gave them a start or a duration — and the trace's own total therefore
// counted the slowest work in a run as zero. One run showed 54 seconds against
// a real 313: 112 of the missing seconds ran before the first timed node
// existed, and 12 after the last one finished.
func TestStamp_RecordsWhenAStageRan(t *testing.T) {
	at := time.Now().Add(-2 * time.Second)
	n := stamp(at, &NodeInfo{ID: "preflight", Type: "preflight"})

	if n.StartedMs != at.UnixMilli() {
		t.Errorf("StartedMs = %d, want %d", n.StartedMs, at.UnixMilli())
	}
	if n.Ms < 1900 || n.Ms > 2500 {
		t.Errorf("Ms = %d, want about 2000", n.Ms)
	}
	if n.StartedAt == "" {
		t.Error("StartedAt was not set, so a reader with no millisecond field sees nothing")
	}
}

// Nothing to stamp is not a crash, and an unset clock does not invent a start.
// A zero StartedMs is what the reader treats as "this stage was not timed".
func TestStamp_HandlesNothing(t *testing.T) {
	if stamp(time.Now(), nil) != nil {
		t.Error("a nil node came back non-nil")
	}
	n := stamp(time.Time{}, &NodeInfo{ID: "preflight"})
	if n.StartedMs != 0 || n.Ms != 0 {
		t.Errorf("an unset clock stamped a time: started=%d ms=%d", n.StartedMs, n.Ms)
	}
}

// The run's length is the last finish minus the first start, which is the
// arithmetic the trace view does. Adding durations is not the same thing: nodes
// run in concurrent batches, so a sum counts the same seconds several times.
//
// Five web_search nodes in one live run all started at the same second and
// finished within 2.4 seconds of wall clock. Their durations add to 8.5.
func TestASpanIsNotASum(t *testing.T) {
	base := time.Now().UnixMilli()
	// One batch of five, concurrent, plus a stage before and after.
	nodes := []NodeInfo{
		{ID: "preflight", StartedMs: base, Ms: 4000},
		{ID: "n1", StartedMs: base + 112000, Ms: 1496},
		{ID: "n2", StartedMs: base + 112000, Ms: 2435},
		{ID: "n3", StartedMs: base + 112000, Ms: 1505},
		{ID: "n4", StartedMs: base + 112000, Ms: 1714},
		{ID: "n5", StartedMs: base + 112000, Ms: 1341},
		{ID: "aggregator", StartedMs: base + 305000, Ms: 7800},
	}
	var sum int64
	first, last := int64(1<<62), int64(0)
	for _, n := range nodes {
		sum += n.Ms
		if n.StartedMs < first {
			first = n.StartedMs
		}
		if e := n.StartedMs + n.Ms; e > last {
			last = e
		}
	}
	span := last - first
	if span != 312800 {
		t.Errorf("span = %dms, want 312800 — first start to last finish", span)
	}
	if sum >= span {
		t.Errorf("the sum (%dms) is not smaller than the span (%dms); this case is "+
			"meant to show a sum missing the untimed gaps", sum, span)
	}
}
