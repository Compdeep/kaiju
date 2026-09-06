package agent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// The chat mode answers and stays answering.
//
// This lane used to ask the router whether the turn needed the planner, and
// leave for it mid-turn when the router said yes — so asking for chat bought a
// conversation that might not stay one, and the only way to get the guarantee
// was a second flag no interface sent. That flag is gone and this is the
// guarantee, so the lane must not reach the planner by any route.
func TestTheChatLaneCannotReachThePlanner(t *testing.T) {
	body := funcBody(t, readSource(t, "chat.go"), "Chat")

	// The escalation was these two calls. Either one back means the lane can
	// leave again.
	for _, gone := range []string{"RunAgentTask(", "RunDAGSync(", "mayEscalate"} {
		if strings.Contains(body, gone) {
			t.Errorf("Chat calls %s, so the chat mode can still become a planned run", gone)
		}
	}
	// And it must still end in the conversational turn.
	if !strings.Contains(body, "a.Converse(ctx, t)") {
		t.Error("Chat no longer ends in Converse")
	}
}

// The router is still asked, and only for what it names. It returns two things:
// whether the turn needs the agent, and what the answer refers to but cannot
// see. The second is the only source of recall terms, and dropping the call
// would quietly remove the ability to answer "what did we say about X earlier"
// in the one mode with no tools to ask with.
func TestTheChatLaneReadsOnlyWhatTheRouterNames(t *testing.T) {
	body := funcBody(t, readSource(t, "chat.go"), "Chat")
	if !strings.Contains(body, "a.routeQuery(") {
		t.Fatal("the router is not asked at all, so recall terms have no source")
	}
	if !strings.Contains(body, "_, lacking := a.routeQuery(") {
		t.Error("the router's verdict is being read again; in this mode it has " +
			"already been answered by the mode itself")
	}
}

// ChatTurn carries no permission field. Whether a turn may plan is the mode's
// answer now, and a second control would let the two disagree.
func TestChatTurnCarriesNoSecondControl(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "chat.go", readSource(t, "chat.go"), parser.ParseComments)
	if err != nil {
		t.Fatalf("parsing chat.go: %v", err)
	}
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name.Name != "ChatTurn" {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			return false
		}
		for _, fld := range st.Fields.List {
			for _, name := range fld.Names {
				if name.Name == "Agent" {
					t.Error("ChatTurn.Agent is back. Whether a turn may plan is the " +
						"execution mode's answer; a second control lets them disagree")
				}
			}
		}
		return false
	})
}

// The scheduler knows all three. A turn reaching the graph in chat mode arrives
// from a caller that does not go through the chat front door, and routing it
// would ask a question the mode has already answered.
func TestTheSchedulerHandlesAllThreeModes(t *testing.T) {
	body := funcBody(t, readSource(t, "scheduler.go"), "runPlanAndSchedule")
	for _, want := range []string{"case ExecutionAgent:", "case ExecutionChat:"} {
		if !strings.Contains(body, want) {
			t.Errorf("the scheduler has no %s, so that mode falls to the routing branch", want)
		}
	}
}
