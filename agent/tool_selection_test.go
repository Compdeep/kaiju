package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// relevantTools ranks the registry against what the run is trying to do, and
// narrows only when the planner's tool index will not fit. These tests hold the
// order and the narrowing; agent/toolfind holds the search itself.

// The whole registry stays reachable. Ranking decides what the planner reads
// first, not what it is allowed to reach for — a tool the search ranked poorly
// is still there, because the search reads one-line descriptions before any
// work has run and cannot know what the task will turn out to need.
func TestRelevantTools_RanksWithoutRemoving(t *testing.T) {
	a := agentWithTools(t)
	got, _ := a.relevantTools(context.Background(), nil, Trigger{Type: "chat_query"}, "raise a ticket for the customer")

	if len(got) != len(a.registry.List()) {
		t.Errorf("ranking dropped tools: %d of %d (%v)", len(got), len(a.registry.List()), got)
	}
}

// An empty objective falls back to the trigger's own text rather than ranking
// against nothing — every caller has something to search with, and one that
// passes nothing has made a mistake this must not turn into an empty toolbox.
func TestRelevantTools_EmptyObjectiveStillReturnsTools(t *testing.T) {
	a := agentWithTools(t)
	got, _ := a.relevantTools(context.Background(), nil, Trigger{Type: "chat_query"}, "")
	if len(got) == 0 {
		t.Fatal("an empty objective emptied the toolbox")
	}
}

// The shell leads, whatever the ranking made of it.
//
// A search over descriptions has no way to know what a shell is for. Asked for
// the latest transactions on a blockchain it put the three web tools at the top
// and the shell fourteenth, when reading an RPC endpoint with curl is a route
// to the answer. Ranking answers what a task is about; the shell is there for
// when that answer turns out to be wrong.
func TestRelevantTools_ShellLeads(t *testing.T) {
	reg := toolapi.NewRegistry()
	for _, n := range []string{"list_records", shellToolName, "web_fetch"} {
		if err := reg.Register(&plainTool{name: n}); err != nil {
			t.Fatal(err)
		}
	}
	a := &Agent{registry: reg}

	got, _ := a.relevantTools(context.Background(), nil, Trigger{}, "read a web page about something")
	if len(got) == 0 || got[0] != shellToolName {
		t.Errorf("the shell did not lead: %v", got)
	}
	if n := countName(got, shellToolName); n != 1 {
		t.Errorf("the shell appears %d times, want exactly 1: %v", n, got)
	}
	if len(got) != 3 {
		t.Errorf("pinning the shell lost a tool: %v", got)
	}
}

// A registry with no shell is returned as it was ranked.
func TestRelevantTools_NoShellToPin(t *testing.T) {
	a := agentWithTools(t)
	got, _ := a.relevantTools(context.Background(), nil, Trigger{Type: "chat_query"}, "anything")
	if len(got) != len(a.registry.List()) {
		t.Errorf("a registry without a shell lost tools: %v", got)
	}
}

// Ranking says what is RELEVANT, never what is PERMITTED. A human-only tool
// stays withheld from an unattended run however highly it ranked.
func TestRelevantTools_RankingCannotGrantAccess(t *testing.T) {
	got, _ := agentWithTools(t).relevantTools(context.Background(), nil,
		Trigger{Type: "event", ExecutionMode: "autonomous"}, "raise a ticket")

	if has(got, "raise_ticket") {
		t.Errorf("a human-only tool reached an unattended run: %v", got)
	}
	if !has(got, "list_records") {
		t.Errorf("an ordinary tool went missing: %v", got)
	}
}

// Trimming is what a registry too large to show costs, and it comes off the
// bottom — where the ranking put the tools least likely to be needed.
func TestFitToolIndex_TrimsFromTheBottom(t *testing.T) {
	reg := toolapi.NewRegistry()
	var names []string
	big := strings.Repeat("x", toolIndexBudget.Base/4)
	for _, n := range []string{"first", "second", "third", "fourth", "fifth", "sixth"} {
		if err := reg.Register(&wordyTool{name: n, desc: big}); err != nil {
			t.Fatalf("register %s: %v", n, err)
		}
		names = append(names, n)
	}
	a := &Agent{registry: reg}

	got := a.fitToolIndex(nil, names)
	if len(got) == 0 {
		t.Fatal("the whole index was trimmed away")
	}
	if len(got) >= len(names) {
		t.Fatalf("an over-budget index was not trimmed: kept %d of %d", len(got), len(names))
	}
	if got[0] != "first" {
		t.Errorf("trimmed from the top: %v", got)
	}
	size := 0
	for _, n := range got {
		size += toolIndexEntrySize(reg, n)
	}
	if size > toolIndexBudget.Base {
		t.Errorf("kept %d chars, budget is %d", size, toolIndexBudget.Base)
	}
}

// A registry that fits is shown whole. Narrowing is a cost of size, not
// something done to a small set on principle.
func TestFitToolIndex_KeepsAnIndexThatFits(t *testing.T) {
	a := agentWithTools(t)
	names := a.registry.List()
	if got := a.fitToolIndex(nil, names); len(got) != len(names) {
		t.Errorf("a registry that fits was trimmed: kept %d of %d", len(got), len(names))
	}
}

// newIndexedAgent is an agent whose tool index is live, for the tests that
// care what the ranking does rather than what surrounds it.
func newIndexedAgent(reg *toolapi.Registry) (*Agent, error) {
	a := &Agent{registry: reg}
	a.toolIndex = a.openToolIndex()
	return a, nil
}

func countName(names []string, want string) int {
	n := 0
	for _, s := range names {
		if s == want {
			n++
		}
	}
	return n
}

