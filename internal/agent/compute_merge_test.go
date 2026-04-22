package agent

import (
	"reflect"
	"testing"
)

func TestMergeParamRefsIntoContext(t *testing.T) {
	t.Run("extras only, no context", func(t *testing.T) {
		params := map[string]any{
			"goal":        "calc",
			"mode":        "shallow",
			"ports_data":  "{port list}",
			"spot_rate_1": "$2543",
		}
		got := mergeParamRefsIntoContext(params, nil)
		want := map[string]any{"ports_data": "{port list}", "spot_rate_1": "$2543"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	})

	t.Run("preserves explicit context map and adds extras", func(t *testing.T) {
		params := map[string]any{
			"goal":       "calc",
			"ports_data": "data",
		}
		ctx := map[string]any{"seed": 42}
		got := mergeParamRefsIntoContext(params, ctx)
		m := got.(map[string]any)
		if m["seed"] != 42 || m["ports_data"] != "data" {
			t.Fatalf("unexpected merge result: %#v", m)
		}
	})

	t.Run("no extras, returns ctxData unchanged", func(t *testing.T) {
		params := map[string]any{"goal": "x", "mode": "deep"}
		got := mergeParamRefsIntoContext(params, "keep me")
		if got != "keep me" {
			t.Fatalf("expected pass-through, got %v", got)
		}
	})

	t.Run("non-map ctxData gets wrapped alongside extras", func(t *testing.T) {
		params := map[string]any{"goal": "x", "ports_data": "p"}
		got := mergeParamRefsIntoContext(params, "scalar context")
		m, ok := got.(map[string]any)
		if !ok {
			t.Fatalf("expected map, got %T", got)
		}
		if m["context"] != "scalar context" || m["inputs"].(map[string]any)["ports_data"] != "p" {
			t.Fatalf("unexpected wrap: %#v", m)
		}
	})

	t.Run("nil extras keep nil ctx", func(t *testing.T) {
		params := map[string]any{"goal": "x", "mode": "shallow", "ebs_rate": nil}
		got := mergeParamRefsIntoContext(params, nil)
		if got != nil {
			t.Fatalf("expected nil ctx when all extras are nil, got %v", got)
		}
	})
}
