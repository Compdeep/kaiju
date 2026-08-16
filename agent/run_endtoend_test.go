package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// A whole run, driven by a scripted model.
//
// Everything else is real: the registry, the plan, the graph, the scheduler's
// completion loop, the dispatcher, the tools. Only the model answers from a
// script, which is what makes the run reachable from a test at all.
//
// These exist because the suite has been silent three times during this
// migration while real behaviour disappeared — a data-flow check that stopped
// rejecting anything, a classifier that stopped running, and a kernel that lost
// both its modules. Two of the three fail here. Measured, not assumed: each was
// put back and the tests re-run.
//
// The third does not, and the reason is worth knowing. RunDAGSync is the
// synchronous path and does not go through the kernel's module system, so a
// kernel with no modules runs a plan perfectly well from here. Reaching that
// needs a test that submits through the queue instead, which is a harness of its
// own and is not this one.

// countingTool records what it was asked and answers with a fixed payload.
type countingTool struct {
	name  string
	calls int
	got   map[string]any
}

func (c *countingTool) Name() string                { return c.name }
func (c *countingTool) Description() string         { return "for the end-to-end tests" }
func (c *countingTool) Impact(map[string]any) int   { return toolapi.ImpactObserve }
func (c *countingTool) RequiresTarget() bool        { return false }
func (c *countingTool) Parameters() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (c *countingTool) Execute(_ context.Context, p map[string]any) (string, error) {
	c.calls++
	c.got = p
	return toolapi.ToolOK("listing", "2 processes", map[string]any{"count": 2}).JSON(), nil
}

// plan is what a scripted executive answers with.
func plan(steps ...map[string]any) stubReply {
	return stubReply{Args: map[string]any{"steps": steps}}
}

func step(tool, tag string, params map[string]any) map[string]any {
	if params == nil {
		params = map[string]any{}
	}
	return map[string]any{"tool": tool, "tag": tag, "params": params, "depends_on": []int{}}
}

// A plan with one tool step runs that tool and reaches an answer.
//
// The smallest whole run there is: classify, plan, execute, conclude. If any of
// those stages stops being wired to the next one, this stops passing — which is
// the thing no test in this package could say before.
func TestARunPlansExecutesAndAnswers(t *testing.T) {
	tool := &countingTool{name: "process_list"}
	model := newStubModel(t, map[string]stubReply{
		"submit_preflight": {Args: map[string]any{
			"mode": "agent", "intent": "observe", "skills": []string{},
		}},
		"plan": plan(step("process_list", "procs", nil)),
		"submit_decision": {Args: map[string]any{
			"decision": "conclude", "summary": "two processes", "outcome": "two processes are running",
		}},
	})
	a := agentOnStub(t, model, tool)

	res, err := a.RunDAGSync(context.Background(), Trigger{
		Type: "chat_query", Data: json.RawMessage(`{"query":"what is running?"}`),
	})
	if err != nil {
		t.Fatalf("the run failed: %v (stages called: %v)", err, model.functionsCalled())
	}
	if tool.calls != 1 {
		t.Errorf("the planned tool ran %d times, want once. Stages called: %v",
			tool.calls, model.functionsCalled())
	}
	if res == nil || res.Outcome == "" {
		t.Errorf("the run produced no outcome. Stages called: %v", model.functionsCalled())
	}
}

// The classifier runs, and what it returns reaches the plan.
//
// This is the behaviour that vanished when the engine's Config was adopted and
// nothing set ClassifierEnabled: preflight stopped running, no skill was
// selected, no intent inferred, and every test still passed.
func TestPreflightRunsAndItsIntentReachesTheRun(t *testing.T) {
	tool := &countingTool{name: "process_list"}
	model := newStubModel(t, map[string]stubReply{
		"submit_preflight": {Args: map[string]any{
			"mode": "agent", "intent": "observe", "skills": []string{},
		}},
		"plan":            plan(step("process_list", "procs", nil)),
		"submit_decision": {Args: map[string]any{"decision": "conclude", "outcome": "done"}},
	})
	a := agentOnStub(t, model, tool)

	if _, err := a.RunDAGSync(context.Background(), Trigger{
		Type: "chat_query", Data: json.RawMessage(`{"query":"what is running?"}`),
	}); err != nil {
		t.Fatalf("the run failed: %v", err)
	}
	if n := model.callsTo("submit_preflight"); n != 1 {
		t.Errorf("preflight ran %d times, want once — with it off, nothing selects "+
			"skills or infers intent. Stages called: %v", n, model.functionsCalled())
	}
}

