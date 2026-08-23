package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent"
	"github.com/Compdeep/kaiju/agent/toolapi"
)

// What these three have to be true of, brought here from an application that had
// its own copies of them.
//
// That application carried three memory tools over the same *agent.Memory — this
// package's type, holding this package's store — so the pair were two
// implementations of one job. Its versions were the better ones in four ways, and
// those four are this engine's behaviour now and are checked here. Without that,
// registering these in their place would have been a silent loss.

func newMemory(t *testing.T) *agent.Memory {
	t.Helper()
	m, err := agent.NewMemory(t.TempDir())
	if err != nil {
		t.Fatalf("memory: %v", err)
	}
	return m
}

// Asking for something never stored is an empty result that names what was asked.
//
// Both answered with a sentence, and this is memory: a model reading the result was
// handed prose where a fact belongs.
func TestRecallingWhatWasNeverStoredIsEmpty(t *testing.T) {
	msg, err := NewMemoryRecall(newMemory(t)).ExecuteTyped(context.Background(),
		map[string]any{"key": "no-such-key"})
	if err != nil {
		t.Fatalf("memory_recall: %v", err)
	}
	if msg.Status != toolapi.StatusEmpty {
		t.Fatalf("an unstored key = %q (%q), want empty", msg.Status, msg.Content)
	}
	if !strings.Contains(msg.Detail, "no-such-key") {
		t.Errorf("the detail should name the key, got %q", msg.Detail)
	}

	msg, err = NewMemorySearch(newMemory(t)).ExecuteTyped(context.Background(),
		map[string]any{"tags": []any{"nothing-tagged-this"}})
	if err != nil {
		t.Fatalf("memory_search: %v", err)
	}
	if msg.Status != toolapi.StatusEmpty {
		t.Fatalf("tags with no hit = %q (%q), want empty", msg.Status, msg.Content)
	}
	if !strings.Contains(msg.Detail, "nothing-tagged-this") {
		t.Errorf("the detail should name the tags, got %q", msg.Detail)
	}
}

// No key is the caller's mistake, not an absence in memory.
//
// Nothing was looked up, so reporting it as empty would say memory holds nothing.
// A failed result rather than a Go error, so the reason reaches whoever wrote the
// step instead of ending it.
func TestRecallWithNothingToLookUpIsAFailure(t *testing.T) {
	msg, err := NewMemoryRecall(newMemory(t)).ExecuteTyped(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("memory_recall returned a Go error rather than a failed result: %v", err)
	}
	if msg.Status != toolapi.StatusError {
		t.Fatalf("no key = %q (%q), want error", msg.Status, msg.Content)
	}
	// And it says where to go instead, since searching without a key is a
	// different tool.
	if !strings.Contains(msg.Detail, "memory_search") {
		t.Errorf("the detail does not name the tool that searches without a key: %q", msg.Detail)
	}
}

// The same for a search with no tags.
func TestSearchWithNoTagsIsAFailure(t *testing.T) {
	msg, err := NewMemorySearch(newMemory(t)).ExecuteTyped(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("memory_search returned a Go error rather than a failed result: %v", err)
	}
	if msg.Status != toolapi.StatusError {
		t.Fatalf("no tags = %q, want error", msg.Status)
	}
}

