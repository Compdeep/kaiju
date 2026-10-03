package agent

import (
	"strings"
	"testing"
)

// The planner's own text reaches the aggregator as evidence.
//
// A turn where the planner runs nothing has no steps, no timeline and no arcs, so
// without this the aggregator would be asked to write an answer from an empty run
// and would correctly report that it has none. What the planner concluded is the
// whole of the evidence for such a turn, so it travels, and what changes is the
// voice and the audience rather than the content.
func TestAggregatorPromptCarriesTheDirectAnswer(t *testing.T) {
	a := &Agent{}
	g := &Graph{DirectAnswer: "The Vue app is built and live; client-side rendering explains the empty fetch."}

	got := a.assembleAggregatorPrompt(Trigger{}, g, nil)

	if !strings.Contains(got, "The Vue app is built and live") {
		t.Fatalf("what the planner concluded did not reach the aggregator:\n%s", got)
	}
	// It must be told that this is all there is, or it reports a run it cannot see.
	if !strings.Contains(got, "No step ran this turn") {
		t.Errorf("the aggregator is not told the run was empty:\n%s", got)
	}
	// And told who it is writing to, since the planner was told the opposite.
	if !strings.Contains(got, "second person") {
		t.Errorf("the aggregator is not told to address the reader:\n%s", got)
	}
	// It must not be invited to extend what it was given.
	if !strings.Contains(got, "Add nothing to it") {
		t.Errorf("nothing stops the aggregator adding to a conclusion it cannot check:\n%s", got)
	}
}

// A graph with no direct answer is unchanged — the ordinary path must not grow a
// section describing an empty run.
func TestAggregatorPromptOmitsTheBlockWhenThereIsNoDirectAnswer(t *testing.T) {
	a := &Agent{}
	for name, g := range map[string]*Graph{
		"empty string": {DirectAnswer: ""},
		"nil graph":    nil,
	} {
		got := a.assembleAggregatorPrompt(Trigger{}, g, nil)
		if strings.Contains(got, "No step ran this turn") {
			t.Errorf("%s: the empty-run block was written anyway:\n%s", name, got)
		}
	}
}
