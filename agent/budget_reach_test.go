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
					if !ok || len(x.Args) != 1 {
						return true
					}
					// Both resolvers. replyBudget was added later, for the caps
					// on what a stage WRITES, and a guard that only knew about
					// budget would report every one of those as dead.
					if sel.Sel.Name != "budget" && sel.Sel.Name != "replyBudget" {
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

// A stage that writes to a model must take its ceiling from the table, not from
// a literal.
//
// The table was built on the boundary "caps on content going INTO a prompt",
// which put every output cap outside it. Measured across 600 runs: the
// reflector's ceiling was 1024 and its p90 was 1024 — more than one reflection
// in ten was cut off mid-reply, on a number with no comment and no commit that
// chose it. It arrived in a wholesale move and was never revisited when the
// reflector was given the user's answer to write.
//
// Both halves decide how much of a run reaches a model. One does it on the
// return leg.
func TestNoStageSetsItsOwnReplyCeiling(t *testing.T) {
	fset := token.NewFileSet()
	pkg, err := parser.ParseDir(fset, ".", func(f fs.FileInfo) bool {
		return !strings.HasSuffix(f.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse the package: %v", err)
	}

	// Exempt, with the reason. A literal here is a decision about the SHAPE of
	// the reply rather than its size, and the table would obscure that.
	exempt := map[string]string{
		"preflight.go":     "the router answers with a mode and a handful of words; 96 states that",
		"scheduler.go":     "a one-line label",
		"validator_llm.go": "a yes or no with a reason",
	}

	for _, p := range pkg {
		for path, file := range p.Files {
			base := path[strings.LastIndex(path, "/")+1:]
			ast.Inspect(file, func(n ast.Node) bool {
				kv, ok := n.(*ast.KeyValueExpr)
				if !ok {
					return true
				}
				key, ok := kv.Key.(*ast.Ident)
				if !ok || key.Name != "MaxTokens" {
					return true
				}
				lit, ok := kv.Value.(*ast.BasicLit)
				if !ok {
					return true // a call or a field: already resolved somewhere
				}
				if _, ok := exempt[base]; ok {
					return true
				}
				t.Errorf("%s sets MaxTokens to the literal %s. Every other cap on how much "+
					"of a run reaches a model is in budgets.go and scales with the window; "+
					"a literal here does not, so a 1M-context deployment gets the same "+
					"ceiling as an 8K one — and nothing shows the number next to its siblings.",
					base, lit.Value)
				return true
			})
		}
	}
}
