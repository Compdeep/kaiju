package agent

import (
	"strings"
	"testing"
)

// The reflector is asked for the user's answer every time it concludes, and on
// the paths where an aggregator follows, that answer is thrown away. On an
// interactive query it is thrown away EVERY time: decideAutoAggMode's first
// branch is "someone is waiting on this answer, so it is synthesised whatever
// the reflector said".
//
// So the question is asked before the call rather than after, and the reflector
// is told when not to bother. It still decides whether to stop — that was never
// the scheduler's — it is only spared writing a reply nothing reads.
func TestAggregatorWillWriteTheAnswer(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode int
		want bool
		why  string
	}{
		{"pinned to skip", 0, false,
			"agg_mode 0 means the reflector's outcome IS the answer; an application " +
				"reading the run's outcome depends on it being written"},
		{"pinned to the light lane", 1, true, "an aggregator is committed"},
		{"pinned to the heavy lane", 2, true, "an aggregator is committed"},
	} {
		a := &Agent{}
		got := a.aggregatorWillWriteTheAnswer(Trigger{AggMode: tc.mode}, NewGraph())
		if got != tc.want {
			t.Errorf("%s: got %v, want %v — %s", tc.name, got, tc.want, tc.why)
		}
	}
}

// Auto mode, and a person waiting. This is the case that pays: every chat query
// takes it, and the reflector's answer is discarded on every one.
func TestAggregatorWillWriteTheAnswer_WhenSomeoneIsWaiting(t *testing.T) {
	a := &Agent{}
	trig := Trigger{AggMode: -1, Type: "chat_query"}
	if !a.aggregatorWillWriteTheAnswer(trig, NewGraph()) {
		t.Error("an awaited run does not know its answer is being written by the aggregator — " +
			"so the reflector writes one too, and it is discarded")
	}
}

// Auto mode with nobody waiting and nothing else committing. The reflector's
// own aggregate flag decides that case and has not been written yet, so the
// question is asked with nil — and the fallback keeps the answer.
//
// The direction matters more than the case: unsure means WRITE it. Being wrong
// the other way loses the run's only reply.
func TestAggregatorWillWriteTheAnswer_UnsureMeansWriteIt(t *testing.T) {
	a := &Agent{}
	trig := Trigger{AggMode: -1, Type: "event", ExecutionMode: "autonomous"}
	if a.aggregatorWillWriteTheAnswer(trig, NewGraph()) {
		t.Error("an unawaited, uncommitted run suppressed the reflector's answer — " +
			"if no aggregator then runs, the run has no reply at all")
	}
}

// The instruction is carried in the prompt, not by removing the field from the
// schema. Those drifted apart once already: the prompt asked for "verdict" while
// the schema declared "outcome", and three quarters of concluding reflections
// had their answer silently dropped.
func TestTheReflectorIsToldInThePromptNotTheSchema(t *testing.T) {
	src := readSource(t, "reflection.go")
	if !strings.Contains(src, "aggregatorWillWriteTheAnswer") {
		t.Fatal("the reflector never asks whether its answer will be used")
	}
	if !strings.Contains(src, "Do not write the answer") {
		t.Error("the reflector is not told, so it writes an answer that is discarded")
	}
	if strings.Contains(src, "reflectorSchema(") && strings.Contains(src, "reflectorSchema(true") {
		t.Error("the schema is being varied per call — one shape with one instruction " +
			"beside it is what stops the prompt and the schema drifting apart")
	}
}
