package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

// The dispatch cap governs every tool but one.
//
// It used to be skipped by every tool implementing TypedExecutor, which is very
// nearly all of them — so a cap written to bound a step's result bounded a
// handful of string-only tools and nothing else. The tools most able to return
// something enormous are exactly the typed ones that fetch.
//
// One web_fetch returned 434,493 characters. It was wired into a compute step's
// prompt, which then failed on the model's context length; its result became an
// arc, and the two reframes, the reflector and the aggregator downstream of it
// each failed on the same error. Five model calls, five round trips paid for,
// and only the last one reached the reader.

// A fetched envelope is cut, and survives being cut: every key still there, the
// longest string field shortened, so a ${node.X.field} downstream still
// resolves.
func TestAFetchedEnvelopeIsCutAndStaysValid(t *testing.T) {
	const cap = 4096
	env, _ := json.Marshal(map[string]any{
		"status":  "ok",
		"url":     "https://example.invalid/rates",
		"title":   "Interchange rates",
		"content": strings.Repeat("interchange fee schedule. ", 20_000),
	})
	if len(env) < 400_000 {
		t.Fatalf("the fixture is %d bytes; it is meant to be the size that broke a run", len(env))
	}

	got := truncateToolResult(string(env), cap, Text.HeadTail)
	if len(got) > cap {
		t.Errorf("result is %d bytes against a cap of %d", len(got), cap)
	}

	var back map[string]any
	if err := json.Unmarshal([]byte(got), &back); err != nil {
		t.Fatalf("the envelope no longer parses after cutting: %v", err)
	}
	for _, k := range []string{"status", "url", "title", "content"} {
		if _, ok := back[k]; !ok {
			t.Errorf("%q was lost; a downstream reference to it would resolve to nothing", k)
		}
	}
	if back["url"] != "https://example.invalid/rates" {
		t.Errorf("a short field was damaged: %v", back["url"])
	}
}
