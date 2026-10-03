package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/llm"
)

// Provenance must never reach a provider.
//
// The whole design rests on this: the field exists so a lane can tell a turn that
// did work from one that only spoke, and it is carried on the same struct that is
// marshalled into the request body. A missing `json:"-"` would send our own
// bookkeeping to the model as part of its message.
func TestProvenanceIsNeverSentToAProvider(t *testing.T) {
	req := llm.ChatRequest{
		Model: "m",
		Messages: []llm.Message{
			{Role: "assistant", Content: "the docs are rebuilt", Provenance: "file_read, edit_file, bash"},
		},
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"Provenance", "provenance", "file_read", "edit_file"} {
		if strings.Contains(string(body), leak) {
			t.Errorf("the request body contains %q: %s", leak, body)
		}
	}
}

// The note names the turns that ran something, newest last.
func TestHistoryProvenanceNoteListsWhatRan(t *testing.T) {
	note := historyProvenanceNote([]llm.Message{
		{Role: "user", Content: "fix the intro"},
		{Role: "assistant", Content: "done", Provenance: "file_read, edit_file, bash"},
		{Role: "user", Content: "yeah thats much better"},
	})
	if note == "" {
		t.Fatal("no note for history whose last reply ran three tools")
	}
	if !strings.Contains(note, "file_read, edit_file, bash") {
		t.Errorf("the note does not name the tools: %q", note)
	}
	if !strings.Contains(note, "the turn before this one") {
		t.Errorf("the note does not place the turn: %q", note)
	}
	// It must not let the lane read another turn's work as its own — the failure
	// this exists for was a turn answering "Confirmed" about an earlier run.
	if !strings.Contains(note, "never this one's") {
		t.Errorf("the note does not say whose actions these are: %q", note)
	}
}

// A turn nothing is recorded for is left out, not called toolless. Most stored
// history has no trace, and describing it as having run nothing would turn an
// absence of records into a claim about the conversation.
func TestHistoryProvenanceNoteOmitsWhatItDoesNotKnow(t *testing.T) {
	if note := historyProvenanceNote([]llm.Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi"},
		{Role: "user", Content: "how are you"},
	}); note != "" {
		t.Errorf("a note was produced for history with no provenance at all: %q", note)
	}

	// Mixed: one known turn, one not. Only the known one is named, and the note
	// says an unlisted turn is unknown rather than idle.
	note := historyProvenanceNote([]llm.Message{
		{Role: "assistant", Content: "older", Provenance: "bash"},
		{Role: "user", Content: "and now?"},
		{Role: "assistant", Content: "newer"},
	})
	if !strings.Contains(note, "bash") {
		t.Errorf("the known turn is missing: %q", note)
	}
	if !strings.Contains(note, "not evidence it ran nothing") {
		t.Errorf("the note does not say absence is not evidence: %q", note)
	}
}

// An empty history produces nothing, so a first turn carries no note.
func TestHistoryProvenanceNoteIsEmptyOnAFirstTurn(t *testing.T) {
	if note := historyProvenanceNote(nil); note != "" {
		t.Errorf("a first turn was given a note: %q", note)
	}
}