// A tool result reaches the next step through a reference.
//
// The template resolver, the node body and the dispatcher, exercised together
// rather than one at a time: step two asks for a field of step one's output and
// has to receive the value, not the text of it.
func TestOneStepReadsTheOutputOfTheStepBefore(t *testing.T) {
	first := &countingTool{name: "process_list"}
	second := &countingTool{name: "get_process"}
	model := newStubModel(t, map[string]stubReply{
		"submit_preflight": {Args: map[string]any{"mode": "agent", "intent": "observe"}},
		"plan": plan(
			step("process_list", "procs", nil),
			step("get_process", "detail", map[string]any{"count": "${step.0.count}"}),
		),
		"submit_decision": {Args: map[string]any{"decision": "conclude", "outcome": "done"}},
	})
	a := agentOnStub(t, model, first, second)

	if _, err := a.RunDAGSync(context.Background(), Trigger{
		Type: "chat_query", Data: json.RawMessage(`{"query":"look at the processes"}`),
	}); err != nil {
		t.Fatalf("the run failed: %v (stages: %v)", err, model.functionsCalled())
	}
	if second.calls != 1 {
		t.Fatalf("the second step ran %d times, want once — a reference that does not "+
			"resolve fails the step before the tool is reached", second.calls)
	}
	if got := second.got["count"]; got != float64(2) {
		t.Errorf("the second step received count=%#v, want 2 — the value the first "+
			"step produced, not the text of the reference", got)
	}
}

// A tool that does not exist is not planned.
//
// The planner is given a registry and its steps are checked against it. A step
// naming something unregistered is dropped at plan time rather than failing at
// dispatch, where the cause is further from the cure.
func TestAStepNamingAToolThatIsNotThereIsDropped(t *testing.T) {
	tool := &countingTool{name: "process_list"}
	model := newStubModel(t, map[string]stubReply{
		"submit_preflight": {Args: map[string]any{"mode": "agent", "intent": "observe"}},
		"plan": plan(
			step("process_list", "procs", nil),
			step("no_such_tool", "invented", nil),
		),
		"submit_decision": {Args: map[string]any{"decision": "conclude", "outcome": "done"}},
	})
	a := agentOnStub(t, model, tool)

	if _, err := a.RunDAGSync(context.Background(), Trigger{
		Type: "chat_query", Data: json.RawMessage(`{"query":"look"}`),
	}); err != nil {
		t.Fatalf("a plan naming one unknown tool failed the whole run: %v", err)
	}
	if tool.calls != 1 {
		t.Errorf("the real step ran %d times, want once", tool.calls)
	}
}

// The reflector can send a run round again, and the second pass concludes.
//
// Two decisions, scripted in order. This reaches the completion loop's
// reflection handling, which is where the flag that a failed reflection used to
// leave set lives.
func TestAReflectionThatContinuesRunsAgain(t *testing.T) {
	tool := &countingTool{name: "process_list"}
	model := newStubModel(t, map[string]stubReply{
		"submit_preflight": {Args: map[string]any{"mode": "agent", "intent": "observe"}},
		"plan":             plan(step("process_list", "procs", nil)),
	})
	model.answerNth("submit_decision",
		stubReply{Args: map[string]any{"decision": "continue", "summary": "keep going"}},
		stubReply{Args: map[string]any{"decision": "conclude", "outcome": "done"}},
	)
	a := agentOnStub(t, model, tool)

	if _, err := a.RunDAGSync(context.Background(), Trigger{
		Type: "chat_query", Data: json.RawMessage(`{"query":"look"}`),
	}); err != nil {
		t.Fatalf("the run failed: %v (stages: %v)", err, model.functionsCalled())
	}
	if n := model.callsTo("submit_decision"); n < 1 {
		t.Errorf("the reflector never ran; stages called: %v", model.functionsCalled())
	}
}

