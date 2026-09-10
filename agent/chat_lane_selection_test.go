package agent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"strings"
	"testing"
)

// Every laneSelection built by hand must carry the run's reasoning choice.
//
// Converse builds one as a literal, because the chat lane overrides the MODEL
// and nothing else. That literal silently dropped every field it did not
// mention: a per-request effort travelled on the scheduler and react paths,
// which go through laneSelectionFromTrigger, and vanished on the one lane a
// person actually talks to.
//
// Nothing failed. The run returned 200 with a good answer and the effort simply
// was not on the wire — which is the exact fault the whole feature exists to
// prevent, arriving through the door it was built to close. Every unit test
// passed, because every one of them went through laneSelectionFromTrigger.
//
// So: a literal laneSelection either sets the reasoning fields or it is a
// deliberate decision recorded here.
func TestEveryHandBuiltLaneSelectionCarriesTheReasoning(t *testing.T) {
	fset := token.NewFileSet()
	pkg, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatal(err)
	}

	found := 0
	for _, p := range pkg {
		for name, f := range p.Files {
			ast.Inspect(f, func(n ast.Node) bool {
				lit, ok := n.(*ast.CompositeLit)
				if !ok {
					return true
				}
				id, ok := lit.Type.(*ast.Ident)
				if !ok || id.Name != "laneSelection" {
					return true
				}
				if len(lit.Elts) == 0 {
					return true // the zero value, which means "no selection"
				}
				found++
				var keys []string
				for _, e := range lit.Elts {
					if kv, ok := e.(*ast.KeyValueExpr); ok {
						if k, ok := kv.Key.(*ast.Ident); ok {
							keys = append(keys, k.Name)
						}
					}
				}
				line := fset.Position(lit.Pos()).Line
				// The literal in Converse is completed on the next lines rather
				// than inline, so a nearby assignment counts.
				src := fileText(t, name)
				near := window(src, line, 6)
				for _, want := range []string{"effort", "budget"} {
					has := false
					for _, k := range keys {
						if k == want {
							has = true
						}
					}
					if !has && !strings.Contains(near, "sel."+want) {
						t.Errorf("%s:%d builds a laneSelection without %s. A literal drops "+
							"every field it does not name, so the run's own choice would not "+
							"reach the wire on this lane", name, line, want)
					}
				}
				return true
			})
		}
	}
	if found == 0 {
		t.Fatal("no hand-built laneSelection found, so this test is looking in the wrong place")
	}
	t.Logf("%d hand-built laneSelection literal(s) checked", found)
}

func fileText(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(b)
}

// window returns the lines around a 1-indexed line number.
func window(src string, line, span int) string {
	lines := strings.Split(src, "\n")
	lo := line - 1
	hi := line + span
	if lo < 0 {
		lo = 0
	}
	if hi > len(lines) {
		hi = len(lines)
	}
	return strings.Join(lines[lo:hi], "\n")
}
