package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Compdeep/kaiju/agent/llm"
	"github.com/Compdeep/kaiju/agent/tools"
)

// The preflight pipeline had no tests. These come from Enbarr's copy of the
// engine, where they existed, and are pointed at this pipeline — which routes
// chat with a cheap call before paying for plan preparation, and skips routing
// altogether when nobody is watching.
//
// What they cover is the pipeline AROUND classification: the routing decision,
// the autonomous override, the short-circuit, and the refinement. Classification
// itself is a model call and is not what these are about, so it is stood in for.

// fixedClassification makes both classification calls return the same answer.
func fixedClassification(pf *PreflightResult) func(string, []llm.Message) *PreflightResult {
	return func(string, []llm.Message) *PreflightResult {
		out := *pf
		out.Skills = append([]string(nil), pf.Skills...)
		return &out
	}
}

func chatTrigger(query string) Trigger {
	data, _ := json.Marshal(map[string]string{"query": query})
	return Trigger{Type: "chat_query", Data: data}
}

// drivePreflight runs the real pipeline as far as the planner and reports what
// happened: the graph it built, the conversational reply when it short-circuited,
// and the trigger afterwards, whose Target a refinement may have settled.
func drivePreflight(t *testing.T, a *Agent, trigger Trigger) (*Graph, string, *Trigger) {
	t.Helper()
	a.cfg.ClassifierEnabled = true

	// The pipeline lives inside runPlanAndSchedule, which goes on to plan. There
	// is nothing to plan with here, so the planner is pointed at a model that
	// refuses: it then fails with an ordinary error instead of dereferencing a
	// nil client, and everything before it has already happened.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no planning in this test", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	a.llm = llm.NewClient(srv.URL, "k", "test")
	a.executor = a.llm
	if a.registry == nil {
		a.registry = tools.NewRegistry()
	}
	if a.intentRegistry == nil {
		a.intentRegistry = NewIntentRegistry()
	}

	graph, _, cleanup := a.setupDAGPipeline(trigger)
	t.Cleanup(cleanup)

	b := NewBudget(100, 100, 100, 100, time.Minute)
	_, err := a.runPlanAndSchedule(context.Background(), trigger, graph, b)

	var conv *ExecutiveConversationalError
	if err != nil && asConversational(err, &conv) {
		return graph, conv.Text, &trigger
	}
	return graph, "", &trigger
}

// asConversational reports whether err is the conversational short-circuit.
func asConversational(err error, out **ExecutiveConversationalError) bool {
	if c, ok := err.(*ExecutiveConversationalError); ok {
		*out = c
		return true
	}
	return false
}

// A chat query never reaches the planner. Without this the engine plans and runs
// tools for "hello", which costs a plan and every step in it.
func TestPipelineChatShortCircuits(t *testing.T) {
	a := &Agent{classifyStub: fixedClassification(&PreflightResult{Mode: "chat"})}

	graph, reply, _ := drivePreflight(t, a, chatTrigger("hello"))

	if reply != "" {
		t.Errorf("chat should short-circuit with no text of its own, got %q", reply)
	}
	if graph.Preflight == nil || graph.Preflight.Mode != "chat" {
		t.Errorf("Preflight.Mode = %v, want chat", graph.Preflight)
	}
}

// A run nobody is watching never chats — there is no one to chat to. It skips
// routing entirely, so a classification of "chat" cannot reach it.
func TestPipelineAutonomousNeverChats(t *testing.T) {
	a := &Agent{classifyStub: fixedClassification(&PreflightResult{Mode: "chat"})}
	a.cfg.ExecutionMode = "autonomous"

	graph, reply, _ := drivePreflight(t, a, chatTrigger("anything"))

	if reply != "" {
		t.Errorf("an autonomous run short-circuited: %q", reply)
	}
	if graph.Preflight == nil || graph.Preflight.Mode != "agent" {
		t.Errorf("Preflight.Mode = %v, want agent", graph.Preflight)
	}
}

// Note on one property not covered here: a refinement may also settle which
// machine a run is about, by setting Target on the trigger it is handed.
// runPlanAndSchedule takes the trigger by value and passes the address of its
// own copy, so the mutation reaches the planner but not the caller — which makes
// it a scheduler-level property, not a pipeline one. The behaviour that matters,
// deciding which machine a request means, is covered where that refinement lives.

// A refinement may reply instead of planning, when the request cannot be acted
// on as written. The reply is the answer and the run ends.
func TestPipelineRefinementCanReplyInsteadOfPlanning(t *testing.T) {
	const question = "That could be several machines: db-2, db-5. Which one?"
	a := &Agent{
		classifyStub: fixedClassification(&PreflightResult{Mode: "agent"}),
		refine: func(context.Context, *PreflightResult, *Trigger) (*PreflightResult, string, error) {
			return nil, question, nil
		},
	}

	_, reply, _ := drivePreflight(t, a, chatTrigger("why is powershell running"))

	if reply != question {
		t.Errorf("reply = %q, want the refinement's question", reply)
	}
}

// A refinement that changes the skills changes which guidance the run gets.
// Without this the refinement runs and its answer is discarded.
func TestPipelineRefinementCanChangeTheSkills(t *testing.T) {
	a := &Agent{
		classifyStub: fixedClassification(&PreflightResult{Mode: "agent", Skills: []string{"general_reasoning"}}),
		refine: func(_ context.Context, pf *PreflightResult, _ *Trigger) (*PreflightResult, string, error) {
			out := *pf
			out.Skills = []string{"system_operations"}
			return &out, "", nil
		},
	}

	graph, _, _ := drivePreflight(t, a, chatTrigger("restart the collector"))

	if len(graph.ActiveCards) != 1 || graph.ActiveCards[0] != "system_operations" {
		t.Errorf("ActiveCards = %v, want the refinement's choice", graph.ActiveCards)
	}
}

// With the classifier switched off the pipeline does nothing at all: no
// classification, no short-circuit, no refinement, straight to the planner.
func TestPipelineDoesNothingWhenTheClassifierIsOff(t *testing.T) {
	a := &Agent{classifyStub: fixedClassification(&PreflightResult{Mode: "chat"})}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no planning in this test", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	a.llm = llm.NewClient(srv.URL, "k", "test")
	a.executor = a.llm
	a.registry = tools.NewRegistry()
	a.intentRegistry = NewIntentRegistry()
	a.cfg.ClassifierEnabled = false

	graph, _, cleanup := a.setupDAGPipeline(chatTrigger("hello"))
	t.Cleanup(cleanup)
	_, _ = a.runPlanAndSchedule(context.Background(), chatTrigger("hello"), graph, NewBudget(100, 100, 100, 100, time.Minute))

	if graph.Preflight != nil {
		t.Errorf("Preflight = %v, want nothing when the classifier is off", graph.Preflight)
	}
}

// chatQueryOf pulls the query text out of a chat trigger's payload.
func chatQueryOf(t Trigger) string {
	var d map[string]string
	_ = json.Unmarshal(t.Data, &d)
	return strings.ToLower(d["query"])
}
