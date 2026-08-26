package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// The failures of 25 August 2026, as whole runs.
//
// Every one of them was invisible to this suite while it stayed green, and
// every one looked from outside like "the planner is broken". They are here as
// runs rather than as unit tests because that is how they presented: a value
// produced by one stage and gone by the next, which no test of a single
// function can see.
//
// Two of these pass now. One does not, and is marked: it is the exit criterion
// for the phase that carries typed results into stages.

// fileTool answers like file_read: the text of a file in the envelope's
// content, and its own bookkeeping under data. The split matters — content is
// the envelope's, path is the payload's, and a reference may name either.
type fileTool struct {
	name string
	path string
	text string
	got  map[string]any
}

func (f *fileTool) Name() string              { return f.name }
func (f *fileTool) Description() string       { return "reads a file" }
func (f *fileTool) Impact(map[string]any) int { return toolapi.ImpactObserve }
func (f *fileTool) RequiresTarget() bool      { return false }
func (f *fileTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
		"path":{"type":"string"},"command":{"type":"string"},"goal":{"type":"string"}}}`)
}
func (f *fileTool) OutputSchema() json.RawMessage {
	return toolapi.EnvelopeSchema(`{"type":"object","properties":{
		"path":{"type":"string","description":"the file that was read"},
		"lines_total":{"type":"integer","description":"lines in the file"}}}`)
}
func (f *fileTool) Execute(_ context.Context, params map[string]any) (string, error) {
	f.got = params
	msg := toolapi.ToolOK("file", f.text, map[string]any{
		"path": f.path, "lines_total": 3,
	})
	b, _ := json.Marshal(msg)
	return string(b), nil
}

// A plan wiring the envelope's own `content` runs.
//
// The planner's own prompt teaches this hand-off — read a file, compute over
// its text — and the plan-time check rejected it, because the lookup unwrapped
// the envelope before searching and `content` is not under `data`. Three
// corrections later the model had deleted the step it was told was wrong and
// written one that referenced itself.
func TestRun_APlanWiringEnvelopeContentIsNotRejected(t *testing.T) {
	reader := &fileTool{name: "file_read", path: "ttm.csv", text: "a,b,c"}
	consumer := &countingTool{name: "compute_over"}
	model := newStubModel(t, map[string]stubReply{
		"submit_preflight": {Args: map[string]any{"mode": "agent", "intent": "observe"}},
		"plan": plan(
			step("file_read", "read_csv", map[string]any{"path": "ttm.csv"}),
			stepDep("compute_over", "rank", map[string]any{"goal": "${step.0.content}"}, 0),
		),
		"submit_decision": {Args: map[string]any{"decision": "conclude", "outcome": "done"}},
	})
	a := agentOnStub(t, model, reader, consumer)

	if _, err := a.RunDAGSync(context.Background(), Trigger{
		Type: "chat_query", Data: json.RawMessage(`{"query":"rank the rows in ttm.csv"}`),
	}); err != nil {
		t.Fatalf("a correct plan was rejected: %v", err)
	}
	if consumer.calls != 1 {
		t.Fatalf("the consuming step ran %d times, want once — the plan was thrown out", consumer.calls)
	}
	if got, _ := consumer.got["goal"].(string); got != "a,b,c" {
		t.Errorf("the step received %q, want the file's text. The reference did not resolve.", got)
	}
	if n := model.callsTo("plan"); n != 1 {
		t.Errorf("the planner was called %d times — a correct plan should need no correction", n)
	}
}

// A reference naming a step by its TAG resolves to that step.
//
// Positions restart with every plan; tags do not, which is why a re-plan and
// the reflector both reach for them. Nothing read one: strconv.Atoi discarded
// its error and every tag silently became step 0. A reference meant for the
// second step read the first step's output, and where the step writing it WAS
// step 0 it referenced itself.
func TestRun_AReferenceByTagResolvesToThatStep(t *testing.T) {
	first := &fileTool{name: "file_read", path: "first.txt", text: "FIRST"}
	second := &fileTool{name: "other_read", path: "second.txt", text: "SECOND"}
	consumer := &countingTool{name: "compute_over"}
	model := newStubModel(t, map[string]stubReply{
		"submit_preflight": {Args: map[string]any{"mode": "agent", "intent": "observe"}},
		"plan": plan(
			step("file_read", "read_first", map[string]any{"path": "first.txt"}),
			step("other_read", "read_second", map[string]any{"path": "second.txt"}),
			stepDep("compute_over", "use_second",
				map[string]any{"goal": "${step.read_second.content}"}, 1),
		),
		"submit_decision": {Args: map[string]any{"decision": "conclude", "outcome": "done"}},
	})
	a := agentOnStub(t, model, first, second, consumer)

	if _, err := a.RunDAGSync(context.Background(), Trigger{
		Type: "chat_query", Data: json.RawMessage(`{"query":"read both"}`),
	}); err != nil {
		t.Fatalf("the run failed: %v", err)
	}
	got, _ := consumer.got["goal"].(string)
	if got == "FIRST" {
		t.Fatal("the tag resolved to step 0 — this is the bug: a tag became a position")
	}
	if got != "SECOND" {
		t.Errorf("the step received %q, want SECOND", got)
	}
}

// A stage is GIVEN the values the steps before it produced.
//
// This is the 25 August failure in miniature. The first arc reads a file and
// the tool returns where it put it. The reflector then asks for a re-plan, and
// the planner writing that second arc has to know the path.
//
// It did not. Its context was the worklog and the tool index, and a node's
// fields are in neither — so the only route from the step to the plan that
// followed it was a sentence the reflector retyped. It wrote a placeholder
// instead of the value, the placeholder resolved to nothing, and the plan used
// a path the model half-remembered while the real file sat on disk.
func TestRun_ThePlannerIsGivenWhatTheStepsProduced(t *testing.T) {
	reader := &fileTool{name: "file_read", path: "workspace/report_9f3a.csv", text: "a,b,c"}
	model := newStubModel(t, map[string]stubReply{
		"submit_preflight": {Args: map[string]any{"mode": "agent", "intent": "observe"}},
		"plan":             plan(step("file_read", "read_csv", map[string]any{"path": "ttm.csv"})),
	})
	// Arc one runs, then the reflector asks for another. Arc two is where the
	// planner needs what arc one produced.
	model.answerNth("submit_decision",
		stubReply{Args: map[string]any{
			"decision": "replan", "summary": "the file was read",
			"next": "work over the file that was read",
		}},
		stubReply{Args: map[string]any{"decision": "conclude", "outcome": "done"}},
	)
	a := agentOnStub(t, model, reader)

	if _, err := a.RunDAGSync(context.Background(), Trigger{
		Type: "chat_query", Data: json.RawMessage(`{"query":"read ttm.csv"}`),
	}); err != nil {
		t.Fatalf("the run failed: %v", err)
	}
	if n := model.callsTo("plan"); n < 2 {
		t.Fatalf("the run never re-planned (%d plan calls), so there is nothing to assert", n)
	}
	if !model.wasShown("plan", 1, "workspace/report_9f3a.csv") {
		t.Errorf("the re-plan was not given the path the first arc produced:\n%s",
			model.shownTo("plan", 1))
	}
}

// The results reach the planner as the calls that produced them, not as prose.
//
// A tool message paired to an assistant turn is what a model is trained to read
// as a return value. The same fields pasted into a paragraph are something it
// has to parse out of a sentence, which is where a value becomes a paraphrase.
func TestRun_ResultsArriveAsToolMessages(t *testing.T) {
	reader := &fileTool{name: "file_read", path: "workspace/report_9f3a.csv", text: "a,b,c"}
	model := newStubModel(t, map[string]stubReply{
		"submit_preflight": {Args: map[string]any{"mode": "agent", "intent": "observe"}},
		"plan":             plan(step("file_read", "read_csv", map[string]any{"path": "ttm.csv"})),
	})
	model.answerNth("submit_decision",
		stubReply{Args: map[string]any{"decision": "replan", "next": "keep going"}},
		stubReply{Args: map[string]any{"decision": "conclude", "outcome": "done"}},
	)
	a := agentOnStub(t, model, reader)

	if _, err := a.RunDAGSync(context.Background(), Trigger{
		Type: "chat_query", Data: json.RawMessage(`{"query":"read ttm.csv"}`),
	}); err != nil {
		t.Fatalf("the run failed: %v", err)
	}
	reqs := model.requestsTo("plan")
	if len(reqs) < 2 {
		t.Fatalf("the run never re-planned (%d plan calls)", len(reqs))
	}

	var declared, answered string
	for _, m := range reqs[1].Messages {
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			declared = strings.Join(m.ToolCalls, ",")
		}
		if m.Role == "tool" {
			answered = m.ToolCallID
		}
	}
	if declared != "file_read" {
		t.Errorf("no assistant turn declared the call that ran, got %q:\n%s",
			declared, model.shownTo("plan", 1))
	}
	if answered == "" {
		t.Errorf("no tool message carried the result:\n%s", model.shownTo("plan", 1))
	}
}

// Every tool message pairs with a call the assistant turn before it declared.
//
// The protocol's rule, not a preference: a tool_call_id with nothing declaring
// it is rejected by strict providers, and the whole request fails rather than
// the message being ignored.
func TestBuildMessagesWithResults_EveryToolMessagePairs(t *testing.T) {
	arcs := [][]StepResult{
		{
			{NodeID: "n1", Tool: "file_read", Name: "read_csv",
				Params: map[string]any{"path": "ttm.csv"}, Payload: json.RawMessage(`{"path":"x"}`)},
			{NodeID: "n2", Tool: "bash", Name: "grep_it",
				Params: map[string]any{"command": "grep x"}, Err: "exit 2"},
		},
		{
			{NodeID: "n3", Tool: "file_write", Name: "write_out",
				Params: map[string]any{"path": "out.csv"}, Payload: json.RawMessage(`{"bytes":12}`)},
		},
	}
	msgs := BuildMessagesWithResults("sys", "the objective", nil, arcs)

	declared := map[string]bool{}
	var lastRole string
	for _, m := range msgs {
		switch m.Role {
		case "assistant":
			for _, tc := range m.ToolCalls {
				declared[tc.ID] = true
			}
		case "tool":
			if !declared[m.ToolCallID] {
				t.Errorf("tool message %q pairs with no declared call", m.ToolCallID)
			}
			if lastRole != "assistant" && lastRole != "tool" {
				t.Errorf("a tool message followed a %q message; nothing may sit between "+
					"a call and its reply", lastRole)
			}
		}
		lastRole = m.Role
	}
	if msgs[0].Role != "system" {
		t.Errorf("first message is %q, want system", msgs[0].Role)
	}
	if last := msgs[len(msgs)-1]; last.Role != "user" || last.Content != "the objective" {
		t.Errorf("the objective must be read last, got %q %q", last.Role, last.Content)
	}
	if len(declared) != 3 {
		t.Errorf("declared %d calls, want 3", len(declared))
	}
}

// A failed step is a result. A stage deciding what to do next needs the error
// more than it needs the successes.
func TestBuildMessagesWithResults_AFailureIsCarried(t *testing.T) {
	msgs := BuildMessagesWithResults("sys", "obj", nil, [][]StepResult{{
		{NodeID: "n1", Tool: "bash", Name: "grep_it",
			Params: map[string]any{"command": "grep x /nope"},
			Err:    "grep: /nope: No such file or directory"},
	}})
	var got string
	for _, m := range msgs {
		if m.Role == "tool" {
			got = m.Content
		}
	}
	if !strings.Contains(got, "No such file") {
		t.Errorf("the failure did not reach the stage: %q", got)
	}
}

// An arc that ran nothing produces no assistant turn. A turn declaring no calls
// is not a legal message.
func TestBuildMessagesWithResults_EmptyArcsAreSkipped(t *testing.T) {
	msgs := BuildMessagesWithResults("sys", "obj", nil, [][]StepResult{{}, {}})
	if len(msgs) != 2 {
		t.Fatalf("want system + user only, got %d messages", len(msgs))
	}
}

// stepDep is step() with a dependency, for the plans that wire one.
func stepDep(tool, tag string, params map[string]any, deps ...int) map[string]any {
	s := step(tool, tag, params)
	s["depends_on"] = deps
	return s
}

// The reflector is given the arcs too, and a failure reaches it as an error
// field rather than as a sentence about one.
//
// Not so it can carry the value onward — it cannot, and does not need to. The
// planner is handed the same arcs directly, which is what closed the gap.
// Measured against the executor model with the path in a tool message and an
// explicit instruction to name it, the reply was "check the correct file path".
// A small model will not retype a value however it is given one.
func TestRun_TheReflectorIsGivenTheArcs(t *testing.T) {
	reader := &fileTool{name: "file_read", path: "workspace/report_9f3a.csv", text: "a,b,c"}
	model := newStubModel(t, map[string]stubReply{
		"submit_preflight": {Args: map[string]any{"mode": "agent", "intent": "observe"}},
		"plan":             plan(step("file_read", "read_csv", map[string]any{"path": "ttm.csv"})),
		"submit_decision":  {Args: map[string]any{"decision": "conclude", "outcome": "done"}},
	})
	a := agentOnStub(t, model, reader)

	if _, err := a.RunDAGSync(context.Background(), Trigger{
		Type: "chat_query", Data: json.RawMessage(`{"query":"read ttm.csv"}`),
	}); err != nil {
		t.Fatalf("the run failed: %v", err)
	}
	reqs := model.requestsTo("submit_decision")
	if len(reqs) == 0 {
		t.Fatal("the reflector never ran")
	}
	var sawCall, sawResult bool
	for _, m := range reqs[0].Messages {
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			sawCall = true
		}
		if m.Role == "tool" && strings.Contains(m.Content, "workspace/report_9f3a.csv") {
			sawResult = true
		}
	}
	if !sawCall || !sawResult {
		t.Errorf("the reflector was not given the arc (call=%v result=%v):\n%s",
			sawCall, sawResult, model.shownTo("submit_decision", 0))
	}
}

// A plan wired with the declared shape runs, and the value arrives.
//
// No ${step.N.content} anywhere: the reference is an object the provider can
// validate, and the dependency is not stated — the reference IS the dependency.
func TestRun_ADeclaredReferenceCarriesTheValue(t *testing.T) {
	reader := &fileTool{name: "file_read", path: "ttm.csv", text: "a,b,c"}
	consumer := &countingTool{name: "compute_over"}
	model := newStubModel(t, map[string]stubReply{
		"submit_preflight": {Args: map[string]any{"mode": "agent", "intent": "observe"}},
		"plan": plan(
			step("file_read", "read_csv", map[string]any{"path": "ttm.csv"}),
			step("compute_over", "rank", map[string]any{
				"goal": map[string]any{"step": "read_csv", "field": "content"},
			}),
		),
		"submit_decision": {Args: map[string]any{"decision": "conclude", "outcome": "done"}},
	})
	a := agentOnStub(t, model, reader, consumer)

	if _, err := a.RunDAGSync(context.Background(), Trigger{
		Type: "chat_query", Data: json.RawMessage(`{"query":"rank the rows in ttm.csv"}`),
	}); err != nil {
		t.Fatalf("a declared plan was rejected: %v", err)
	}
	if consumer.calls != 1 {
		t.Fatalf("the consuming step ran %d times, want once", consumer.calls)
	}
	if got, _ := consumer.got["goal"].(string); got != "a,b,c" {
		t.Errorf("the step received %q, want the file's text", got)
	}
	if n := model.callsTo("plan"); n != 1 {
		t.Errorf("the planner was corrected %d times for a correct plan", n-1)
	}
}

// The string form still works while both are accepted, so a model that has not
// moved is not a broken run.
func TestRun_TheStringFormStillWorks(t *testing.T) {
	reader := &fileTool{name: "file_read", path: "ttm.csv", text: "a,b,c"}
	consumer := &countingTool{name: "compute_over"}
	model := newStubModel(t, map[string]stubReply{
		"submit_preflight": {Args: map[string]any{"mode": "agent", "intent": "observe"}},
		"plan": plan(
			step("file_read", "read_csv", map[string]any{"path": "ttm.csv"}),
			stepDep("compute_over", "rank", map[string]any{"goal": "${step.read_csv.content}"}, 0),
		),
		"submit_decision": {Args: map[string]any{"decision": "conclude", "outcome": "done"}},
	})
	a := agentOnStub(t, model, reader, consumer)

	if _, err := a.RunDAGSync(context.Background(), Trigger{
		Type: "chat_query", Data: json.RawMessage(`{"query":"rank the rows"}`),
	}); err != nil {
		t.Fatalf("the run failed: %v", err)
	}
	if got, _ := consumer.got["goal"].(string); got != "a,b,c" {
		t.Errorf("the string form stopped working: %q", got)
	}
}
