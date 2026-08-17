package agent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The list matches what the scheduler actually plants.
//
// A list an application reads to check its coverage is worth less than nothing
// if it can fall behind the code: the application checks it, passes, and the
// step still fails at dispatch. And a hand-kept list is exactly the problem
// GraftedToolNames exists to solve — the names were in a comment before, and a
// comment is a hand-kept list with no test.
//
// So the list is not trusted here. Every string literal assigned to a Node's
// ToolName anywhere in this package is read out of the source, and the two sets
// must be equal. Adding a graft with a new name fails here, at build time,
// instead of failing inside a run on whichever machine the step was aimed at.
func TestGraftedToolNamesMatchesTheScheduler(t *testing.T) {
	inSource := map[string]bool{}

	for _, file := range packageSources(t) {
		f, err := parser.ParseFile(token.NewFileSet(), file, readSource(t, file), 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", file, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			kv, ok := n.(*ast.KeyValueExpr)
			if !ok {
				return true
			}
			key, ok := kv.Key.(*ast.Ident)
			if !ok || key.Name != "ToolName" {
				return true
			}
			// Only a literal is a graft. A variable means the name came from a
			// plan, and the planner only names what the registry showed it.
			if lit, ok := kv.Value.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				inSource[mustUnquote(t, lit.Value)] = true
			}
			return true
		})
	}

	if len(inSource) == 0 {
		t.Fatal("no literal ToolName found in this package, so this test is reading nothing")
	}

	declared := map[string]bool{}
	for _, n := range GraftedToolNames() {
		declared[n] = true
	}

	for name := range inSource {
		if !declared[name] {
			t.Errorf("the scheduler plants a %q step and GraftedToolNames does not list it, "+
				"so an application checking its coverage passes and the step still fails "+
				"at dispatch", name)
		}
	}
	for name := range declared {
		if !inSource[name] {
			t.Errorf("GraftedToolNames lists %q and nothing in this package plants it — "+
				"an application is being told to register a tool the engine will never name", name)
		}
	}

	t.Logf("%d grafted names, agreed by source and list", len(inSource))
}

// A caller cannot edit the engine's answer.
func TestGraftedToolNamesHandsOutACopy(t *testing.T) {
	first := GraftedToolNames()
	if len(first) == 0 {
		t.Fatal("no grafted names at all")
	}
	first[0] = "clobbered"
	if second := GraftedToolNames(); second[0] == "clobbered" {
		t.Error("editing the returned slice changed the engine's own list")
	}
}

// packageSources lists this package's non-test .go files.
func packageSources(t *testing.T) []string {
	t.Helper()
	all, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("reading the package: %v", err)
	}
	var out []string
	for _, name := range all {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	if len(out) == 0 {
		t.Fatal("no source files found, so this test is looking in the wrong place")
	}
	return out
}

// mustUnquote turns a Go string literal into its value.
func mustUnquote(t *testing.T, lit string) string {
	t.Helper()
	if len(lit) < 2 {
		t.Fatalf("not a string literal: %s", lit)
	}
	return lit[1 : len(lit)-1]
}
