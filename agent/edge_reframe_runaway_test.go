package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/llm"
	"github.com/Compdeep/kaiju/agent/toolapi"
)

// An edge whose reply reached its cap passes on the material, not the reply.
// On run f8e43d3a (2026-11-16) the plan edge looped for 8,192 tokens and every
// one of them went into the planner's prompt.
func TestReframe_ACappedReplyIsNotPassedOn(t *testing.T) {
	loop := "One last check on the PROPOSED NEXT MOVE section. So this is correct."
	model := newStubModel(t, map[string]stubReply{"": {Content: loop, Cut: true}})
	a := agentOnStub(t, model)
	g := NewGraph()
	withStep(g, "look", "web_search", toolapi.ToolEmpty("search", "no results"))

	block := a.EdgeReFrame(context.Background(), g, "find the advisory", ReframeToPlanner)
	if strings.Contains(block, "One last check") {
		t.Errorf("the capped reply was passed on:\n%s", block)
	}
	if !strings.Contains(block, "look (web_search): returned nothing") {
		t.Errorf("the material was not passed on instead:\n%s", block)
	}
}

// A reply that finished is still passed on as written.
func TestReframe_AFinishedReplyIsPassedOn(t *testing.T) {
	model := newStubModel(t, map[string]stubReply{"": {Content: "WHAT REMAINS: the advisory."}})
	a := agentOnStub(t, model)
	g := NewGraph()
	withStep(g, "look", "web_search", toolapi.ToolEmpty("search", "no results"))

	if block := a.EdgeReFrame(context.Background(), g, "find the advisory", ReframeToPlanner); !strings.Contains(block, "WHAT REMAINS: the advisory.") {
		t.Errorf("the reply was not passed on:\n%s", block)
	}
}

// An edge that does not reason, on a model the catalog says can stop, is given
// room for the framing alone. The reflector's edge, which reasons, and a model
// that cannot stop keep the room for reasoning.
func TestEdgeReplyCap(t *testing.T) {
	const window = 262144
	stops := edgeCapAgent(window, true)
	stops.executor.Catalog(func(string) (llm.ModelFacts, bool) {
		return llm.ModelFacts{ContextTokens: window, Thinking: llm.Thinking{Default: true, Optional: true}}, true
	})
	if got := stops.edgeReplyCap(context.Background(), ReframeToPlanner); got != edgeWriteCap {
		t.Errorf("plan edge on a model that can stop: %d, want %d", got, edgeWriteCap)
	}
	if got := stops.edgeReplyCap(context.Background(), ReframeToAnswer); got != edgeWriteCap {
		t.Errorf("answer edge on a model that can stop: %d, want %d", got, edgeWriteCap)
	}
	if got := stops.edgeReplyCap(context.Background(), ReframeToReflector); got != 8192 {
		t.Errorf("reflector edge, which reasons: %d, want 8192", got)
	}

	cannot := edgeCapAgent(window, true)
	cannot.executor.Catalog(func(string) (llm.ModelFacts, bool) {
		return llm.ModelFacts{ContextTokens: window, Thinking: llm.Thinking{Default: true}}, true
	})
	if got := cannot.edgeReplyCap(context.Background(), ReframeToPlanner); got != 8192 {
		t.Errorf("plan edge on a model that cannot stop: %d, want 8192", got)
	}

	if got := (&Agent{}).edgeReplyCap(context.Background(), ReframeToPlanner); got != replyEdgeBudget.Base {
		t.Errorf("no catalog: %d, want the base %d", got, replyEdgeBudget.Base)
	}
}

// The edge is sent the material with the steps' outputs in it, and no tool
// messages: the stage it feeds has those itself. On run 798fce9d (2026-11-16)
// the tool messages were 77% of each reframe call.
func TestReframe_TheOutputsComeInTheMaterialNotAsToolMessages(t *testing.T) {
	model := newStubModel(t, map[string]stubReply{"": {Content: "WHAT REMAINS: nothing."}})
	a := agentOnStub(t, model)
	g := NewGraph()
	g.Context = NewContextGate(g, &Trigger{}, a)
	withStep(g, "titan_vs_saturn", "web_fetch", toolapi.ToolOK("page", "RG= 1.186913501319864E+06 km", map[string]any{"url": "https://ssd.jpl.nasa.gov/api/horizons.api"}))
	// What a stage holding the tool messages is passed when no reframe is
	// written names the step without its output.
	if m := a.reframeMaterial(context.Background(), g, "the tide on Titan", ReframeToPlanner); strings.Contains(m, "RG= 1.186913501319864E+06") {
		t.Errorf("the pass-through text repeats an output the tool messages hold:\n%s", m)
	}
	a.EdgeReFrame(context.Background(), g, "the tide on Titan", ReframeToPlanner)

	model.mu.Lock()
	defer model.mu.Unlock()
	if len(model.calls) != 1 {
		t.Fatalf("want one call to the edge's model, got %d", len(model.calls))
	}
	call := model.calls[0]
	for _, m := range call.Messages {
		if m.Role == "tool" || len(m.ToolCalls) > 0 {
			t.Errorf("the edge was sent a tool message: %+v", m)
		}
	}
	if !strings.Contains(call.User, "RG= 1.186913501319864E+06") {
		t.Errorf("the step's output is not in the material:\n%s", call.User)
	}
}
