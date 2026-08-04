package agent

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// fakeLookup returns a TemplateLookup that resolves step indices and node IDs from
// the two maps provided. Values are stored as parsed Go values, mirroring
// how the dispatcher would feed parsed JSON results into the resolver.
func fakeLookup(byStep map[int]any, byNode map[string]any) TemplateLookup {
	return func(ref TemplateRef) (any, bool) {
		if ref.Kind == "node" {
			v, ok := byNode[ref.NodeID]
			return v, ok
		}
		v, ok := byStep[ref.Index]
		return v, ok
	}
}

// ─── FindRefs ────────────────────────────────────────────────────────────────

func TestFindRefs_Empty(t *testing.T) {
	if got := FindRefs(map[string]any{"a": "b", "n": 1}); len(got) != 0 {
		t.Errorf("expected no refs, got %v", got)
	}
}

func TestFindRefs_StringLeaf(t *testing.T) {
	refs := FindRefs(map[string]any{"url": "${step.0.results.0.url}"})
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref, got %d", len(refs))
	}
	r := refs[0]
	if r.Kind != "step" || r.Index != 0 {
		t.Errorf("kind/index mismatch: %+v", r)
	}
	if !reflect.DeepEqual(r.Path, []string{"results", "0", "url"}) {
		t.Errorf("path mismatch: %v", r.Path)
	}
}

func TestFindRefs_NodeKind(t *testing.T) {
	refs := FindRefs(map[string]any{"x": "${node.12D3KooWShkP.field}"})
	if len(refs) != 1 || refs[0].Kind != "node" {
		t.Fatalf("expected one node ref, got %+v", refs)
	}
	if refs[0].NodeID != "12D3KooWShkP" {
		t.Errorf("node id mismatch: %q", refs[0].NodeID)
	}
}

func TestFindRefs_NestedAndArray(t *testing.T) {
	v := map[string]any{
		"outer": map[string]any{
			"items": []any{
				"static",
				"${step.1.host}",
				map[string]any{"deeper": "${step.2}"},
			},
		},
	}
	refs := FindRefs(v)
	if len(refs) != 2 {
		t.Fatalf("expected 2 refs, got %d: %+v", len(refs), refs)
	}
}

func TestFindRefs_MidStringMultiple(t *testing.T) {
	refs := FindRefs("yt-dlp -o '${step.0.title}' '${step.0.url}'")
	if len(refs) != 2 {
		t.Fatalf("expected 2 refs from mid-string, got %d", len(refs))
	}
}

func TestFindRefs_RejectsMalformed(t *testing.T) {
	cases := []string{
		"${step.}",         // empty index
		"${step.abc}",      // non-numeric step index
		"${other.5.field}", // unknown kind
		"$step.0.field",    // missing braces
	}
	for _, c := range cases {
		if got := FindRefs(c); len(got) != 0 {
			t.Errorf("%q should produce no refs, got %v", c, got)
		}
	}
}

func TestFindRefs_DedupNotPerformed(t *testing.T) {
	// Two refs to the same step should both appear — callers (like the
	// auto-derive pass) decide whether to dedup.
	refs := FindRefs("${step.0.a} and ${step.0.b}")
	if len(refs) != 2 {
		t.Errorf("expected both refs to be returned, got %d", len(refs))
	}
}

// ─── ResolveTemplates ────────────────────────────────────────────────────────

