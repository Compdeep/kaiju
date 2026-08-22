package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Compdeep/kaiju/agent"
	"github.com/Compdeep/kaiju/agent/toolapi"
)

// stubStore records what it was asked and answers with what it was given.
type stubStore struct {
	gotQuery   string
	gotSession string
	gotLimit   int
	answer     []toolapi.FoundMessage
	err        error
}

func (s *stubStore) SearchMessages(_ context.Context, query, sessionID string, limit int) ([]toolapi.FoundMessage, error) {
	s.gotQuery, s.gotSession, s.gotLimit = query, sessionID, limit
	return s.answer, s.err
}

// message_search does not recall; the chat lane does. Present so the stub is a
// whole store.
func (s *stubStore) RecallMessages(context.Context, string, []string, toolapi.Recall) ([]toolapi.FoundMessage, error) {
	return nil, nil
}

// payload reads the tool's own fields out of the envelope.
func payload(t *testing.T, msg toolapi.ToolMessage) map[string]any {
	t.Helper()
	var p map[string]any
	if err := json.Unmarshal(msg.Data, &p); err != nil {
		t.Fatalf("payload is not an object: %v (%s)", err, msg.Data)
	}
	return p
}

// run calls the tool the way a run inside the named conversation would. An empty
// session is a call from outside any conversation.
func run(t *testing.T, tool *MessageSearch, session string, params map[string]any) toolapi.ToolMessage {
	t.Helper()
	ctx := context.Background()
	if session != "" {
		ctx = agent.WithExecContext(ctx, &agent.ExecuteContext{Graph: &agent.Graph{SessionID: session}})
	}
	msg, err := tool.ExecuteTyped(ctx, params)
	if err != nil {
		t.Fatalf("execute returned an error rather than a failed result: %v", err)
	}
	return msg
}

// A search reaches the store with the words, the session and the ceiling.
func TestMessageSearchPassesTheQueryOn(t *testing.T) {
	store := &stubStore{answer: []toolapi.FoundMessage{
		{ID: 3, SessionID: "s1", Role: "user", Content: "we agreed on the shorter timeout", CreatedAt: 1700000000},
	}}
	msg := run(t, NewMessageSearch(store), "s1", map[string]any{"query": "timeout", "limit": 5})

	if store.gotQuery != "timeout" {
		t.Errorf("query = %q, want timeout", store.gotQuery)
	}
	if store.gotSession != "s1" {
		t.Errorf("session = %q, want s1 by default", store.gotSession)
	}
	if store.gotLimit != 5 {
		t.Errorf("limit = %d, want 5", store.gotLimit)
	}
	if msg.Status != toolapi.StatusOK {
		t.Errorf("status = %q, want ok", msg.Status)
	}
	if !strings.Contains(msg.Content, "shorter timeout") {
		t.Errorf("the message is not in the content: %q", msg.Content)
	}
}

// Scope "all" drops the session, so every conversation is searched.
func TestMessageSearchScopeAllSearchesEveryConversation(t *testing.T) {
	store := &stubStore{}
	run(t, NewMessageSearch(store), "s1", map[string]any{"query": "timeout", "scope": "all"})
	if store.gotSession != "" {
		t.Errorf("session = %q, want it dropped for scope all", store.gotSession)
	}
}

// A run that is not in a conversation cannot search "this" one. It searches all
// of them and says so, rather than refusing.
func TestMessageSearchWidensWhenThereIsNoSession(t *testing.T) {
	store := &stubStore{answer: []toolapi.FoundMessage{{ID: 1, Role: "user", Content: "hello"}}}
	msg := run(t, NewMessageSearch(store), "", map[string]any{"query": "hello"})
	if store.gotSession != "" {
		t.Errorf("session = %q, want empty", store.gotSession)
	}
	if !strings.Contains(msg.Content, "searched every conversation") {
		t.Errorf("the widening is not stated in the content: %q", msg.Content)
	}
	if scope := payload(t, msg)["scope"]; scope != "all" {
		t.Errorf("scope in the payload = %v, want all", scope)
	}
}

// No match is an answer, not a failure.
func TestMessageSearchNoMatchIsEmptyNotFailed(t *testing.T) {
	msg := run(t, NewMessageSearch(&stubStore{}), "s1", map[string]any{"query": "kestrels"})
	if msg.Status != toolapi.StatusEmpty {
		t.Errorf("status = %q, want empty", msg.Status)
	}
	if !strings.Contains(msg.Detail, "kestrels") {
		t.Errorf("the detail does not say what was looked for: %q", msg.Detail)
	}
}