// The re-plan bug, held shut.
//
// Tool choice used to be made once, at the top of a turn, by preflight. A
// re-plan then got the tools the FIRST plan was given — while the reflector's
// account of what was missing went into the prompt and never near the search.
// A run that needed a tool it had not been shown could not ask for one.
//
// So the objective a re-plan ranks against has to carry the frame, and the
// tools it gets back have to differ because of it.
func TestObjective_ReplanFrameReachesTheSearch(t *testing.T) {
	a := agentWithTools(t)
	trigger := chatTrigger("sort out the customer's problem")

	first := a.objective(trigger, nil)
	replan := a.objective(trigger, nil, "\n\n## Re-plan\nReflector says the next move is: raise a ticket with support.")

	if !strings.Contains(first, "sort out the customer's problem") {
		t.Errorf("the objective lost the user's own words: %q", first)
	}
	if first == replan {
		t.Fatal("a re-plan produced the same objective as the first plan; the frame never reached it")
	}
	if !strings.Contains(replan, "raise a ticket with support") {
		t.Errorf("the re-plan objective lost the reflector's next move: %q", replan)
	}
}

// And the search has to act on it: a re-plan naming work the first objective
// never mentioned must rank differently.
func TestRelevantTools_ReplanRanksOnWhatIsNowMissing(t *testing.T) {
	reg := toolapi.NewRegistry()
	if err := reg.Register(&wordyTool{name: "list_records", desc: "Read rows out of the customer database."}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(&wordyTool{name: "raise_ticket", desc: "Open a support ticket for a person to work on."}); err != nil {
		t.Fatal(err)
	}
	a, err := newIndexedAgent(reg)
	if err != nil {
		t.Fatal(err)
	}
	trigger := chatTrigger("read the customer's rows out of the database")

	first, _ := a.relevantTools(context.Background(), nil, trigger, a.objective(trigger, nil))
	if first[0] != "list_records" {
		t.Fatalf("the first plan ranked wrongly: %v", first)
	}

	replan, _ := a.relevantTools(context.Background(), nil, trigger,
		a.objective(trigger, nil, "\n\n## Re-plan\nReflector says the next move is: open a support ticket for a person to work on."))
	if replan[0] != "raise_ticket" {
		t.Errorf("the re-plan did not rank on what was now missing: %v", replan)
	}
}

// The narrowing is written down whether or not it narrowed.
//
// Six steps stand between the registry and the planner's prompt and only one
// removes a tool for being irrelevant. The other five leave the list alone in
// the ordinary case, so a planner shown everything looks the same as one shown
// a chosen everything — and the difference is the whole answer when a run is
// being read back. Each step reports its counts and its reason.
func TestRelevantTools_RecordsHowTheListWasNarrowed(t *testing.T) {
	a := agentWithTools(t)
	_, narrowing := a.relevantTools(context.Background(), nil, Trigger{Type: "chat_query"}, "list some records")

	if len(narrowing) == 0 {
		t.Fatal("nothing was recorded, so a run cannot say why the planner saw what it saw")
	}
	joined := strings.Join(narrowing, " | ")
	for _, want := range []string{"rank", "shell-first", "unattended", "scope", "categories", "fit-budget"} {
		if !strings.Contains(joined, want) {
			t.Errorf("step %q is missing from the record: %s", want, joined)
		}
	}
	// Every entry says what it did, in counts.
	for _, e := range narrowing {
		if !strings.Contains(e, "->") {
			t.Errorf("entry %q does not say what it did", e)
		}
	}
}

// A step that changed nothing still says WHY, because "54->54" reads the same
// for four different reasons and only one of them is the answer.
func TestRelevantTools_ANoOpStepStillGivesItsReason(t *testing.T) {
	a := agentWithTools(t)
	_, narrowing := a.relevantTools(context.Background(), nil, Trigger{Type: "chat_query"}, "list some records")

	var categories string
	for _, e := range narrowing {
		if strings.HasPrefix(e, "categories ") {
			categories = e
		}
	}
	if categories == "" {
		t.Fatal("the category step recorded nothing")
	}
	// With no graph there is no preflight, so it must say so rather than leave
	// the reader to infer it from equal counts.
	if !strings.Contains(categories, "no categories") && !strings.Contains(categories, "below the floor") {
		t.Errorf("the category step did not say why it did nothing: %q", categories)
	}
}

// Preflight's decisions reach the trace, the categories among them.
//
// The summary line says the lane, the rank and the guidance. The categories are
// what the tool index is then narrowed by, and they were on no line at all — so
// a run that showed every tool and one that showed a chosen few read the same,
// and the reason was in a field nothing rendered.
func TestPreflightDecided_CarriesWhatNarrowsTheToolIndex(t *testing.T) {
	pf := &PreflightResult{
		Mode:               "agent",
		RequiredCategories: []string{"process", "network"},
		Skills:             []string{"security"},
		ComputeMode:        "shallow",
		NeedsSynthesis:     true,
	}
	got := strings.Join(preflightDecided(pf), " | ")
	for _, want := range []string{"categories: process, network", "mode: agent", "guidance: security", "compute: shallow", "synthesis"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q missing from: %s", want, got)
		}
	}
}

// Naming none is an answer, not an absence: it is the first thing anyone asks
// when a planner was shown everything.
func TestPreflightDecided_SaysWhenNoCategoriesWereNamed(t *testing.T) {
	got := strings.Join(preflightDecided(&PreflightResult{Mode: "agent"}), " | ")
	if !strings.Contains(got, "categories: none named") {
		t.Errorf("an empty category list left no line: %s", got)
	}
}

func TestPreflightDecided_NilIsNotAPanic(t *testing.T) {
	if got := preflightDecided(nil); got != nil {
		t.Errorf("want nil, got %v", got)
	}
}
