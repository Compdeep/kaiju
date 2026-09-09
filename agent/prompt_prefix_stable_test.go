package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/skillmd"
	"github.com/Compdeep/kaiju/agent/toolapi"
)

// A provider reuses its work on a prompt only as far as the first character
// that differs from the last one it saw. Everything after that first difference
// is read again, and charged again, however identical it is.
//
// So what these tests assert is not "the text is nice" but "the beginning does
// not move". Two calls a quarter of an hour apart shared 17,242 characters of a
// 61,399-character system prompt — 28% — because a clock sat ahead of the tool
// index and the index itself was printed in relevance order.

// The tool index is the largest block in the prompt. Ranked, it reordered with
// the question and re-charged all of it; ordered, it is the same bytes twice.
func TestToolIndexIsTheSameWhateverOrderTheToolsArrive(t *testing.T) {
	reg := toolapi.NewRegistry()
	for _, n := range []string{"bash", "file_read", "web_fetch", "compute"} {
		reg.Register(&countingTool{name: n})
	}

	ranked := compileToolIndex(reg, []string{"web_fetch", "bash", "compute", "file_read"})
	otherRanking := compileToolIndex(reg, []string{"file_read", "compute", "bash", "web_fetch"})

	if ranked != otherRanking {
		n := 0
		for n < len(ranked) && n < len(otherRanking) && ranked[n] == otherRanking[n] {
			n++
		}
		t.Fatalf("the index differs at character %d, so everything after it is re-read:\n  A: %.60q\n  B: %.60q",
			n, ranked[n:], otherRanking[n:])
	}
}

// Every tool still appears — the order is fixed, nothing is dropped.
func TestToolIndexStillListsEveryTool(t *testing.T) {
	reg := toolapi.NewRegistry()
	names := []string{"bash", "file_read", "web_fetch"}
	for _, n := range names {
		reg.Register(&countingTool{name: n})
	}
	idx := compileToolIndex(reg, names)
	for _, n := range names {
		if !strings.Contains(idx, n) {
			t.Errorf("%s is missing from the index", n)
		}
	}
}

// And they are in a fixed order, so a future change that reintroduces ranking
// fails here rather than quietly costing a cache hit on every call.
func TestToolIndexIsInAFixedOrder(t *testing.T) {
	reg := toolapi.NewRegistry()
	for _, n := range []string{"zebra_tool", "alpha_tool", "middle_tool"} {
		reg.Register(&countingTool{name: n})
	}
	idx := compileToolIndex(reg, []string{"zebra_tool", "middle_tool", "alpha_tool"})
	a, m, z := strings.Index(idx, "alpha_tool"), strings.Index(idx, "middle_tool"), strings.Index(idx, "zebra_tool")
	if !(a < m && m < z) {
		t.Errorf("index order is alpha=%d middle=%d zebra=%d, want ascending", a, m, z)
	}
}

// The clock never leads the tool index.
//
// "Current time:" is the one line in the gate's output that differs on every
// call, and the index is the largest block that does not — 39,564 characters of
// a 61,399-character planner prompt. A clock in front of it moved the first
// differing character to 17,242, so the index and everything after it fell
// outside the run a provider can reuse.
//
// The worklog beside it changes anyway, so the clock costs nothing there.
func TestTheClockDoesNotLeadTheToolIndex(t *testing.T) {
	a := &Agent{skillGuidance: map[string]*skillmd.SkillMD{"review": guidanceSkill("review", ""+
		"## Aggregator Guidance\n\nrate it honestly\n")}}
	reg := toolapi.NewRegistry()
	reg.Register(&countingTool{name: "bash"})
	a.registry = reg

	g := NewGraph()
	g.ActiveCards = []string{"review"}
	g.Context = NewContextGate(g, &Trigger{}, a)

	// The index FIRST, so passing this means it was skipped rather than merely
	// ordered around.
	resp, err := g.Context.Get(context.Background(), ContextRequest{
		ReturnSources: Sources(
			ToolIndex([]string{"bash"}),
			LabelledGuidance("## Aggregator Guidance", "aggregator doctrine"),
		),
		MaxBudget: 6000,
	})
	if err != nil {
		t.Fatalf("gate: %v", err)
	}
	idx := resp.Sources[SourceToolIndex]
	if idx == "" {
		t.Fatal("no tool index came back, so there is nothing to keep stable")
	}
	if strings.Contains(idx, "Current time:") {
		t.Errorf("the clock leads the tool index — the index and everything after it stops caching:\n%.120s", idx)
	}
	if !strings.Contains(resp.Sources[SourceSkillGuidance], "Current time:") {
		t.Error("the clock did not land on the other source, so a stage reading gate context has no date")
	}
}

// With the index the only source returned, the clock still goes somewhere: a
// stage told the date by nobody writes a remembered one into a parameter.
func TestTheClockSurvivesWhenTheIndexIsTheOnlySource(t *testing.T) {
	a := &Agent{skillGuidance: map[string]*skillmd.SkillMD{}}
	reg := toolapi.NewRegistry()
	reg.Register(&countingTool{name: "bash"})
	a.registry = reg

	g := NewGraph()
	g.Context = NewContextGate(g, &Trigger{}, a)

	resp, err := g.Context.Get(context.Background(), ContextRequest{
		ReturnSources: Sources(ToolIndex([]string{"bash"})),
		MaxBudget:     6000,
	})
	if err != nil {
		t.Fatalf("gate: %v", err)
	}
	if !strings.Contains(resp.Sources[SourceToolIndex], "Current time:") {
		t.Error("the clock was dropped when the index was the only source")
	}
}