// The caller's own mistakes come back as failed results with the reason, so the
// run carries on and the step can be written again.
func TestMessageSearchRejectsBadCalls(t *testing.T) {
	cases := []struct {
		name   string
		params map[string]any
		says   string
	}{
		{"no query", map[string]any{}, "needs a query"},
		{"blank query", map[string]any{"query": "   "}, "needs a query"},
		{"no words in the query", map[string]any{"query": "!!!"}, "has none"},
		{"unknown scope", map[string]any{"query": "x", "scope": "everywhere"}, "takes"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			msg := run(t, NewMessageSearch(&stubStore{}), "s1", c.params)
			if msg.Status != toolapi.StatusError {
				t.Fatalf("status = %q, want failed", msg.Status)
			}
			if !strings.Contains(msg.Detail, c.says) {
				t.Errorf("detail = %q, want it to mention %q", msg.Detail, c.says)
			}
		})
	}
}

// Built with no store at all, the tool says so instead of taking the run down.
func TestMessageSearchWithoutAStoreFailsSafely(t *testing.T) {
	msg := run(t, NewMessageSearch(nil), "s1", map[string]any{"query": "anything"})
	if msg.Status != toolapi.StatusError {
		t.Errorf("status = %q, want failed", msg.Status)
	}
	if !strings.Contains(msg.Detail, "no message store") {
		t.Errorf("detail = %q, want the reason in it", msg.Detail)
	}
}

// The limit is held inside what the schema declares, whatever a caller sends.
func TestMessageSearchBoundsTheLimit(t *testing.T) {
	for _, c := range []struct{ sent, want int }{{0, 10}, {-4, 10}, {200, 50}, {7, 7}} {
		store := &stubStore{}
		run(t, NewMessageSearch(store), "s1", map[string]any{"query": "x", "limit": c.sent})
		if store.gotLimit != c.want {
			t.Errorf("limit %d became %d, want %d", c.sent, store.gotLimit, c.want)
		}
	}
}

// The listing is a set of results to choose between: each is one line, with the
// whole text in the payload beside it.
func TestMessageSearchListingIsOneLinePerMessage(t *testing.T) {
	long := strings.Repeat("word ", 400)
	store := &stubStore{answer: []toolapi.FoundMessage{{ID: 1, Role: "assistant", Content: "first\nsecond\nthird"}, {ID: 2, Role: "user", Content: long}}}
	msg := run(t, NewMessageSearch(store), "s1", map[string]any{"query": "word"})

	lines := strings.Split(msg.Content, "\n")
	if len(lines) != 2 {
		t.Fatalf("want one line per message, got %d: %q", len(lines), msg.Content)
	}
	for _, l := range lines {
		if len(l) > messageSearchExcerpt+80 {
			t.Errorf("a listing line runs to %d characters", len(l))
		}
	}
	whole := payload(t, msg)["messages"]
	found, ok := whole.([]any)
	if !ok || len(found) != 2 {
		t.Fatalf("the payload does not carry both messages: %#v", whole)
	}
	if got := found[1].(map[string]any)["content"]; got != long {
		t.Error("the payload holds the shortened text rather than the whole message")
	}
}

// A store that fails is reported with its reason rather than as nothing found.
func TestMessageSearchReportsAStoreFailure(t *testing.T) {
	store := &stubStore{err: errStore}
	msg := run(t, NewMessageSearch(store), "s1", map[string]any{"query": "x"})
	if msg.Status != toolapi.StatusError {
		t.Fatalf("status = %q, want failed", msg.Status)
	}
	if !strings.Contains(msg.Detail, errStore.Error()) {
		t.Errorf("detail = %q, want the store's reason in it", msg.Detail)
	}
}

var errStore = errTest("the index is locked")

type errTest string

func (e errTest) Error() string { return string(e) }

// The conversation searched is the run's, not one fixed when the tool was
// built. One registry serves every conversation on the machine, so a tool
// holding a session of its own would have every run searching the same one.
func TestMessageSearchTakesTheSessionFromTheRun(t *testing.T) {
	store := &stubStore{}
	tool := NewMessageSearch(store)
	for _, session := range []string{"first", "second"} {
		run(t, tool, session, map[string]any{"query": "x"})
		if store.gotSession != session {
			t.Errorf("run in %q searched %q", session, store.gotSession)
		}
	}
}

// The listing is cut by characters, not bytes. A byte cut lands inside a
// multi-byte character — Japanese is three bytes to the character — and the
// listing the model reads then ends in a broken one.
func TestMessageSearchCutsTheListingByCharacters(t *testing.T) {
	for _, prefix := range []string{"", "a", "ab", "abcd"} {
		store := &stubStore{answer: []toolapi.FoundMessage{
			{ID: 1, Role: "user", Content: prefix + strings.Repeat("会議で決めたタイムアウト", 40)},
		}}
		msg := run(t, NewMessageSearch(store), "s1", map[string]any{"query": "x"})
		if !utf8.ValidString(msg.Content) {
			t.Errorf("prefix %q: the listing is not valid UTF-8", prefix)
		}
	}
	store := &stubStore{answer: []toolapi.FoundMessage{{ID: 1, Role: "user", Content: strings.Repeat("🐋", 400)}}}
	if msg := run(t, NewMessageSearch(store), "s1", map[string]any{"query": "x"}); !utf8.ValidString(msg.Content) {
		t.Error("four-byte characters: the listing is not valid UTF-8")
	}
}