// The registry reaches the planner, and the question reaches every stage.
//
// The cheapest check that prompt assembly still happens at all: a planner that
// is not told which tools exist cannot name one, and a stage that is not told
// the question is answering nothing in particular.
func TestThePlannerIsToldWhatItHasAndWhatWasAsked(t *testing.T) {
	tool := &countingTool{name: "process_list"}
	model := newStubModel(t, map[string]stubReply{
		"submit_preflight": {Args: map[string]any{"mode": "agent", "intent": "observe"}},
		"plan":             plan(step("process_list", "procs", nil)),
		"submit_decision":  {Args: map[string]any{"decision": "conclude", "outcome": "done"}},
	})
	a := agentOnStub(t, model, tool)

	if _, err := a.RunDAGSync(context.Background(), Trigger{
		Type: "chat_query", Data: json.RawMessage(`{"query":"what is running?"}`),
	}); err != nil {
		t.Fatalf("the run failed: %v", err)
	}
	if !model.sawSystemContaining("process_list") {
		t.Error("no stage was told the tool exists, so the planner cannot name it " +
			"and every plan it writes is invented")
	}
	if !model.sawUserContaining("what is running?") {
		t.Error("no stage was told what was asked")
	}
	if got := strings.Join(model.functionsCalled(), ","); !strings.Contains(got, "plan") {
		t.Errorf("the executive never ran; stages called: %s", got)
	}
}

// A step that declares a dependency and never references it is rejected.
//
// The check that catches it reads the params for a reference to another step's
// output. It once scanned for the text "${node." instead, which a half-written
// placeholder also contains, so the step passed and the tool was handed the
// placeholder as prose.
func TestAStepThatDependsOnSomethingItNeverReadsIsRejected(t *testing.T) {
	first := &countingTool{name: "process_list"}
	second := &countingTool{name: "compute"}
	model := newStubModel(t, map[string]stubReply{
		"submit_preflight": {Args: map[string]any{"mode": "agent", "intent": "observe"}},
		"plan": plan(
			step("process_list", "procs", nil),
			map[string]any{
				"tool": "compute", "tag": "build",
				"params":     map[string]any{"goal": "use ${node. and stop", "mode": "shallow"},
				"depends_on": []int{0},
			},
		),
		"submit_decision": {Args: map[string]any{"decision": "conclude", "outcome": "done"}},
	})
	a := agentOnStub(t, model, first, second)

	if _, err := a.RunDAGSync(context.Background(), Trigger{
		Type: "chat_query", Data: json.RawMessage(`{"query":"look"}`),
	}); err != nil {
		t.Fatalf("the run failed: %v", err)
	}
	if second.calls != 0 {
		t.Errorf("the step ran %d times. It declared a dependency and its only "+
			"reference to that dependency is half written, so nothing was ever "+
			"going to be injected and the tool received the placeholder as prose",
			second.calls)
	}
}

// A plan cut off at the token cap is asked for again, shorter.
//
// The planner reads finish_reason itself and retries with an instruction to use
// fewer, larger steps — which is better than reporting the truncation, and is
// why this stage keeps the unchecked lane call. Without it the fragment is
// returned as if the planner had chosen to answer in prose.
func TestATruncatedPlanIsAskedForAgainShorter(t *testing.T) {
	tool := &countingTool{name: "process_list"}
	model := newStubModel(t, map[string]stubReply{
		"submit_preflight": {Args: map[string]any{
			"mode": "agent", "intent": "observe", "skills": []string{},
		}},
		"submit_decision": {Args: map[string]any{
			"decision": "conclude", "summary": "two processes", "outcome": "two processes are running",
		}},
	})
	// Cut the first plan, answer the second.
	model.answerNth("plan",
		stubReply{Args: map[string]any{"steps": []map[string]any{step("process_list", "procs", nil)}}, Cut: true},
		plan(step("process_list", "procs", nil)),
	)
	a := agentOnStub(t, model, tool)

	res, err := a.RunDAGSync(context.Background(), Trigger{
		Type: "chat_query", Data: json.RawMessage(`{"query":"what is running?"}`),
	})
	if err != nil {
		t.Fatalf("a truncated plan ended the run instead of being asked for again: %v", err)
	}
	if n := model.callsTo("plan"); n != 2 {
		t.Errorf("the planner was called %d times, want 2 — the cut plan and the shorter one", n)
	}
	if tool.calls != 1 {
		t.Errorf("the retried plan's tool ran %d times, want once", tool.calls)
	}
	if res == nil || res.Outcome == "" {
		t.Error("the run recovered from the cut plan and still produced nothing")
	}
}

