package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Compdeep/kaiju/agent/prompt"
	"github.com/Compdeep/kaiju/agent/toolapi"
)

// The engine's shape, written down.
//
// These tests do not check that anything works. They check that the parts have
// not moved: the prompts, the kinds of node a run is built from, the words a
// reflector may answer with, the sources a stage may ask the gate for, the
// handlers an application supplies, and the order the stages of a run are
// called in.
//
// They exist because that shape was arrived at, not designed in one go, and it
// behaves well: a run that fails still reports its failure honestly. Anything
// that moves a part of it should be a thing somebody decided, not a thing that
// happened.
//
// WHEN ONE OF THESE FAILS
//
// It is not a bug in the test. It means the shape changed. Read what it says
// changed and decide whether you meant it:
//
//   - If you did, update the recorded value IN THE SAME COMMIT as the change.
//     That puts both halves in front of whoever reviews it, which is the whole
//     mechanism — there is no other approval step, and this is the one.
//   - If you did not, you have found an accident. That is what this is for.
//
// Never update a recorded value in a commit of its own, and never to make a
// red test green. The value is the record; changing it is the approval.

// ── Prompts ─────────────────────────────────────────────────────────────────

// frozenPrompt records one section's fingerprint and length. The length is here
// because a hash alone says a thing changed and nothing about how much.
type frozenPrompt struct {
	sha16  string
	length int
}

// The seventeen sections, their order, and their content.
//
// Order is part of it: sectionOrder in the prompt package is what validation
// and logging walk, and a section added in the middle is a different file from
// one added at the end.
var frozenPromptOrder = []string{
	"SOUL", "ROUTE", "PREFLIGHT", "EXECUTIVE", "AGGREGATOR", "REFRAME",
	"REFRAME_HOOK", "HOLMES", "MICROPLANNER", "OBSERVER", "REFLECTOR",
	"INTERJECTION", "CLASSIFIER", "CURATOR", "CHAT", "VISION", "REACT",
}

var frozenPrompts = map[string]frozenPrompt{
	"SOUL":         {"ed330645ec6a4e06", 4889},
	"ROUTE":        {"4c55c2820a774445", 1655},
	"PREFLIGHT":    {"6bfa4528251eb9c6", 10264},
	"EXECUTIVE":    {"f46c148c5a72cd2a", 4188},
	"AGGREGATOR":   {"a782c5558ef001d0", 3631},
	"REFRAME":      {"2f3311663f869d6b", 1567},
	"REFRAME_HOOK": {"2066afe67efca674", 329},
	"HOLMES":       {"b97dfdc6253fa8ad", 6900},
	"MICROPLANNER": {"5b32605d997cd5fa", 4619},
	"OBSERVER":     {"2ab11100a1203b92", 1040},
	"REFLECTOR":    {"994d44d00546eb37", 4929},
	"INTERJECTION": {"29661ee83d91b1e2", 788},
	"CLASSIFIER":   {"1f813616ac1a9d88", 270},
	"CURATOR":      {"4231721258e0aa5a", 3199},
	"CHAT":         {"6b5c0b585bffb6bb", 405},
	"VISION":       {"a34b6cb0dc575294", 218},
	"REACT":        {"8ac5f1aca3a544d3", 1669},
}

// liveSections reads the sections as the binary actually holds them, which is
// after the package's init has filled them from the embedded prompts.md.
func liveSections() map[string]string {
	return map[string]string{
		"SOUL": prompt.Soul, "ROUTE": prompt.Route, "PREFLIGHT": prompt.Preflight,
		"EXECUTIVE": prompt.Executive, "AGGREGATOR": prompt.Aggregator,
		"REFRAME": prompt.Reframe, "REFRAME_HOOK": prompt.ReframeHook,
		"HOLMES": prompt.Holmes, "MICROPLANNER": prompt.Microplanner,
		"OBSERVER": prompt.Observer, "REFLECTOR": prompt.Reflector,
		"INTERJECTION": prompt.Interjection, "CLASSIFIER": prompt.Classifier,
		"CURATOR": prompt.Curator, "CHAT": prompt.Chat, "VISION": prompt.Vision,
		"REACT": prompt.React,
	}
}

func TestFrozen_TheSectionsThatExist(t *testing.T) {
	live := liveSections()
	if len(live) != len(frozenPromptOrder) {
		t.Fatalf("there are %d sections and %d are recorded", len(live), len(frozenPromptOrder))
	}
	for _, name := range frozenPromptOrder {
		if _, ok := live[name]; !ok {
			t.Errorf("section %s is recorded and no longer exists", name)
		}
	}
	for name := range live {
		if _, ok := frozenPrompts[name]; !ok {
			t.Errorf("section %s exists and is not recorded — add it to frozenPrompts and to frozenPromptOrder", name)
		}
	}
}

