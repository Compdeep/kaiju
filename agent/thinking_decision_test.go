package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/llm"
)

// reasoningAsked is what one stage's request said about thinking: "on", "off",
// or "" when it said nothing.
func reasoningAsked(t *testing.T, m *stubModel, fn string) string {
	t.Helper()
	calls := m.requestsTo(fn)
	if len(calls) == 0 {
		t.Fatalf("%s was never called (stages: %v)", fn, m.functionsCalled())
	}
	return calls[0].Reasoning
}

// Not every conversation needs reasoning, and the turn that decides is the one
// that reads the message first.
//
// A model that reasons by default does it on every turn, so a greeting costs
// the same wait as a comparison. It cannot be configured — it is a property of
// the message — so the router answers it beside the question it already
// answers, and the chat lane sends it.
func TestAChatTurnTheRouterSaidNeedsNoThinkingAsksForNone(t *testing.T) {
	model := newStubModel(t, map[string]stubReply{
		"recall": {Args: map[string]any{"lacking_context": []string{}, "think": false}},
	})
	a := agentOnStub(t, model)

	if _, err := a.Chat(context.Background(), ChatTurn{Query: "morning"}); err != nil {
		t.Fatalf("the turn failed: %v", err)
	}
	if got := reasoningAsked(t, model, ""); got != "off" {
		t.Errorf("the answering call asked %q for thinking, want off", got)
	}
}

// And a turn that needs working out asks for it.
func TestAChatTurnThatNeedsWorkingOutAsksForThinking(t *testing.T) {
	model := newStubModel(t, map[string]stubReply{
		"recall": {Args: map[string]any{"lacking_context": []string{}, "think": true}},
	})
	a := agentOnStub(t, model)

	if _, err := a.Chat(context.Background(), ChatTurn{
		Query: "which of these three is cheapest over five years?",
	}); err != nil {
		t.Fatalf("the turn failed: %v", err)
	}
	if got := reasoningAsked(t, model, ""); got != "on" {
		t.Errorf("the answering call asked %q for thinking, want on", got)
	}
}

// A decision nobody made leaves the model exactly as it was.
//
// The deciding call can fail, and it falls back to chat. Reading that failure
// as "do not think" would let a stage that tripped quietly change how every
// answer after it is written.
func TestNoDecisionLeavesTheModelAlone(t *testing.T) {
	model := newStubModel(t, map[string]stubReply{
		"recall": {Args: map[string]any{"lacking_context": []string{}}}, // no think field
	})
	a := agentOnStub(t, model)

	if _, err := a.Chat(context.Background(), ChatTurn{Query: "morning"}); err != nil {
		t.Fatalf("the turn failed: %v", err)
	}
	if got := reasoningAsked(t, model, ""); got != "" {
		t.Errorf("the answering call asked %q for thinking with nothing decided", got)
	}
}

// A plan is always reasoned about.
//
// Every other lane takes the model's own default, and on current models that is
// thinking — but it is a default rather than a guarantee, and this is the one
// call where the difference between a good plan and a bad one is the thinking.
// What bounds it is the deadline and the budget, not the absence of the ask.
func TestThePlannerAlwaysAsksToThink(t *testing.T) {
	model := newStubModel(t, map[string]stubReply{
		"submit_preflight": {Args: map[string]any{
			"mode": "agent", "intent": "observe", "skills": []string{},
		}},
		"plan": plan(step("process_list", "procs", nil)),
	})
	a := agentOnStub(t, model, &countingTool{name: "process_list"})

	if _, err := a.RunDAGSync(context.Background(), Trigger{
		Type: "chat_query", Data: json.RawMessage(`{"query":"what is running?"}`),
	}); err != nil {
		t.Fatalf("the run failed: %v", err)
	}
	if got := reasoningAsked(t, model, "plan"); got != "on" {
		t.Errorf("the planner asked %q for thinking, want on", got)
	}
}

// The two small schemas ask it, and the reply budget has room for the answer.
//
// A closed schema makes every property required and writes them in the order Go
// sorts keys, which puts "think" last in both — and a field written last is the
// field a cut reply loses. The bound on the words is what leaves room for it.
func TestBothDecidingSchemasAskAndFitTheirBudget(t *testing.T) {
	for name, def := range map[string]llm.ToolDef{
		"route":  routeSchema(),
		"recall": recallSchema(),
	} {
		body := string(def.Function.Parameters)
		if !strings.Contains(body, `"think"`) {
			t.Errorf("%s does not ask whether the turn needs thinking", name)
		}
		if !strings.Contains(body, `"maxItems"`) || !strings.Contains(body, `"maxLength"`) {
			t.Errorf("%s leaves the words unbounded, so they can spend the budget "+
				"before the decision after them is written", name)
		}
	}
	// Four words of forty characters, plus the mode and the boolean, in tokens:
	// roughly a quarter of the budget. The margin is the point.
	if routeReplyBudget < 128 {
		t.Errorf("the reply budget is %d, too small for the fields the schemas now ask for", routeReplyBudget)
	}
}
