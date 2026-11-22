package agent

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// A result's text that a later step read by reference is replayed once.
//
// On run 10:00 2026-11-22 a compute step named three research pages by
// reference. The dispatcher pasted the pages into its parameters before it ran,
// and the pasted parameters were replayed: 65k characters in the compute call's
// arguments beside the same pages as the three steps' own results, in every
// planner, reflector and aggregator call after it. The replay must carry the
// page once, as the research step's result, and the compute call as the planner
// wrote it.
func TestReplayWritesAPastedResultAsItsReference(t *testing.T) {
	page := strings.Repeat("Semiconductors Valuation Multiples 2026. ", 40)

	g := NewGraph()
	budget := NewBudget(100, 100, 100, 100, time.Minute)
	steps := []PlanStep{
		{Tool: "web_research", Tag: "fetch_page", Params: map[string]any{"focus": "the page"}},
		{Tool: "compute", Tag: "valuation", Params: map[string]any{
			"goal":    "compare the multiples",
			"context": []any{"price=130.01", "text=${step.fetch_page.content}"},
		}},
	}
	nodes, err := planStepsToNodes(steps, g, budget, nil)
	if err != nil {
		t.Fatalf("planStepsToNodes: %v", err)
	}
	research, compute := nodes[0], nodes[1]
	g.SetBody(research.ID, toolMessageBody{msg: toolapi.ToolOK("page", page, nil)})

	// The step fires on the pasted text, as before.
	if err := substituteTemplates(compute, g, nil); err != nil {
		t.Fatalf("substituteTemplates: %v", err)
	}
	ctx, _ := compute.Params["context"].([]any)
	if len(ctx) != 2 || !strings.Contains(ctx[1].(string), page) {
		t.Fatalf("the tool must run on the pasted text, got %v", compute.Params["context"])
	}
	g.SetBody(compute.ID, toolMessageBody{msg: toolapi.ToolOK("result", "ratio 1.04", nil)})

	msgs := BuildMessagesWithResults("sys", "user", nil, g.Arcs())
	var computeArgs string
	pages := 0
	for _, m := range msgs {
		pages += strings.Count(m.Content, page)
		for _, c := range m.ToolCalls {
			pages += strings.Count(c.Function.Arguments, page)
			if c.Function.Name == "compute" {
				computeArgs = c.Function.Arguments
			}
		}
	}
	if computeArgs == "" {
		t.Fatal("the compute call was not replayed")
	}
	if !strings.Contains(computeArgs, "text=${step.fetch_page.content}") {
		t.Errorf("the replayed call must carry the reference the planner wrote, got %s", computeArgs)
	}
	if !strings.Contains(computeArgs, "price=130.01") {
		t.Errorf("a value the planner typed must stay, got %s", computeArgs)
	}
	if pages != 1 {
		t.Errorf("the page should appear once, as the research step's result; it appears %d times", pages)
	}
	// The node's parameters are the tool's and are not changed by the replay.
	if !strings.Contains(ctx[1].(string), page) {
		t.Errorf("building the replay changed the node's parameters")
	}
}

// A short pasted value stays pasted: a url or a path reads better as itself,
// and a reference saves nothing.
func TestReplayKeepsAShortPastedValue(t *testing.T) {
	arcs := [][]StepResult{
		{{NodeID: "n1", Tool: "web_search", Name: "find", Content: "https://example.com/report.pdf"}},
		{{NodeID: "n2", Tool: "web_fetch", Name: "read", Params: map[string]any{"url": "https://example.com/report.pdf"}, Content: "the report"}},
	}
	for _, m := range BuildMessagesWithResults("sys", "obj", nil, arcs) {
		for _, c := range m.ToolCalls {
			if c.Function.Name == "web_fetch" && !strings.Contains(c.Function.Arguments, "https://example.com/report.pdf") {
				t.Errorf("a short value must stay pasted, got %s", c.Function.Arguments)
			}
		}
	}
}

// A payload field read by reference is written as the reference to that field.
func TestReplayWritesAPastedFieldAsItsReference(t *testing.T) {
	body := strings.Repeat("article text ", 30)
	arcs := [][]StepResult{
		{{NodeID: "n1", Tool: "reader", Name: "fetch_page", Payload: []byte(`{"title":"T","body":` + quote(body) + `}`)}},
		{{NodeID: "n2", Tool: "compute", Name: "count", Params: map[string]any{"context": []any{"text=" + body}}, Content: "42 words"}},
	}
	for _, m := range BuildMessagesWithResults("sys", "obj", nil, arcs) {
		for _, c := range m.ToolCalls {
			if c.Function.Name != "compute" {
				continue
			}
			if strings.Contains(c.Function.Arguments, body) || !strings.Contains(c.Function.Arguments, "text=${step.fetch_page.body}") {
				t.Errorf("a pasted field must be written as its reference, got %s", c.Function.Arguments)
			}
		}
	}
}

// A step is not shown reading its own result: a tool that echoes its input
// would otherwise be replayed as referencing itself.
func TestReplayDoesNotReferenceAStepFromItsOwnArguments(t *testing.T) {
	text := strings.Repeat("echoed ", 40)
	arcs := [][]StepResult{
		{{NodeID: "n1", Tool: "bash", Name: "say", Params: map[string]any{"command": "echo " + text}, Content: text}},
	}
	for _, m := range BuildMessagesWithResults("sys", "obj", nil, arcs) {
		for _, c := range m.ToolCalls {
			if strings.Contains(c.Function.Arguments, "${step.say") {
				t.Errorf("a step must not reference itself, got %s", c.Function.Arguments)
			}
		}
	}
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