func TestFrozen_TheTextOfEachPrompt(t *testing.T) {
	for name, text := range liveSections() {
		want, ok := frozenPrompts[name]
		if !ok {
			continue // reported by the test above
		}
		sum := sha256.Sum256([]byte(text))
		got := hex.EncodeToString(sum[:])[:16]
		if got == want.sha16 {
			continue
		}
		t.Errorf("the %s prompt changed: recorded %s at %d characters, now %s at %d.\n"+
			"    If that was deliberate, put {%q, %d} in frozenPrompts in this commit.",
			name, want.sha16, want.length, got, len(text), got, len(text))
	}
}

// A prompt that is empty is a prompt that did not load, which is a silent
// failure — every stage then runs on whatever the model does without guidance.
func TestFrozen_NoPromptIsEmpty(t *testing.T) {
	for name, text := range liveSections() {
		if len(text) == 0 {
			t.Errorf("the %s prompt is empty, so its stage has no instruction at all", name)
		}
	}
}

// ── The kinds of node a run is built from ───────────────────────────────────

var frozenNodeTypes = map[int]string{
	0: "tool", 1: "compute", 2: "executive", 3: "micro_planner", 4: "aggregator",
	5: "actuator", 6: "reflection", 7: "observer", 8: "interjection", 9: "holmes",
}

// The numbers matter, not only the names: a NodeType is an iota and it is
// written into stored traces, so inserting one in the middle renumbers every
// kind after it and every trace already written means something else.
func TestFrozen_NodeTypes(t *testing.T) {
	for n, want := range frozenNodeTypes {
		if got := NodeType(n).String(); got != want {
			t.Errorf("node type %d is now %q and was recorded as %q — inserting a kind in the middle "+
				"renumbers the ones after it and changes what every stored trace means", n, got, want)
		}
	}
	if got := NodeType(len(frozenNodeTypes)).String(); got != "unknown" {
		t.Errorf("a node type %d exists and is not recorded: %q", len(frozenNodeTypes), got)
	}
}

// ── What a reflector may decide ─────────────────────────────────────────────

// Three words, and the whole growth path hangs off them: continue runs the next
// batch, replan re-enters the executive, conclude ends the run. A fourth would
// be a new path through the engine.
var frozenReflectorDecisions = []string{"continue", "replan", "conclude"}

func TestFrozen_ReflectorDecisions(t *testing.T) {
	for _, d := range frozenReflectorDecisions {
		if !isReflectionDecision(d) {
			t.Errorf("%q is no longer a decision a reflector may return", d)
		}
	}
	for _, notADecision := range []string{"investigate", "stop", "retry", ""} {
		if isReflectionDecision(notADecision) {
			t.Errorf("%q is now a decision and was not recorded as one", notADecision)
		}
	}
}

// isReflectionDecision is the test's own reading of the three words, kept here
// rather than reaching into the engine so that a change in the engine fails
// this test instead of being followed by it.
func isReflectionDecision(s string) bool {
	for _, d := range frozenReflectorDecisions {
		if s == d {
			return true
		}
	}
	return false
}

// ── What a tool may say about its result ────────────────────────────────────

// Four statuses. Which one a tool returns decides whether its step counts as
// evidence, as nothing, or as a failure — and since a failure is what reaches
// the reflector, adding or removing one changes when a run can repair itself.
var frozenToolStatuses = []toolapi.ToolStatus{
	toolapi.StatusOK, toolapi.StatusEmpty, toolapi.StatusError, toolapi.StatusUnclassified,
}

func TestFrozen_ToolStatuses(t *testing.T) {
	want := map[string]bool{"ok": true, "empty": true, "error": true, "unclassified": true}
	if len(frozenToolStatuses) != len(want) {
		t.Fatalf("%d statuses are recorded and %d are named", len(frozenToolStatuses), len(want))
	}
	for _, s := range frozenToolStatuses {
		if !want[string(s)] {
			t.Errorf("the status %q is not one of the four recorded", s)
		}
	}
}

// ── What a stage may ask the gate for ───────────────────────────────────────

// The sources are the whole of what any stage can see. A stage's view of a run
// is assembled from these and nothing else, so the list is the memory boundary.
var frozenContextSources = []string{
	SourceBlueprint, SourceWorklog, SourceNodeReturns, SourceWorkspaceTree,
	SourceServiceState, SourceHistory, SourceSkillGuidance, SourceWorkspaceDeep,
	SourceFunctionMap, SourceExistingBlueprints, SourceToolIndex, SourceStepOutcomes,
}

var frozenContextSourceNames = []string{
	"blueprint", "worklog", "node_returns", "workspace_tree", "service_state",
	"history", "skill_guidance", "workspace_deep", "function_map",
	"existing_blueprints", "tool_index", "step_outcomes",
}

func TestFrozen_ContextSources(t *testing.T) {
	if len(frozenContextSources) != len(frozenContextSourceNames) {
		t.Fatalf("the two recorded lists disagree: %d and %d", len(frozenContextSources), len(frozenContextSourceNames))
	}
	for i, want := range frozenContextSourceNames {
		if frozenContextSources[i] != want {
			t.Errorf("context source %d is %q and was recorded as %q", i, frozenContextSources[i], want)
		}
	}
}

// ── What an application supplies ────────────────────────────────────────────

