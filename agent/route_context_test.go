package agent

import (
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/llm"
)

// The router reads the previous exchange to resolve a terse follow-up. Two
// callers build the history it reads, and they disagree about whether the
// current user message is in it: the chat lane stores then loads, the agent lane
// loads then stores. routeContext has to give the same answer either way.
//
// It did not. It dropped the last entry unconditionally, so on the agent path it
// discarded the ASSISTANT reply — the one turn a follow-up refers to. Measured on
// a live run: the route call carried 635 tokens where the chat lane's carried
// 1078, the router reported the gap itself (lacking_context ["main news"]), and
// "yeah of course any of them" was classified as conversation. The run answered
// from the chat lane and never reached the planner.
func TestRouteContextGivesTheSameExchangeToBothCallers(t *testing.T) {
	u1 := llm.Message{Role: "user", Content: "whats the main news ?"}
	a1 := llm.Message{Role: "assistant", Content: "I could search the web for you."}
	u2 := llm.Message{Role: "user", Content: "yeah of course any of them"}

	// The agent lane loads before it stores, so history stops at the reply.
	agentPath := routeContext([]llm.Message{u1, a1})
	// The chat lane stores before it loads, so the current message is the last.
	chatPath := routeContext([]llm.Message{u1, a1, u2})

	for name, got := range map[string][]llm.Message{"agent": agentPath, "chat": chatPath} {
		if len(got) != 2 || got[0].Content != u1.Content || got[1].Content != a1.Content {
			t.Errorf("the %s path was given %v, want the exchange [%q, %q]. "+
				"Without the reply, \"any of them\" refers to nothing and the turn "+
				"reads as conversation.", name, contents(got), u1.Content, a1.Content)
		}
	}
}

// A longer conversation: both callers must land on the LAST exchange, not one
// turn behind it. Dropping unconditionally slid the window back a turn as well as
// losing the reply.
func TestRouteContextTakesTheLastExchangeNotThePreviousOne(t *testing.T) {
	msgs := []llm.Message{
		{Role: "user", Content: "u1"}, {Role: "assistant", Content: "a1"},
		{Role: "user", Content: "u2"}, {Role: "assistant", Content: "a2"},
	}
	current := llm.Message{Role: "user", Content: "u3"}

	want := []string{"u2", "a2"}
	for name, got := range map[string][]llm.Message{
		"agent": routeContext(msgs),
		"chat":  routeContext(append(append([]llm.Message{}, msgs...), current)),
	} {
		if strings.Join(contents(got), ",") != strings.Join(want, ",") {
			t.Errorf("the %s path was given %v, want %v", name, contents(got), want)
		}
	}
}

// The running summary rides along whichever caller asked, and is not counted as
// part of the exchange.
func TestRouteContextKeepsTheConversationSummary(t *testing.T) {
	sum := llm.Message{Role: "system", Content: "[Conversation summary] earlier turns"}
	got := routeContext([]llm.Message{
		sum,
		{Role: "user", Content: "u1"},
		{Role: "assistant", Content: "a1"},
	})
	if len(got) != 3 || got[0].Content != sum.Content {
		t.Fatalf("the summary was dropped: %v", contents(got))
	}
	if got[1].Content != "u1" || got[2].Content != "a1" {
		t.Errorf("the exchange is %v, want [u1 a1]", contents(got)[1:])
	}
}

// A long reply is capped, and capping must not mutate the caller's history — the
// same slice is read again by the lane that answers.
func TestRouteContextCapsTheReplyWithoutMutatingHistory(t *testing.T) {
	long := strings.Repeat("x", 900)
	history := []llm.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", Content: long},
	}
	got := routeContext(history)
	if len(got) != 2 || len([]rune(got[1].Content)) != 501 {
		t.Errorf("the reply was not capped: %d runes", len([]rune(got[1].Content)))
	}
	if history[1].Content != long {
		t.Error("routeContext modified the caller's history; the answering lane reads the same slice")
	}
}

func contents(msgs []llm.Message) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, m.Content)
	}
	return out
}
