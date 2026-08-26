package agent

import (
	"strings"
	"testing"
)

// An imperative is agent. The router is told so — "when in doubt, ALWAYS choose
// agent... misclassifying agent as chat blocks the user's actual request" — and
// does not always follow it.
//
// Observed in one session: "check online" was routed to chat, so the turn had no
// tools and the model answered by promising to search. A QUESTION in the same
// session was routed to agent and did get tools. The two were the wrong way
// round, and the one that was denied tools is the one that asked for work.
func TestAsksForAnAction(t *testing.T) {
	yes := []string{
		"check online",
		"can you check online",
		"please search for the latest release",
		"look up the docs",
		"fetch that page",
		"run the tests",
		"now try again",
		"ok build it",
		"hey could you list the running processes",
		"restart the service",
		"delete that file",
		"Check Online",
	}
	for _, q := range yes {
		if !asksForAnAction(q) {
			t.Errorf("%q asks for work and would be sent to chat, where nothing can run", q)
		}
	}

	no := []string{
		"what is the largest ocean",
		"I was checking my email earlier",
		"thanks, that looks right",
		"why did that fail",
		"the search finder is broken", // "find" inside a word
		"who wrote this",
		"tell me about solana", // a question, and the router may still choose agent
	}
	for _, q := range no {
		if asksForAnAction(q) {
			t.Errorf("%q is not an imperative but was forced to the agent", q)
		}
	}
}

// It runs before the model is asked, like the two checks beside it. Over-routing
// costs one extra call; under-routing hands the user a turn that cannot act.
func TestActionCheckRunsBeforeTheRouter(t *testing.T) {
	src := readSource(t, "preflight.go")
	i := strings.Index(src, "asksForAnAction(query)")
	j := strings.Index(src, "msgs := []llm.Message{{Role: \"system\", Content: prompt.Route}}")
	if i < 0 || j < 0 || i > j {
		t.Error("the action check does not run before the routing call, so the model " +
			"still decides a case it has been observed getting backwards")
	}
}
