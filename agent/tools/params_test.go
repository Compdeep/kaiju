package tools

import (
	"encoding/json"
	"reflect"
	"testing"
)

// The same parameter, in every shape it arrives in, has to read the same.
//
// A plan is decoded from JSON, so a number is a float64 and an array is []any.
// An application building a tool node in Go writes an int and a []string. A
// tool that reads only one of those runs on its default for the other and
// answers a question nobody asked, with no error anywhere.

func TestParamNumReadsEveryShape(t *testing.T) {
	var fromJSON map[string]any
	if err := json.Unmarshal([]byte(`{"count": 25, "ratio": 0.5}`), &fromJSON); err != nil {
		t.Fatal(err)
	}

	for _, params := range []map[string]any{
		fromJSON,
		{"count": 25, "ratio": 0.5},
		{"count": int64(25), "ratio": float32(0.5)},
		{"count": json.Number("25"), "ratio": json.Number("0.5")},
	} {
		n, ok := ParamNum(params, "count")
		if !ok || n != 25 {
			t.Errorf("ParamNum(count) = (%v, %v) for %T, want 25", n, ok, params["count"])
		}
		i, ok := ParamInt(params, "count")
		if !ok || i != 25 {
			t.Errorf("ParamInt(count) = (%v, %v) for %T, want 25", i, ok, params["count"])
		}
		r, ok := ParamNum(params, "ratio")
		if !ok || r < 0.49 || r > 0.51 {
			t.Errorf("ParamNum(ratio) = (%v, %v) for %T, want 0.5", r, ok, params["ratio"])
		}
	}
}

// Absent and non-numeric both report not-present, so a caller keeps its default
// rather than acting on a zero it cannot tell from a real one.
func TestParamNumSaysWhenThereIsNoNumber(t *testing.T) {
	params := map[string]any{"count": "twenty-five", "other": nil}
	for _, key := range []string{"count", "other", "missing"} {
		if n, ok := ParamNum(params, key); ok {
			t.Errorf("ParamNum(%s) = (%v, true), want not present", key, n)
		}
	}
	// Zero is a number, and present.
	if n, ok := ParamNum(map[string]any{"count": 0}, "count"); !ok || n != 0 {
		t.Errorf("ParamNum(0) = (%v, %v), want (0, true) — a caller has to tell zero from absent", n, ok)
	}
}

func TestParamStringsReadsEveryShape(t *testing.T) {
	var fromJSON map[string]any
	if err := json.Unmarshal([]byte(`{"tags": ["fleet", "queen"]}`), &fromJSON); err != nil {
		t.Fatal(err)
	}
	want := []string{"fleet", "queen"}

	for _, params := range []map[string]any{
		fromJSON,
		{"tags": []string{"fleet", "queen"}},
		{"tags": []any{"fleet", "queen"}},
	} {
		if got := ParamStrings(params, "tags"); !reflect.DeepEqual(got, want) {
			t.Errorf("ParamStrings = %#v for %T, want %#v", got, params["tags"], want)
		}
	}

	// Entries that are not usable strings are dropped rather than turned into
	// empty ones, so a caller never acts on a value that is not there.
	got := ParamStrings(map[string]any{"tags": []any{"fleet", "", 7, nil, "queen"}}, "tags")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParamStrings with unusable entries = %#v, want %#v", got, want)
	}
	if got := ParamStrings(map[string]any{}, "tags"); got != nil {
		t.Errorf("an absent array = %#v, want nil", got)
	}
	if got := ParamStrings(map[string]any{"tags": "fleet"}, "tags"); got != nil {
		t.Errorf("a bare string is not an array of them, got %#v", got)
	}
}
