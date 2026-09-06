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

// The chat lane asks its own question with its own prompt and its own tool.
//
// It used to borrow the router's, which decides chat-or-agent, and throw half
// the answer away — asking a model to decide something already decided, and
// putting that decision in the same small reply as the words. The words could
// then spend the budget before the decision was written, and an unparseable
// reply routes to chat: a decision this lane had already made, arrived at by
// failure. With no mode in the reply there is nothing left to starve.
func TestTheChatLaneAsksOnlyWhatItNeeds(t *testing.T) {
	body := funcBody(t, readSource(t, "chat.go"), "Chat")
	if strings.Contains(body, "a.routeQuery(") {
		t.Error("the chat lane is calling the router again, which asks it to decide " +
			"a mode this lane has already been told")
	}
	if !strings.Contains(body, "a.recallTerms(") {
		t.Fatal("the chat lane no longer asks what to look up, so it cannot reach " +
			"anything older than the window and has no tools to ask with")
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