// Stored, then recalled, then found by tag.
//
// Without this the two above pass on a tool that reports empty for everything.
func TestStoreThenRecallThenSearch(t *testing.T) {
	mem := newMemory(t)

	msg, err := NewMemoryStore(mem).ExecuteTyped(context.Background(), map[string]any{
		"key": "upstream-addr", "value": "10.0.0.1:4001", "tags": []any{"routing"},
	})
	if err != nil {
		t.Fatalf("memory_store: %v", err)
	}
	if msg.Status != toolapi.StatusOK {
		t.Fatalf("memory_store = %q, want ok", msg.Status)
	}

	msg, err = NewMemoryRecall(mem).ExecuteTyped(context.Background(),
		map[string]any{"key": "upstream-addr"})
	if err != nil {
		t.Fatalf("memory_recall: %v", err)
	}
	if msg.Status != toolapi.StatusOK {
		t.Fatalf("recalling a stored key = %q, want ok", msg.Status)
	}
	if msg.Content != "10.0.0.1:4001" {
		t.Errorf("content = %q, want the stored value", msg.Content)
	}

	msg, err = NewMemorySearch(mem).ExecuteTyped(context.Background(),
		map[string]any{"tags": []any{"routing"}})
	if err != nil {
		t.Fatalf("memory_search: %v", err)
	}
	if msg.Status != toolapi.StatusOK {
		t.Fatalf("tag search = %q, want ok", msg.Status)
	}
	// Text, because a result whose content is empty reaches a model as JSON while
	// the other two reach it as words.
	if !strings.Contains(msg.Content, "upstream-addr") {
		t.Errorf("the listing does not name the entry: %q", msg.Content)
	}
	var payload memorySearchData
	if err := json.Unmarshal(msg.Data, &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if payload.Count != 1 || len(payload.Entries) != 1 || payload.Entries[0].Key != "upstream-addr" {
		t.Errorf("payload does not carry the entry: %+v", payload)
	}
	if len(payload.Tags) != 1 || payload.Tags[0] != "routing" {
		t.Errorf("payload does not say which tags were searched: %+v", payload)
	}
}

// A list of tags, however it arrives.
//
// This took one tag only, and refused a call carrying "tags" outright, so the
// question a search exists for — what is stored under any of these — could not be
// asked. A planner sends JSON, which arrives as []any; this program sends
// []string.
func TestSearchTakesTagsInEitherForm(t *testing.T) {
	mem := newMemory(t)
	store := NewMemoryStore(mem)
	for _, pair := range [][2]string{{"a", "first"}, {"b", "second"}} {
		if _, err := store.ExecuteTyped(context.Background(), map[string]any{
			"key": pair[0], "value": pair[1], "tags": []string{pair[0] + "-tag"},
		}); err != nil {
			t.Fatalf("memory_store: %v", err)
		}
	}

	for name, params := range map[string]map[string]any{
		"json list": {"tags": []any{"a-tag", "b-tag"}},
		"go list":   {"tags": []string{"a-tag", "b-tag"}},
		"single":    {"tag": "a-tag"},
	} {
		t.Run(name, func(t *testing.T) {
			msg, err := NewMemorySearch(mem).ExecuteTyped(context.Background(), params)
			if err != nil {
				t.Fatalf("%v", err)
			}
			if msg.Status != toolapi.StatusOK {
				t.Fatalf("status = %q, detail = %q", msg.Status, msg.Detail)
			}
			if !strings.Contains(msg.Content, "first") {
				t.Errorf("the entry tagged a-tag is missing: %q", msg.Content)
			}
		})
	}
}

// None of the three asks a step to name a machine.
//
// The memory is this process's, so there is nowhere else to run. RequiresTarget
// defaults to true for a tool that does not say, and an application that reads it
// would then refuse every memory step that did not name a host.
func TestTheMemoryToolsRunWhereTheMemoryIs(t *testing.T) {
	for _, tool := range []toolapi.Tool{
		NewMemoryStore(nil), NewMemoryRecall(nil), NewMemorySearch(nil),
	} {
		if toolapi.RequiresTarget(tool) {
			t.Errorf("%s asks a step to name a machine, and its memory is in this process",
				tool.Name())
		}
	}
}

// A tool built with no memory store refuses rather than crashing.
//
// All three panicked inside Memory when the store was nil, which takes the process
// down rather than failing one step — and an agent crashing on a memory call is a
// worse answer to "what is stored" than a refusal. Found by running every read-only
// tool in one sweep and watching three of them panic.
func TestTheMemoryToolsRefuseWithNoStore(t *testing.T) {
	for _, tool := range []toolapi.TypedExecutor{
		NewMemoryStore(nil), NewMemoryRecall(nil), NewMemorySearch(nil),
	} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("panicked with no store: %v", r)
				}
			}()
			msg, err := tool.ExecuteTyped(context.Background(), map[string]any{"key": "k", "value": "v"})
			if err != nil {
				return // refused, which is what should happen
			}
			if msg.Status != toolapi.StatusError {
				t.Errorf("status = %q with no store, want error", msg.Status)
			}
		}()
	}
}
