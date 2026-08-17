package agent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// Every setter says why it cannot be a Config field.
//
// A setter and a Config field fill the same private field. `applyModels` puts
// the model lanes there straight from Config, and each setter puts the same
// value there again. So a setter buys exactly one thing: saying it again to an
// agent that is already running, which is what an operator changing a setting
// on a live service needs.
//
// A setter that does not need that is surface an embedder reads past for
// nothing, and it is a second way to set something — two doors onto one field,
// where a reader has to check both to know what the value is. Thirteen existed
// when this was first counted, two had no caller in either repository, and
// nothing said which of the rest were load-bearing.
//
// So each one states in its doc comment what reads it during a run. Not as
// prose for its own sake: writing that sentence is what forces the question,
// and the two that could not answer it were deleted (d8a74f4).
func TestEverySetterSaysWhyItIsCallableAtRuntime(t *testing.T) {
	src := readSource(t, "agent.go")
	f, err := parser.ParseFile(token.NewFileSet(), "agent.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parsing agent.go: %v", err)
	}

	// SetConcurrency is covered by the comment over the kernel accessors it
	// belongs to, which says a host with a dashboard adjusts concurrency while
	// the process runs. The parser cannot see a group comment from the method.
	covered := map[string]string{
		"SetConcurrency": "the kernel-accessor group comment above it",
	}

	seen := 0
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || !strings.HasPrefix(fn.Name.Name, "Set") || !onAgent(fn) {
			continue
		}
		seen++
		if why, ok := covered[fn.Name.Name]; ok {
			t.Logf("%s: %s", fn.Name.Name, why)
			continue
		}
		doc := fn.Doc.Text()
		if doc == "" {
			t.Errorf("%s has no doc comment, so nothing says why it is not a Config field", fn.Name.Name)
			continue
		}
		if !strings.Contains(doc, "run time") && !strings.Contains(doc, "runtime") {
			t.Errorf("%s does not say what reads it during a run. A setter that "+
				"nothing reads mid-run is a Config field with a second door on it",
				fn.Name.Name)
		}
	}

	if seen == 0 {
		t.Fatal("no setters found, which means this test is looking in the wrong place")
	}
	t.Logf("%d setters checked", seen)
}

// onAgent reports whether fn is a method on *Agent. Other types in this file
// have Set methods of their own — localClearance.Set is a mutex around an int —
// and they are not the engine's configuration surface.
func onAgent(fn *ast.FuncDecl) bool {
	if fn.Recv == nil || len(fn.Recv.List) != 1 {
		return false
	}
	star, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	id, ok := star.X.(*ast.Ident)
	return ok && id.Name == "Agent"
}
