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

// The Config field a setter writes says the setter exists.
//
// A setter is a second door onto a field Config already fills, and the two are
// written in different files. A reader looking at Config cannot otherwise tell
// which values can still be changed after New and which are fixed for the life
// of the agent — and there is no way to find out by trying, because New takes
// Config by value, so setting a field on your own copy afterwards fails
// silently.
//
// So every setter that has a Config field behind it is named on that field, and
// this holds the two together: a setter renamed or removed leaves a note
// pointing at nothing, and that fails here.
func TestEverySetterIsNamedOnTheFieldItWrites(t *testing.T) {
	cfg := readSource(t, "config.go")

	// The two that configure something Config does not hold. Both are stated in
	// the file's own commentary rather than on a field, since there is no field.
	elsewhere := map[string]bool{
		"SetToolReach":        true, // a tool's reach, in the registry
		"SetClearanceChecker": true, // the checker asked on every gated call
	}

	f, err := parser.ParseFile(token.NewFileSet(), "agent.go", readSource(t, "agent.go"), parser.ParseComments)
	if err != nil {
		t.Fatalf("parsing agent.go: %v", err)
	}

	named := 0
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || !strings.HasPrefix(fn.Name.Name, "Set") || !onAgent(fn) {
			continue
		}
		if elsewhere[fn.Name.Name] {
			if strings.Contains(cfg, "Set at run time: "+fn.Name.Name) {
				t.Errorf("%s is listed as having no Config field, and config.go names it on one", fn.Name.Name)
			}
			continue
		}
		if !strings.Contains(cfg, "Set at run time: "+fn.Name.Name) {
			t.Errorf("no field in config.go says %q sets it. Either name it on the "+
				"field it writes, or add it to the two that configure something "+
				"Config does not hold", fn.Name.Name)
			continue
		}
		named++
	}

	// And no note may name a setter that is gone.
	for _, line := range strings.Split(cfg, "\n") {
		i := strings.Index(line, "Set at run time: ")
		if i < 0 {
			continue
		}
		name := strings.FieldsFunc(line[i+len("Set at run time: "):], func(r rune) bool {
			return r == ',' || r == '.' || r == ' '
		})
		// The convention sentence at the top of config.go names the shape
		// ("Set at run time: X"), not a method.
		if len(name) == 0 || !strings.HasPrefix(name[0], "Set") {
			continue
		}
		if !strings.Contains(readSource(t, "agent.go"), "func (a *Agent) "+name[0]+"(") {
			t.Errorf("config.go says %q sets a field, and no such method exists", name[0])
		}
	}

	if named == 0 {
		t.Fatal("no setter was found named on a Config field, so this test is looking in the wrong place")
	}
	t.Logf("%d setters named on the field they write", named)
}