// The other half: a stage that writes prose for a person keeps a short answer
// rather than failing. Only the stages that parse are checked, which is why the
// check is not inside completeHeavy and completeLight.
func TestATruncatedProseAnswerIsStillAnAnswer(t *testing.T) {
	model := newStubModel(t, map[string]stubReply{
		"submit_preflight": {Args: map[string]any{
			"mode": "agent", "intent": "observe", "skills": []string{},
		}},
		"plan": plan(step("process_list", "procs", nil)),
		"":     {Content: "two processes are runn", Cut: true},
	})
	a := agentOnStub(t, model, &countingTool{name: "process_list"})

	res, err := a.RunDAGSync(context.Background(), Trigger{
		Type: "chat_query", Data: json.RawMessage(`{"query":"what is running?"}`),
	})
	if err != nil {
		t.Fatalf("a short prose answer failed the run: %v", err)
	}
	if res == nil || res.Outcome == "" {
		t.Error("a cut prose answer should still reach the caller — short beats nothing")
	}
}

// ── when a run has to change course, or runs out of allowance ────────────────
//
// Every test above ends with the reflector saying conclude, so two branches of
// the scheduler never execute during testing: replan, where the executive is
// asked again and new steps are grafted mid-run, and the refusals that fire
// when a run has spent its allowance. Break either and every test above still
// passes.
//
// Both are reachable from this harness. The reflector's answer is scripted, and
// the run's limits are the caller's to set.

// A reflector that says replan gets the executive asked again, and what it
// plans is run.
//
// The growth path: a batch succeeded and revealed the next move. Nothing here
// failed — which is why no test reached it, and why the branch is easy to break
// without noticing.
func TestAReflectionThatReplansGrowsTheRun(t *testing.T) {
	first := &countingTool{name: "process_list"}
	second := &countingTool{name: "net_info"}
	model := newStubModel(t, map[string]stubReply{
		"submit_preflight": {Args: map[string]any{"mode": "agent", "intent": "observe"}},
	})
	// Two plans: the first is what the run starts with, the second is what the
	// executive answers when the replan asks it again.
	model.answerNth("plan",
		plan(step("process_list", "procs", nil)),
		plan(step("net_info", "ports", nil)),
	)
	model.answerNth("submit_decision",
		stubReply{Args: map[string]any{"decision": "replan", "summary": "found the process, now the ports", "next": "look at the ports"}},
		stubReply{Args: map[string]any{"decision": "conclude", "outcome": "done"}},
	)
	a := agentOnStub(t, model, first, second)

	if _, err := a.RunDAGSync(context.Background(), Trigger{
		Type: "chat_query", Data: json.RawMessage(`{"query":"what is running?"}`),
	}); err != nil {
		t.Fatalf("the run failed: %v (stages: %v)", err, model.functionsCalled())
	}

	if n := model.callsTo("plan"); n < 2 {
		t.Errorf("the executive was asked %d times; a replan asks it again", n)
	}
	if second.calls == 0 {
		t.Errorf("the step the replan planned never ran; stages called: %v", model.functionsCalled())
	}
}

// The run cannot grow for ever. Past the cap a replan concludes instead, on
// what it has.
func TestReplanningStopsAtItsCap(t *testing.T) {
	tool := &countingTool{name: "process_list"}
	model := newStubModel(t, map[string]stubReply{
		"submit_preflight": {Args: map[string]any{"mode": "agent", "intent": "observe"}},
		"plan":             plan(step("process_list", "procs", nil)),
		// Always replan: without a cap this never ends.
		"submit_decision": {Args: map[string]any{
			"decision": "replan", "summary": "again", "next": "and again", "outcome": "what I have",
		}},
	})
	a := agentOnStubWith(t, model, DAGConfig{
		DAGEnabled: true, MaxNodes: 40, MaxLLMCalls: 40, MaxReplans: 2,
	}, tool)

	res, err := a.RunDAGSync(context.Background(), Trigger{
		Type: "chat_query", Data: json.RawMessage(`{"query":"look"}`),
	})
	if err != nil {
		t.Fatalf("the run failed: %v (stages: %v)", err, model.functionsCalled())
	}
	if res == nil || res.Outcome == "" {
		t.Fatal("a run that hit the replan cap produced no outcome")
	}
	// Two replans, so the executive is asked at most three times: once to start
	// and once per replan. A run that never stopped growing would exceed this.
	if n := model.callsTo("plan"); n > 3 {
		t.Errorf("the executive was asked %d times with a cap of 2 replans", n)
	}
}

