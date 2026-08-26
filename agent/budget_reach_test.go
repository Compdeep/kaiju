package agent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"strings"
	"testing"
)

// A budget nobody reads is worse than no budget. It sits in the table telling a
// reader that one cap scales with the model's window while the code goes on
// using a compiled constant, so the table — the whole point of which is to make
// these numbers knowable in one place — states something false.
//
// payloadBudget was exactly that: declared, tabled, and reachable from nothing.
// The cap that actually governed a payload was a const two files away.
//
// So the package reads its own source and checks each declared budget is passed
// to a.budget somewhere outside budgets.go and outside a test. It reads source
// the way grafted_test.go and frozen_test.go do: the property is about the shape
// of the code, so the code is what gets read.
func TestEveryDeclaredBudgetIsRead(t *testing.T) {
	fset := token.NewFileSet()
	pkg, err := parser.ParseDir(fset, ".", func(f fs.FileInfo) bool {
		return !strings.HasSuffix(f.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse the package: %v", err)
	}

	declared := map[string]bool{}
	read := map[string]bool{}
	for _, p := range pkg {
		for path, file := range p.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.ValueSpec:
					// A budget is a var of type budgetSpec, declared in budgets.go.
					for i, name := range x.Names {
						if i < len(x.Values) && isBudgetSpec(x.Values[i]) {
							declared[name.Name] = true
						}
					}
				case *ast.CallExpr:
					// a.budget(x) — whatever a is called.
					sel, ok := x.Fun.(*ast.SelectorExpr)
					if !ok || sel.Sel.Name != "budget" || len(x.Args) != 1 {
						return true
					}
					id, ok := x.Args[0].(*ast.Ident)
					if ok && !strings.HasSuffix(path, "budgets.go") {
						read[id.Name] = true
					}
				}
				return true
			})
		}
	}

	if len(declared) == 0 {
		t.Fatal("no budgets found — this guard is reading the wrong thing")
	}
	for name := range declared {
		if !read[name] {
			t.Errorf("%s is declared and tabled but nothing resolves it, so the "+
				"cap it claims to set is not the cap that runs", name)
		}
	}
}

func isBudgetSpec(v ast.Expr) bool {
	lit, ok := v.(*ast.CompositeLit)
	if !ok {
		return false
	}
	id, ok := lit.Type.(*ast.Ident)
	return ok && id.Name == "budgetSpec"
}
