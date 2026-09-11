package configapi

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/llm"
	"github.com/Compdeep/kaiju/models"
)

// A fact the catalog records and the adapter drops is a measurement that does
// nothing.
//
// models.json, models.Info and llm.ModelFacts are three shapes of the same
// knowledge — the file's, the catalog's, and what a client needs to decide with.
// Three is the right number: the file format has to exist, and llm must not
// import this catalog because an embedding application supplies its own. What
// joins them is Facts, and a field added at one end and forgotten there is
// silent: the entry parses, the picker shows it, and no call changes.
//
// So the fields models.Info carries about reasoning have to be named in Facts.
func TestEveryReasoningFactReachesTheAdapter(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "configapi.go", nil, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var body string
	ast.Inspect(file, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "Facts" {
			return true
		}
		body = string(readSource(t)[fset.Position(fn.Pos()).Offset:fset.Position(fn.End()).Offset])
		return false
	})
	if body == "" {
		t.Fatal("Facts not found — this guard is reading the wrong thing")
	}

	// Every Info field whose name says it is about reasoning or pace. Named by
	// shape rather than listed, so a field added later is covered without
	// anybody remembering to add it here.
	info := reflect.TypeOf(models.Info{})
	checked := 0
	for i := range info.NumField() {
		name := info.Field(i).Name
		if !strings.Contains(name, "Reasoning") && name != "Pace" && name != "Thinking" {
			continue
		}
		checked++
		// Thinking is read through Thinks(), Pace through DeadlineMultiple().
		reader := map[string]string{"Thinking": "Thinks(", "Pace": "DeadlineMultiple("}[name]
		if reader == "" {
			reader = name
		}
		if !strings.Contains(body, reader) {
			t.Errorf("models.Info.%s is in the catalog and Facts never reads it, so what it "+
				"records reaches no call", name)
		}
	}
	if checked == 0 {
		t.Fatal("no reasoning fields found on models.Info — this guard is reading the wrong thing")
	}
}

func readSource(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("configapi.go")
	if err != nil {
		t.Fatalf("read configapi.go: %v", err)
	}
	return b
}

// The adapter answers for a model the catalog carries, and declines for one it
// does not — which is what makes everything built on it inert for a self-hosted
// endpoint.
func TestFactsDeclinesAModelItDoesNotCarry(t *testing.T) {
	if _, ok := Facts("nobody/has-heard-of-this"); ok {
		t.Error("the adapter answered for a model the catalog does not carry")
	}

	// One the catalog does carry, with its measurements intact.
	var carried models.Info
	for _, m := range models.All() {
		if len(m.ReasoningEfforts) > 0 {
			carried = m
			break
		}
	}
	if carried.ID == "" {
		t.Skip("no model in the catalog records a measured effort")
	}
	f, ok := Facts(carried.ID)
	if !ok {
		t.Fatalf("the adapter declined %q, which the catalog carries", carried.ID)
	}
	if len(f.Thinking.Efforts) != len(carried.ReasoningEfforts) {
		t.Errorf("%q records %d efforts and the adapter passed on %d",
			carried.ID, len(carried.ReasoningEfforts), len(f.Thinking.Efforts))
	}
	for _, e := range f.Thinking.Efforts {
		if e == llm.EffortUnset {
			t.Errorf("%q: an effort came through unrecognised", carried.ID)
		}
	}
	if f.ContextTokens != carried.ContextTokens || f.MaxOutputTokens != carried.MaxOutputTokens {
		t.Errorf("%q: limits did not survive the translation", carried.ID)
	}
}
