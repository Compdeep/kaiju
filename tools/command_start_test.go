package tools

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// directStarts are the calls to exec.Command left in place, each with why.
var directStarts = map[string]string{
	// The engine stopping a command it started that ran out of time, not a
	// step's own command.
	"process_windows.go": "taskkill",
}

// Every program a tool starts goes through toolapi.Command, so that one place
// decides whether it is locked. A direct exec.Command would start it unlocked
// whatever the step's level.
func TestToolsStartProgramsThroughToolapiCommand(t *testing.T) {
	checkNoDirectStarts(t, ".", directStarts)
}

func checkNoDirectStarts(t *testing.T, dir string, allowed map[string]string) {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(dir, "*.go"))
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == "exec" && (sel.Sel.Name == "Command" || sel.Sel.Name == "CommandContext") {
				if _, ok := allowed[filepath.Base(f)]; !ok {
					t.Errorf("%s: exec.%s starts a program unlocked; use toolapi.Command", fset.Position(sel.Pos()), sel.Sel.Name)
				}
			}
			return true
		})
	}
}
