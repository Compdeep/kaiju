package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// One test for the whole reference path, because the failure it guards against
// needed every stage to be right and was invisible in any one of them.
//
// A re-plan named an earlier step by its tag — the only form that survives a new
// plan, since positions restart. The parser could not represent a tag, so it
// discarded the reference instead of reporting it. Discarded, it was
// indistinguishable from prose, so no check that looks for references saw it, it
// stayed in the parameter, and a model was handed a placeholder as text. It read
// it as a data path and looked the value up under keys that do not exist.
//
// So this walks a reference from a plan to a running node and asserts the two
// things that were wrong: a tag resolves, and anything that does not resolve
// stops the step rather than travelling on as text.
func TestReferencePipeline_TagResolvesAndNothingUnresolvedTravels(t *testing.T) {
	g := NewGraph()
	producerID := g.AddNode(&Node{Type: NodeTool, Tag: "fetch_page", ToolName: "reader"})
	g.SetBody(producerID, toolMessageBody{msg: toolapi.ToolMessage{
		Type:   "page",
		Status: toolapi.StatusOK,
		Data:   json.RawMessage(`{"kept_at":"kept/doc.txt"}`),
	}})

	steps := []PlanStep{{Tool: "reader", Tag: "fetch_page"}, {Tool: "compute", Tag: "work"}}
	nodeIDs := []string{producerID, "n2"}

	// Stage 1 — the plan is finalised. A tag reference must become a node
	// reference, exactly as a positional one does.
	params := map[string]any{
		"by_tag":      "${step.fetch_page.kept_at}",
		"by_position": "${step.0.kept_at}",
		"in_prose":    "the file is at ${step.fetch_page.kept_at} — read it",
		"shell":       "echo ${HOME}",
	}
	rewriteStepTemplates(params, nodeIDs, "n2", steps, nil)

	if got := params["by_tag"].(string); !strings.HasPrefix(got, "${node."+producerID) {
		t.Fatalf("a tag must resolve to the node it names, got %q", got)
	}
	if got := params["by_position"].(string); !strings.HasPrefix(got, "${node."+producerID) {
		t.Fatalf("a position must still resolve, got %q", got)
	}
	if got := params["shell"].(string); got != "echo ${HOME}" {
		t.Fatalf("a shell expansion is not a step reference and must be left alone, got %q", got)
	}

	// Stage 2 — the node fires. Every reference must now carry a value.
	n := &Node{ID: "n2", Type: NodeCompute, Params: params}
	if err := substituteTemplates(n, g, nil); err != nil {
		t.Fatalf("every reference named a step that ran: %v", err)
	}
	if got := n.Params["by_tag"]; got != "kept/doc.txt" {
		t.Fatalf("the tag reference must carry the value, got %v", got)
	}
	if got := n.Params["in_prose"].(string); !strings.Contains(got, "kept/doc.txt") || strings.Contains(got, "${step.") {
		t.Fatalf("a reference inside prose must be replaced too, got %q", got)
	}
	if got := n.Params["shell"]; got != "echo ${HOME}" {
		t.Fatalf("a shell expansion must survive substitution, got %v", got)
	}
}

// The other half: a reference nobody can resolve stops the step. It used to stay
// in the parameter and be handed onward as text.
func TestReferencePipeline_AnUnresolvedReferenceStopsTheStep(t *testing.T) {
	g := NewGraph()
	steps := []PlanStep{{Tool: "compute", Tag: "work"}}

	params := map[string]any{"goal": "read ${step.never_ran.kept_at} and count"}
	rewriteStepTemplates(params, []string{"n1"}, "n1", steps, nil)

	n := &Node{ID: "n1", Type: NodeCompute, Params: params}
	err := substituteTemplates(n, g, nil)
	if err == nil {
		t.Fatal("a reference naming no step must stop the node, not travel on as text")
	}
	if !strings.Contains(err.Error(), "never_ran") {
		t.Fatalf("the error must name what could not be resolved, got %v", err)
	}
}