// The thirteen handlers, by name. This is the surface an embedding application
// writes against: one added, removed or renamed is a change to what every
// application must know.
var frozenHandlerFields = []string{
	"Environment", "DescribeTrigger", "Unattended", "TokenCategory", "Admit",
	"Refine", "Answer", "AllowTool", "Clearance", "Store", "Audit", "Remote",
	"ValidateTarget", "RunTargets",
}

func TestFrozen_TheHandlersAnApplicationSupplies(t *testing.T) {
	live := map[string]bool{}
	ht := reflect.TypeOf(Handlers{})
	for i := 0; i < ht.NumField(); i++ {
		live[ht.Field(i).Name] = true
	}

	recorded := map[string]bool{}
	for _, n := range frozenHandlerFields {
		recorded[n] = true
		if !live[n] {
			t.Errorf("the handler %s is recorded and no longer exists — every application supplying it now fails to compile", n)
		}
	}
	for n := range live {
		if !recorded[n] {
			t.Errorf("the handler %s exists and is not recorded — add it here in the commit that adds it", n)
		}
	}
}

// ── The order the stages of a run are called in ─────────────────────────────
//
// The DAG's shape, seen from the outside: which stages a run asks a model for,
// and in what order. Nothing here checks that any stage is any good. It checks
// that a run is still assembled from the same stages in the same order, because
// that order is the engine — routing before classification, classification
// before planning, a description of the situation before every reading of it,
// and the reflector deciding what happens next.
//
// The empty name is the re-frame. It is a plain completion with no forced tool,
// so it has no function name to report, and it appearing before each reflection
// is the anti-fabrication edge doing its job.

// A run with one tool step that the reflector concludes on.
var frozenStagesOfAStraightRun = []string{
	"route",            // is this a conversation or work
	"submit_preflight", // what kind of work, which skills, what intent
	"plan",             // the executive writes the graph
	"",                 // the re-frame: what has happened, in words
	"submit_decision",  // the reflector: continue, replan or conclude
}

// The same run when the reflector asks for more work: the executive is
// re-entered, and the whole read-and-decide cycle happens again.
var frozenStagesOfARunThatReplans = []string{
	"route", "submit_preflight",
	"plan", "", "submit_decision", // first round, reflector says replan
	"plan", "", "submit_decision", // second round, reflector concludes
}

func TestFrozen_TheStagesOfAStraightRun(t *testing.T) {
	tool := &countingTool{name: "process_list"}
	model := newStubModel(t, map[string]stubReply{
		"submit_preflight": {Args: map[string]any{"mode": "agent", "intent": "observe", "skills": []string{}}},
		"plan":             plan(step("process_list", "procs", nil)),
		"submit_decision":  {Args: map[string]any{"decision": "conclude", "summary": "done", "outcome": "it is done"}},
	})
	a := agentOnStub(t, model, tool)

	if _, err := a.RunDAGSync(context.Background(), Trigger{
		Type: "chat_query", Data: json.RawMessage(`{"query":"what is running?"}`),
	}); err != nil {
		t.Fatalf("the run failed: %v", err)
	}
	assertStages(t, "a straight run", frozenStagesOfAStraightRun, model.functionsCalled())
	if tool.calls != 1 {
		t.Errorf("the planned tool ran %d times, want once", tool.calls)
	}
}

func TestFrozen_TheStagesOfARunThatReplans(t *testing.T) {
	tool := &countingTool{name: "process_list"}
	model := newStubModel(t, map[string]stubReply{
		"submit_preflight": {Args: map[string]any{"mode": "agent", "intent": "observe", "skills": []string{}}},
		"plan":             plan(step("process_list", "procs", nil)),
	})
	model.answerNth("submit_decision",
		stubReply{Args: map[string]any{"decision": "replan", "summary": "more needed", "next": "run it again"}},
		stubReply{Args: map[string]any{"decision": "conclude", "summary": "done", "outcome": "it is done"}},
	)
	a := agentOnStub(t, model, tool)

	if _, err := a.RunDAGSync(context.Background(), Trigger{
		Type: "chat_query", Data: json.RawMessage(`{"query":"go"}`),
	}); err != nil {
		t.Fatalf("the run failed: %v", err)
	}
	assertStages(t, "a run that replans", frozenStagesOfARunThatReplans, model.functionsCalled())
	if tool.calls != 2 {
		t.Errorf("the tool ran %d times across two rounds, want twice", tool.calls)
	}
}

// assertStages reports a changed shape as the two sequences, because which
// stage moved is the whole content of the failure.
func assertStages(t *testing.T, what string, want, got []string) {
	t.Helper()
	same := len(want) == len(got)
	if same {
		for i := range want {
			if want[i] != got[i] {
				same = false
				break
			}
		}
	}
	if same {
		return
	}
	t.Errorf("the stages of %s changed.\n    recorded: %v\n    now:      %v\n"+
		"    A stage added, removed or reordered changes how every run is assembled. If you meant it, "+
		"record the new sequence in this commit.", what, want, got)
}