// A run that has spent its allowance stops rather than asking for more.
//
// The scheduler refuses to spawn and concludes on what it has. Nothing in the
// suite reached this, so a refusal that stopped refusing would have been
// invisible.
func TestARunThatSpendsItsAllowanceConcludesOnWhatItHas(t *testing.T) {
	tool := &countingTool{name: "process_list"}
	model := newStubModel(t, map[string]stubReply{
		"submit_preflight": {Args: map[string]any{"mode": "agent", "intent": "observe"}},
		"plan":             plan(step("process_list", "procs", nil)),
		"submit_decision": {Args: map[string]any{
			"decision": "replan", "summary": "more", "next": "more", "outcome": "what I have",
		}},
	})
	// Five model calls is route, classify, plan, reflect — and then one left,
	// which is not enough: the branch requires more than two remaining before
	// it will ask the executive again. Measured rather than chosen: at six the
	// replan proceeds and this run asks the executive twice.
	a := agentOnStubWith(t, model, DAGConfig{
		DAGEnabled: true, MaxNodes: 20, MaxLLMCalls: 5, MaxReplans: 5,
	}, tool)

	res, err := a.RunDAGSync(context.Background(), Trigger{
		Type: "chat_query", Data: json.RawMessage(`{"query":"look"}`),
	})
	if err != nil {
		t.Fatalf("the run failed: %v (stages: %v)", err, model.functionsCalled())
	}
	if res == nil || res.Outcome == "" {
		t.Fatal("a run that ran out of allowance produced no outcome")
	}
	if n := model.callsTo("plan"); n > 1 {
		t.Errorf("the executive was asked %d times by a run with no allowance to replan", n)
	}
}

// A tool's context carries the run it is part of.
//
// An application recording something a tool found — a row of its own, keyed to
// the work that produced it — has no other way to know which run that was. The
// caller's context does not carry it; the run stamps it on its own at the first
// line, and a tool receives that one.
func TestAToolSeesTheRunItIsPartOf(t *testing.T) {
	tool := &runAwareTool{}
	model := newStubModel(t, map[string]stubReply{
		"submit_preflight": {Args: map[string]any{"mode": "agent", "intent": "observe"}},
		"plan":             plan(step("process_list", "procs", nil)),
		"submit_decision":  {Args: map[string]any{"decision": "conclude", "outcome": "done"}},
	})
	a := agentOnStub(t, model, tool)

	res, err := a.RunDAGSync(context.Background(), Trigger{
		Type: "chat_query", ID: "ref-1", Data: json.RawMessage(`{"query":"look"}`),
	})
	if err != nil {
		t.Fatalf("the run failed: %v", err)
	}
	if tool.sawRun == "" {
		t.Fatal("a tool's context does not carry the run, so nothing it writes can name one")
	}
	if !strings.HasPrefix(tool.sawRun, "ref-1-") {
		t.Errorf("the tool saw %q, which does not belong to this trigger", tool.sawRun)
	}
	// And the caller is told the same run, so what it records afterwards agrees
	// with what the tools recorded during.
	if res.RunID != tool.sawRun {
		t.Errorf("the tool saw run %q and the caller was told %q", tool.sawRun, res.RunID)
	}
}

// runAwareTool records the run its context named.
type runAwareTool struct{ sawRun string }

func (r *runAwareTool) Name() string                { return "process_list" }
func (r *runAwareTool) Description() string         { return "for the end-to-end tests" }
func (r *runAwareTool) Impact(map[string]any) int   { return toolapi.ImpactObserve }
func (r *runAwareTool) Parameters() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (r *runAwareTool) Execute(ctx context.Context, _ map[string]any) (string, error) {
	r.sawRun = RunIDFrom(ctx)
	return toolapi.ToolOK("listing", "2 processes", map[string]any{"count": 2}).JSON(), nil
}