func TestResolve_PassesThroughNonStrings(t *testing.T) {
	in := map[string]any{"n": 42, "b": true, "s": "literal"}
	out, err := ResolveTemplates(in, fakeLookup(nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(out, in) {
		t.Errorf("non-template values changed: %v -> %v", in, out)
	}
}

func TestResolve_BarePlaceholderPreservesObject(t *testing.T) {
	upstream := map[string]any{"a": 1, "b": "x"}
	in := map[string]any{"context": "${step.0}"}
	out, err := ResolveTemplates(in, fakeLookup(map[int]any{0: upstream}, nil))
	if err != nil {
		t.Fatal(err)
	}
	got := out.(map[string]any)["context"]
	if !reflect.DeepEqual(got, upstream) {
		t.Errorf("object should pass through, got %T %v", got, got)
	}
}

func TestResolve_BarePlaceholderPreservesArray(t *testing.T) {
	upstream := []any{"a", "b", "c"}
	in := map[string]any{"items": "${step.0.results}"}
	out, err := ResolveTemplates(in, fakeLookup(map[int]any{0: map[string]any{"results": upstream}}, nil))
	if err != nil {
		t.Fatal(err)
	}
	got := out.(map[string]any)["items"]
	if !reflect.DeepEqual(got, upstream) {
		t.Errorf("array should pass through, got %T %v", got, got)
	}
}

func TestResolve_BarePlaceholderPreservesScalarType(t *testing.T) {
	in := map[string]any{"n": "${step.0.count}"}
	out, err := ResolveTemplates(in, fakeLookup(map[int]any{0: map[string]any{"count": float64(7)}}, nil))
	if err != nil {
		t.Fatal(err)
	}
	got := out.(map[string]any)["n"]
	if f, ok := got.(float64); !ok || f != 7 {
		t.Errorf("expected float64(7), got %T %v", got, got)
	}
}

func TestResolve_MidStringInterpolates(t *testing.T) {
	in := "yt-dlp -o 'media/%(title)s.%(ext)s' '${step.0.url}'"
	out, err := ResolveTemplates(in, fakeLookup(map[int]any{
		0: map[string]any{"url": "https://example.com/x"},
	}, nil))
	if err != nil {
		t.Fatal(err)
	}
	want := "yt-dlp -o 'media/%(title)s.%(ext)s' 'https://example.com/x'"
	if out != want {
		t.Errorf("mid-string mismatch:\n  got:  %q\n  want: %q", out, want)
	}
}

func TestResolve_MidStringStringifiesNonString(t *testing.T) {
	in := "count=${step.0.n}"
	out, err := ResolveTemplates(in, fakeLookup(map[int]any{0: map[string]any{"n": float64(42)}}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if out != "count=42" {
		t.Errorf("expected count=42, got %q", out)
	}
}

func TestResolve_NestedParamsResolved(t *testing.T) {
	in := map[string]any{
		"outer": map[string]any{
			"items": []any{
				"static",
				"${step.0.label}",
				map[string]any{"deeper": "${step.1}"},
			},
		},
	}
	out, err := ResolveTemplates(in, fakeLookup(map[int]any{
		0: map[string]any{"label": "found"},
		1: "whole-result",
	}, nil))
	if err != nil {
		t.Fatal(err)
	}
	items := out.(map[string]any)["outer"].(map[string]any)["items"].([]any)
	if items[1] != "found" {
		t.Errorf("inner string ref not resolved: %v", items[1])
	}
	if items[2].(map[string]any)["deeper"] != "whole-result" {
		t.Errorf("deeper ref not resolved: %v", items[2])
	}
}

func TestResolve_NodeIDLookup(t *testing.T) {
	in := map[string]any{"x": "${node.peerA.field}"}
	out, err := ResolveTemplates(in, fakeLookup(nil, map[string]any{
		"peerA": map[string]any{"field": "value"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["x"] != "value" {
		t.Errorf("node lookup failed: %v", out)
	}
}

func TestResolve_ErrorOnUnresolvedBase(t *testing.T) {
	_, err := ResolveTemplates("${step.5.field}", fakeLookup(nil, nil))
	if err == nil || !strings.Contains(err.Error(), "unresolved") {
		t.Errorf("expected unresolved error, got %v", err)
	}
}

func TestResolve_ErrorOnMissingPath(t *testing.T) {
	_, err := ResolveTemplates("${step.0.missing}", fakeLookup(map[int]any{0: map[string]any{"x": 1}}, nil))
	if err == nil || !strings.Contains(err.Error(), "path") {
		t.Errorf("expected path error, got %v", err)
	}
}

func TestResolve_DoesNotMutateInput(t *testing.T) {
	in := map[string]any{"v": "${step.0}"}
	original := map[string]any{"v": "${step.0}"}
	_, err := ResolveTemplates(in, fakeLookup(map[int]any{0: "X"}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in, original) {
		t.Errorf("input was mutated: %v", in)
	}
}

// ─── ResolvePath ─────────────────────────────────────────────────────────────

func TestResolvePath_Empty(t *testing.T) {
	root := "value"
	v, ok := ResolvePath(root, nil)
	if !ok || v != "value" {
		t.Errorf("empty path should return root, got %v ok=%v", v, ok)
	}
}

func TestResolvePath_NestedMap(t *testing.T) {
	root := map[string]any{"a": map[string]any{"b": "x"}}
	v, ok := ResolvePath(root, []string{"a", "b"})
	if !ok || v != "x" {
		t.Errorf("nested map miss: %v ok=%v", v, ok)
	}
}

func TestResolvePath_ArrayIndex(t *testing.T) {
	root := map[string]any{"items": []any{"a", "b", "c"}}
	v, ok := ResolvePath(root, []string{"items", "1"})
	if !ok || v != "b" {
		t.Errorf("array index miss: %v ok=%v", v, ok)
	}
}

func TestResolvePath_OutOfRange(t *testing.T) {
	root := []any{"a"}
	if _, ok := ResolvePath(root, []string{"5"}); ok {
		t.Errorf("expected out-of-range miss")
	}
	if _, ok := ResolvePath(root, []string{"-1"}); ok {
		t.Errorf("expected negative-index miss")
	}
}

func TestResolvePath_TypeMismatch(t *testing.T) {
	root := map[string]any{"n": 5}
	if _, ok := ResolvePath(root, []string{"n", "field"}); ok {
		t.Errorf("expected type mismatch miss when descending into a scalar")
	}
}

// ─── TemplateRef.Key ─────────────────────────────────────────────────────────

func TestTemplateRefKey(t *testing.T) {
	step := TemplateRef{Kind: "step", Index: 7}
	if got := step.Key(); got != "step:7" {
		t.Errorf("step key: got %q want %q", got, "step:7")
	}
	node := TemplateRef{Kind: "node", NodeID: "abc"}
	if got := node.Key(); got != "node:abc" {
		t.Errorf("node key: got %q want %q", got, "node:abc")
	}
}

// Sanity check: Key dedup over a found ref set works as expected for the
// auto-derive pass (Phase 2 will use this pattern).
func TestFindRefs_KeyDedup(t *testing.T) {
	refs := FindRefs(map[string]any{
		"a": "${step.0.x}",
		"b": "${step.0.y}",
		"c": "${step.1}",
	})
	seen := map[string]struct{}{}
	for _, r := range refs {
		seen[r.Key()] = struct{}{}
	}
	if len(seen) != 2 {
		t.Errorf("expected 2 unique upstreams, got %d (%v)", len(seen), seen)
	}
}

// ─── RewriteStepIndicesToNodeIDs ─────────────────────────────────────────────

func TestRewrite_StepToNode(t *testing.T) {
	in := map[string]any{"url": "${step.0.results.0.url}"}
	out := RewriteParamsIndicesToNodeIDs(in, []string{"n1"})
	if got := out["url"]; got != "${node.n1.results.0.url}" {
		t.Errorf("expected ${node.n1.results.0.url}, got %v", got)
	}
}

func TestRewrite_NodeRefsLeftAlone(t *testing.T) {
	in := map[string]any{"x": "${node.peerA.field}"}
	out := RewriteParamsIndicesToNodeIDs(in, []string{"n1", "n2"})
	if out["x"] != "${node.peerA.field}" {
		t.Errorf("node refs should be untouched, got %v", out["x"])
	}
}

func TestRewrite_OutOfRangeUnchanged(t *testing.T) {
	in := map[string]any{"x": "${step.5.field}"}
	out := RewriteParamsIndicesToNodeIDs(in, []string{"n1"})
	if out["x"] != "${step.5.field}" {
		t.Errorf("out-of-range index should leave template unchanged, got %v", out["x"])
	}
}

func TestRewrite_EmptyNodeIDUnchanged(t *testing.T) {
	in := map[string]any{"x": "${step.1.field}"}
	out := RewriteParamsIndicesToNodeIDs(in, []string{"n1", ""})
	if out["x"] != "${step.1.field}" {
		t.Errorf("empty nodeID should leave template unchanged, got %v", out["x"])
	}
}

func TestRewrite_MidString(t *testing.T) {
	in := map[string]any{"cmd": "yt-dlp -o '${step.0.title}' '${step.0.url}'"}
	out := RewriteParamsIndicesToNodeIDs(in, []string{"n7"})
	want := "yt-dlp -o '${node.n7.title}' '${node.n7.url}'"
	if got := out["cmd"]; got != want {
		t.Errorf("\n  got:  %q\n  want: %q", got, want)
	}
}

func TestRewrite_NestedParams(t *testing.T) {
	in := map[string]any{
		"outer": map[string]any{
			"items": []any{"${step.0.a}", "${step.1.b}"},
		},
	}
	out := RewriteParamsIndicesToNodeIDs(in, []string{"n1", "n2"})
	items := out["outer"].(map[string]any)["items"].([]any)
	if items[0] != "${node.n1.a}" || items[1] != "${node.n2.b}" {
		t.Errorf("nested rewrite failed: %v", items)
	}
}

func TestRewrite_NilParams(t *testing.T) {
	if got := RewriteParamsIndicesToNodeIDs(nil, []string{"n1"}); got != nil {
		t.Errorf("nil params should return nil, got %v", got)
	}
}

func TestAutoDerive_AddsMissingDeps(t *testing.T) {
	steps := []PlanStep{
		{Tool: "search", Params: map[string]any{"q": "x"}},
		{Tool: "fetch", Params: map[string]any{"url": "${step.0.results.0.url}"}},
	}
	if err := AutoDeriveTemplateDeps(steps); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !containsInt(steps[1].DependsOn, 0) {
		t.Errorf("step 1 should depend on step 0, got %v", steps[1].DependsOn)
	}
}

func TestAutoDerive_DoesNotDuplicateDeps(t *testing.T) {
	steps := []PlanStep{
		{Tool: "search"},
		{Tool: "fetch", Params: map[string]any{"url": "${step.0.x}"}, DependsOn: []int{0}},
	}
	if err := AutoDeriveTemplateDeps(steps); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(steps[1].DependsOn) != 1 {
		t.Errorf("expected dep [0], got %v", steps[1].DependsOn)
	}
}

func TestAutoDerive_RejectsOutOfRange(t *testing.T) {
	steps := []PlanStep{
		{Tool: "fetch", Params: map[string]any{"url": "${step.5.x}"}},
	}
	err := AutoDeriveTemplateDeps(steps)
	if err == nil || !strings.Contains(err.Error(), "step 5") {
		t.Errorf("expected out-of-range error, got %v", err)
	}
}

func TestAutoDerive_RejectsSelfReference(t *testing.T) {
	steps := []PlanStep{
		{Tool: "loopy", Params: map[string]any{"v": "${step.0.x}"}},
	}
	err := AutoDeriveTemplateDeps(steps)
	if err == nil || !strings.Contains(err.Error(), "itself") {
		t.Errorf("expected self-reference error, got %v", err)
	}
}

func TestAutoDerive_IgnoresNodeRefs(t *testing.T) {
	steps := []PlanStep{
		{Tool: "remote", Params: map[string]any{"x": "${node.peerA.field}"}},
	}
	if err := AutoDeriveTemplateDeps(steps); err != nil {
		t.Fatalf("node refs should not error at plan time: %v", err)
	}
	if len(steps[0].DependsOn) != 0 {
		t.Errorf("node refs should not auto-derive deps, got %v", steps[0].DependsOn)
	}
}

func TestAutoDerive_NestedParams(t *testing.T) {
	steps := []PlanStep{
		{Tool: "a"},
		{Tool: "b"},
		{Tool: "agg", Params: map[string]any{
			"items": []any{
				"${step.0.label}",
				map[string]any{"deeper": "${step.1.value}"},
			},
		}},
	}
	if err := AutoDeriveTemplateDeps(steps); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !containsInt(steps[2].DependsOn, 0) || !containsInt(steps[2].DependsOn, 1) {
		t.Errorf("expected step 2 to depend on 0 and 1, got %v", steps[2].DependsOn)
	}
}

func TestAutoDerive_MultipleRefsSameStepDedupes(t *testing.T) {
	steps := []PlanStep{
		{Tool: "search"},
		{Tool: "fetch", Params: map[string]any{
			"a": "${step.0.x}",
			"b": "${step.0.y}",
			"c": "${step.0.z}",
		}},
	}
	if err := AutoDeriveTemplateDeps(steps); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(steps[1].DependsOn) != 1 {
		t.Errorf("multiple refs to same step should dedupe to one dep, got %v", steps[1].DependsOn)
	}
}

func TestAutoDerive_EmptyPlan(t *testing.T) {
	if err := AutoDeriveTemplateDeps(nil); err != nil {
		t.Errorf("empty plan should not error: %v", err)
	}
	if err := AutoDeriveTemplateDeps([]PlanStep{}); err != nil {
		t.Errorf("empty plan should not error: %v", err)
	}
}

func makeGraphWithDep(t *testing.T, depResult string, depState NodeState) (*Graph, string) {
	t.Helper()
	g := NewGraph()
	upstream := &Node{Type: NodeTool, ToolName: "upstream"}
	id := g.AddNode(upstream)
	if depState == StateFailed {
		g.SetError(id, fmt.Errorf("upstream failed"))
		// SetError leaves Result empty; set it manually for the failed-but-has-output case.
		g.mu.Lock()
		g.nodes[id].Result = depResult
		g.mu.Unlock()
	} else {
		g.SetResult(id, depResult)
	}
	return g, id
}

func TestStringifyIntFloat(t *testing.T) {
	if got := stringifyTemplateValue(float64(7)); got != "7" {
		t.Errorf("integer float should stringify without decimal, got %q", got)
	}
	if got := stringifyTemplateValue(float64(7.5)); !strings.HasPrefix(got, "7.5") {
		t.Errorf("non-integer float should keep decimals, got %q", got)
	}
	// strconv reachable via the fallthrough branch.
	_ = strconv.Itoa(0)
}
