package agent

import (
	"reflect"
	"testing"
)

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
	if r.Type != "step" || r.Index != 0 {
		t.Errorf("kind/index mismatch: %+v", r)
	}
	if !reflect.DeepEqual(r.Path, []string{"results", "0", "url"}) {
		t.Errorf("path mismatch: %v", r.Path)
	}
}

func TestFindRefs_NodeKind(t *testing.T) {
	refs := FindRefs(map[string]any{"x": "${node.12D3KooWShkP.field}"})
	if len(refs) != 1 || refs[0].Type != "node" {
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
