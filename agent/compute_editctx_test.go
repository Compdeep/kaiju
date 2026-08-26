package agent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// A script that was EDITED reads its inputs the same way a script that was
// WRITTEN does: from the file named by $KAIJU_CONTEXT. The coder's prompt
// requires it — "NEVER inline context values as string literals in your code" —
// so every generated script depends on that variable being set when it runs.
//
// The write branch prefixed the execute line with it. The edit branch built its
// own execute and returned before reaching that code, so a repaired script was
// handed a command that ran it against nothing.
//
// Observed on a real run: Holmes diagnosed a truncated script, a repair edited
// it into a complete one, the plan ran the execute line it was given, and the
// output file held {"error": "KAIJU_CONTEXT not set"} — with exit code 0. A step
// that reported success and produced an error object.
//
// Read from the source because the property is which branch does what, and a
// test that calls the function would need a coder, an LLM and a written file to
// reach either branch.
func TestComputeEditPathAttachesTheRuntimeContext(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "compute.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse compute.go: %v", err)
	}

	// Every place that puts an execute line on a result must also name the
	// variable that line needs. Counted per enclosing function body, since the
	// write and edit branches live in the same one.
	var setsExecute, setsContext int
	ast.Inspect(file, func(n ast.Node) bool {
		asg, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for _, lhs := range asg.Lhs {
			idx, ok := lhs.(*ast.IndexExpr)
			if !ok {
				continue
			}
			lit, ok := idx.Index.(*ast.BasicLit)
			if ok && strings.Trim(lit.Value, `"`) == "execute" {
				setsExecute++
			}
		}
		return true
	})
	src := readSource(t, "compute.go")
	setsContext = strings.Count(src, `"KAIJU_CONTEXT=" + shQuote(ctxPath)`)

	if setsExecute == 0 {
		t.Fatal("nothing sets an execute line — this guard is reading the wrong thing")
	}
	if setsContext < 2 {
		t.Errorf("execute is set in %d places but the runtime context is attached in only %d. "+
			"A branch that hands back a command without KAIJU_CONTEXT runs a correct script "+
			"against no input, and the script reports it by printing an error and exiting 0.",
			setsExecute, setsContext)
	}
}
