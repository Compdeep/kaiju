package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/llm"
	"github.com/Compdeep/kaiju/agent/toolapi"
)

type recallStore struct {
	gotSession string
	gotTerms   []string
	gotOpts    toolapi.Recall
	answer     []toolapi.FoundMessage
	err        error
}

func (r *recallStore) SearchMessages(context.Context, string, string, int) ([]toolapi.FoundMessage, error) {
	return nil, nil
}

func (r *recallStore) RecallMessages(_ context.Context, session string, terms []string, opts toolapi.Recall) ([]toolapi.FoundMessage, error) {
	r.gotSession, r.gotTerms, r.gotOpts = session, terms, opts
	return r.answer, r.err
}

// The words reach the store, and so does the count of what the model is already
// being sent.
func TestRecallAsksTheStoreForWhatIsMissing(t *testing.T) {
	store := &recallStore{answer: []toolapi.FoundMessage{{ID: 2, Role: "user", Content: "fourteen days"}}}
	a := &Agent{messages: store}
	turn := ChatTurn{SessionID: "s1", History: []llm.Message{
		{Role: "system", Content: "[Conversation summary]: …"},
		{Role: "user", Content: "one"},
		{Role: "assistant", Content: "two"},
		{Role: "user", Content: "what did we settle on"},
	}}
	found := a.recall(context.Background(), turn, []string{"retention", "audit"})

	if store.gotSession != "s1" {
		t.Errorf("session = %q", store.gotSession)
	}
	if strings.Join(store.gotTerms, ",") != "retention,audit" {
		t.Errorf("terms = %v", store.gotTerms)
	}
	// Three spoken messages; the summary is not one of them.
	if store.gotOpts.SkipNewest != 3 {
		t.Errorf("SkipNewest = %d, want 3 — the summary should not be counted", store.gotOpts.SkipNewest)
	}
	if len(found) != 1 {
		t.Errorf("got %d messages back", len(found))
	}
}

// Nothing asked for, no store, or no conversation: no lookup at all.
func TestRecallDoesNothingWithoutSomethingToDo(t *testing.T) {
	cases := []struct {
		name  string
		agent *Agent
		turn  ChatTurn
		terms []string
	}{
		{"no terms", &Agent{messages: &recallStore{}}, ChatTurn{SessionID: "s1"}, nil},
		{"no store", &Agent{}, ChatTurn{SessionID: "s1"}, []string{"retention"}},
		{"no session", &Agent{messages: &recallStore{}}, ChatTurn{}, []string{"retention"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if found := c.agent.recall(context.Background(), c.turn, c.terms); found != nil {
				t.Errorf("looked something up anyway: %v", found)
			}
			if s, ok := c.agent.messages.(*recallStore); ok && s.gotTerms != nil {
				t.Error("the store was asked")
			}
		})
	}
}

// A store that fails ends the recall, not the turn.
func TestRecallSurvivesAStoreFailure(t *testing.T) {
	a := &Agent{messages: &recallStore{err: errRecall}}
	found := a.recall(context.Background(), ChatTurn{SessionID: "s1"}, []string{"retention"})
	if found != nil {
		t.Errorf("returned %v after a failure", found)
	}
}

var errRecall = errStr("the index is locked")

type errStr string

func (e errStr) Error() string { return string(e) }

// The block says where the messages came from and what was searched for, so a
// match that is not relevant can be recognised as one.
func TestRecallBlockSaysWhereItCameFrom(t *testing.T) {
	block := recallBlock([]toolapi.FoundMessage{
		{Role: "user", Content: "how long for the audit rows", CreatedAt: 1700000000},
		{Role: "assistant", Content: "fourteen days"},
	}, []string{"retention", "audit"})

	for _, want := range []string{
		"Earlier messages from this same conversation",
		"retention, audit",
		"may not be relevant",
		"how long for the audit rows",
		"fourteen days",
		"2023-11-14",
	} {
		if !strings.Contains(block, want) {
			t.Errorf("the block does not mention %q:\n%s", want, block)
		}
	}
	if recallBlock(nil, []string{"x"}) != "" {
		t.Error("an empty recall produced a block")
	}
}

// The block goes immediately before the message it was recalled for, and the
// message stays last so the model answers that one.
func TestWithRecallPlacesTheBlockBeforeTheQuestion(t *testing.T) {
	in := []llm.Message{
		{Role: "system", Content: "you are…"},
		{Role: "user", Content: "one"},
		{Role: "assistant", Content: "two"},
		{Role: "user", Content: "what did we settle on"},
	}
	out := withRecall(in, "RECALLED")

	if len(out) != len(in)+1 {
		t.Fatalf("got %d messages, want %d", len(out), len(in)+1)
	}
	if out[len(out)-1].Content != "what did we settle on" {
		t.Errorf("the question is no longer last: %q", out[len(out)-1].Content)
	}
	if out[len(out)-2].Content != "RECALLED" {
		t.Errorf("the block is not immediately before it: %q", out[len(out)-2].Content)
	}
	if out[0].Content != "you are…" {
		t.Errorf("the system prompt moved: %q", out[0].Content)
	}
	// The input is not written through.
	if len(in) != 4 || in[3].Content != "what did we settle on" {
		t.Error("the messages passed in were modified")
	}
	// Nothing to add changes nothing.
	if got := withRecall(in, ""); len(got) != len(in) {
		t.Error("an empty block still changed the request")
	}
}

// What the router sends back is tidied before it is searched for: blanks widen
// the expression without widening what it finds, and so do repeats.
func TestCleanTerms(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{[]string{"retention", "audit"}, "retention,audit"},
		{[]string{"retention", "", "  ", "audit"}, "retention,audit"},
		{[]string{"retention", "Retention", "RETENTION"}, "retention"},
		{[]string{" retention "}, "retention"},
		{nil, ""},
		{[]string{"", " "}, ""},
	}
	for _, c := range cases {
		if got := strings.Join(cleanTerms(c.in), ","); got != c.want {
			t.Errorf("cleanTerms(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
