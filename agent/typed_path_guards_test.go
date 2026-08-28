package agent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// Guards for the invariants the typed path rests on.
//
// Each of these held before it was written and each was broken at some point
// today, silently, with the suite green. They read the package's own source
// rather than trusting a list, for the reason grafted_test.go gives: a
// hand-kept list is a comment with no test.

// Every stage that reasons about a run is given what the run produced.
//
// Five stages read a graph and call a model. Each was handed a rendering of
// the results and none was handed the results, so a value that survived to the
// node died at the stage — which is how a path that was correct and on disk
// reached the planner as a placeholder somebody had retyped.
//
// A stage dropped from this list is a stage that silently goes back to reading
// prose. Nothing else would notice: the run still works, it just stops knowing
// things.
func TestGuard_EveryReasoningStageIsGivenTheArcs(t *testing.T) {
	stages := map[string]string{
		"executive.go":    "the planner — it writes params, so it needs values",
		"reflection.go":   "decides whether to continue, replan or conclude",
		"rca.go":          "Holmes — looks for a cause among the failures",
		"microplanner.go": "the debugger — writes a fix plan, so it names values",
		"observer.go":     "decides whether a completed step is worth acting on",
	}
	for file, why := range stages {
		src := readSource(t, file)
		if !strings.Contains(src, "graph.Arcs()") {
			t.Errorf("%s no longer carries the run's results (%s).\n"+
				"It builds its messages some other way, so it reads prose about "+
				"the steps instead of the steps.", file, why)
		}
	}
}

// A reader that returns steps in an order walks the order, not the map.
//
// Nodes live in a map and Go randomises its iteration deliberately. Every
// reader that walked it returned a different sequence each run: steps launched
// in one order and the trace showed another, and neither was the order the plan
// was written in.
//
// The map is still right for a count or an existence check, where order means
// nothing. It is never right for something that returns a list.
func TestGuard_OrderedReadersWalkTheOrder(t *testing.T) {
	ordered := map[string]bool{
		"ReadyNodes": true, "Snapshot": true, "StepResults": true,
		"PendingNodes": true, "FailedNodes": true, "SkippedNodes": true,
		"ResolvedByType": true, "resolvedResultNodes": true,
		"ResolvedResultsSoFar": true,
	}

	f, err := parser.ParseFile(token.NewFileSet(), "dag.go", readSource(t, "dag.go"), 0)
	if err != nil {
		t.Fatalf("parsing dag.go: %v", err)
	}
	seen := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || !ordered[fn.Name.Name] {
			return true
		}
		seen[fn.Name.Name] = true
		ast.Inspect(fn, func(inner ast.Node) bool {
			rng, ok := inner.(*ast.RangeStmt)
			if !ok {
				return true
			}
			if sel, ok := rng.X.(*ast.SelectorExpr); ok && sel.Sel.Name == "nodes" {
				t.Errorf("%s ranges over the node MAP, so what it returns is in a "+
					"different order every run. Range g.order and look each id up.",
					fn.Name.Name)
			}
			return true
		})
		return true
	})
	for name := range ordered {
		if !seen[name] {
			t.Errorf("%s is gone from dag.go — if it was renamed, rename it here too, "+
				"and if it was deleted, delete it here", name)
		}
	}
}

// Every reference-parsing path accepts a step's NAME, not only its position.
//
// Four places read the first segment of a reference. Each used strconv.Atoi
// with the error discarded, so a name became step 0 — a reference meant for
// the fetch read the first step's output, and where the step writing it WAS
// step 0 it referenced itself. Three were fixed at different times; the fourth
// was found by a run looping on the same fetch.
//
// Behavioural rather than structural: what matters is that a name resolves,
// however the code gets there.
func TestGuard_EveryReferencePathResolvesAName(t *testing.T) {
	steps := []PlanStep{
		{Tool: "file_read", Tag: "read_first"},
		{Tool: "file_read", Tag: "read_second"},
	}

	t.Run("the resolver itself", func(t *testing.T) {
		if idx, ok := stepIndexFor("read_second", steps); !ok || idx != 1 {
			t.Errorf("a name resolved to %d (ok=%v), want 1 — a name became a position", idx, ok)
		}
	})

	t.Run("validation", func(t *testing.T) {
		plan := append(append([]PlanStep{}, steps...), PlanStep{
			Tool: "compute", Tag: "use_it",
			Params: map[string]any{"in": "${step.read_second.content}"},
		})
		if errs := validatePlanReferences(plan, nil); len(errs) != 0 {
			t.Errorf("a reference by name was reported as a fault: %v", errs)
		}
	})

	t.Run("a declared reference", func(t *testing.T) {
		plan := append(append([]PlanStep{}, steps...), PlanStep{
			Tool: "compute", Tag: "use_it",
			Params: map[string]any{"in": map[string]any{"step": "read_second", "field": "content"}},
		})
		n := &Node{ID: "n3", Params: plan[2].Params}
		deps := resolveDeclaredRefs(n, []string{"n1", "n2", "n3"}, plan, nil)
		if len(deps) != 1 || deps[0] != "n2" {
			t.Errorf("a declared reference by name wired %v, want [n2]", deps)
		}
	})

	t.Run("a name that matches nothing is a fault, not step 0", func(t *testing.T) {
		plan := append(append([]PlanStep{}, steps...), PlanStep{
			Tool: "compute", Tag: "use_it",
			Params: map[string]any{"in": "${step.no_such_step.content}"},
		})
		if errs := validatePlanReferences(plan, nil); len(errs) == 0 {
			t.Error("a name matching nothing was accepted — it silently became step 0")
		}
	})
}

// A tool message pairs with a call declared immediately before it.
//
// The protocol's rule. A tool_call_id with nothing declaring it, or a message
// between the call and its reply, is rejected by strict providers — and the
// whole request fails rather than the message being dropped.
func TestGuard_MessagesAreAlwaysLegal(t *testing.T) {
	arcs := [][]StepResult{
		{{NodeID: "n1", Tool: "file_read", Name: "a", Params: map[string]any{"p": 1}}},
		{},
		{{NodeID: "n2", Tool: "bash", Name: "b", Err: "boom"},
			{NodeID: "n3", Tool: "bash", Name: "c"}},
	}
	msgs := BuildMessagesWithResults("sys", "obj", nil, arcs)

	declared := map[string]bool{}
	prev := ""
	for _, m := range msgs {
		if m.Role == "assistant" {
			for _, tc := range m.ToolCalls {
				if tc.ID == "" || tc.Function.Name == "" {
					t.Errorf("a declared call is missing an id or a name: %+v", tc)
				}
				declared[tc.ID] = true
			}
		}
		if m.Role == "tool" {
			if !declared[m.ToolCallID] {
				t.Errorf("tool message %q pairs with no declared call", m.ToolCallID)
			}
			if prev != "assistant" && prev != "tool" {
				t.Errorf("a %q message sits between a call and its reply", prev)
			}
		}
		prev = m.Role
	}
}
