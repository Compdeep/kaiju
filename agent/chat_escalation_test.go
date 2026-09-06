package agent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// Whether a turn may reach the agent is decided in one place per path, and the
// chat lane's place is the router.
//
// There used to be a second control here: a per-turn permission read only on
// this path, answering the same question the execute path answers with
// execution_mode — under a different name, with the opposite default, and sent
// by no interface. Two ways to say one thing, one of them reachable only by
// hand-writing a request. It is gone, and this fails if it comes back.
func TestTheChatLaneHasOneEscalationDecision(t *testing.T) {
	src := readSource(t, "chat.go")

	f, err := parser.ParseFile(token.NewFileSet(), "chat.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parsing chat.go: %v", err)
	}

	// ChatTurn carries no permission field.
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
					t.Error("ChatTurn.Agent is back. Whether a turn may reach the " +
						"agent is the router's decision on this path and " +
						"execution_mode's on the other; a third control answers " +
						"the same question in a third way")
				}
			}
		}
		return false
	})

	// And the router is asked unconditionally, not behind a permission.
	body := funcBody(t, src, "Chat")
	if !strings.Contains(body, "a.routeQuery(") {
		t.Fatal("the chat lane no longer asks the router at all")
	}
	if strings.Contains(body, "mayEscalate") {
		t.Error("the router is behind a permission again")
	}
}
