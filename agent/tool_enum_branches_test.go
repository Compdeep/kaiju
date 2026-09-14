package agent_test

// Every value a tool offers is a value its code has a branch for, and every
// branch is a value it offers.
//
// web_fetch named a format in its Description that no branch had ever read —
// "raw (full HTML)" — so a plan asking for it fell through to markdown and got
// the opposite of what the name promised. git's Impact switch carries a case its
// action enum cannot reach. Both are the same fault: the schema and the code are
// edited in separate sittings and nothing compares them.
//
// This reads the real registry for what is offered and the real source for what
// is handled, so neither side can be satisfied by restating the other. It checks
// the enums a tool DISPATCHES on: an enum nobody switches on is data, not a set
// of behaviours, and is reported rather than judged.

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"

	"github.com/Compdeep/kaiju/agent"
	"github.com/Compdeep/kaiju/agent/toolapi"
	"github.com/Compdeep/kaiju/tools"
)

// acceptedNotOffered are case values deliberately absent from an enum. A name
// the code still answers to but no longer advertises belongs here with the
// reason, so it reads as a decision rather than as drift.
var acceptedNotOffered = map[string]map[string]string{
	"web_fetch": {
		"summary": "the name this had when it was meant to paraphrase; it never did, " +
			"so the branch is kept for callers that learned it while the enum offers " +
			"the three behaviours it actually has",
	},
}

// toolSourceDirs are the trees a tool can be defined in, relative to this
// package. The engine's own tools sit beside this file; the rest are in tools/.
var toolSourceDirs = []string{".", "../tools"}

func TestEveryOfferedValueHasABranch(t *testing.T) {
	registered := registryForSchemaReview(t)
	files := parseToolSources(t)

	var undispatched []string
	for _, name := range sortedNames(registered) {
		tool := registered[name]
		enums := enumProperties(t, name, tool.Parameters())
		if len(enums) == 0 {
			continue
		}
		file, ok := files[name]
		if !ok {
			t.Errorf("%s: no source file defines a Name() returning %q, so nothing could be checked", name, name)
			continue
		}
		for _, prop := range sortedKeys(enums) {
			offered := enums[prop]
			handled, found := casesForParam(file, prop)
			if !found {
				undispatched = append(undispatched, name+"."+prop)
				continue
			}

			// A value offered with no case of its own. A default does not count:
			// almost every switch here has one, and in most of them it returns
			// "unknown action", so accepting it as cover would make this half of
			// the check fire almost never. Where a default really is the intended
			// behaviour for a named value — web_fetch's markdown — that value
			// gets a case as well, and the default stays as the tolerance for
			// names this build does not know.
			for _, v := range offered {
				if !handled[v] {
					t.Errorf("%s: %q is offered in the %s enum and no case handles it, so a plan choosing it does not get what the name promises",
						name, v, prop)
				}
			}

			// A branch for a value nobody can ask for. Either the enum lost a
			// value the code still answers to, or the case is dead.
			offeredSet := map[string]bool{}
			for _, v := range offered {
				offeredSet[v] = true
			}
			for _, v := range sortedKeys(handled) {
				if offeredSet[v] {
					continue
				}
				if why, allowed := acceptedNotOffered[name][v]; allowed {
					t.Logf("%s: %q is handled and not offered — %s", name, v, why)
					continue
				}
				t.Errorf("%s: the code has a case for %q and the %s enum does not offer it, so either the enum is missing a value or the case is dead",
					name, v, prop)
			}
		}
	}
	if len(undispatched) > 0 {
		sort.Strings(undispatched)
		// Two different things end up here, and the list is worth reading rather
		// than trusting: an enum genuinely passed through as data (panel_push's
		// plugin), and one dispatched somewhere other than the file that defines
		// the tool (compute's mode, read in compute.go). The second is unchecked,
		// not clean.
		t.Logf("enums no switch in the tool's own file reads: %v", undispatched)
	}
}

// registryForSchemaReview builds the tool set an ordinary deployment shows its
// planner, wired as cmd/kaiju/main.go wires it.
func registryForSchemaReview(t *testing.T) map[string]toolapi.Tool {
	t.Helper()
	ws := t.TempDir()
	ag, err := agent.New(agent.Config{PathConfig: agent.PathConfig{Workspace: ws, DataDir: ws}})
	if err != nil {
		t.Fatalf("agent: %v", err)
	}
	reg := ag.Registry()
	if _, err := tools.Register(reg, tools.Deps{Workspace: ws}); err != nil {
		t.Fatalf("register: %v", err)
	}
	reg.Replace(tools.NewSysinfo(ws), "builtin")
	reg.Replace(agent.NewComputeTool(ag), "builtin")
	reg.Replace(agent.NewEditFileTool(ag), "builtin")
	reg.Replace(agent.NewDebugTool(ag), "builtin")

	out := map[string]toolapi.Tool{}
	for _, name := range reg.List() {
		if tl, ok := reg.Get(name); ok {
			out[name] = tl
		}
	}
	if len(out) == 0 {
		t.Fatal("no tools registered")
	}
	return out
}

// enumProperties returns each top-level parameter that offers a fixed set of
// string values, by parameter name.
func enumProperties(t *testing.T, tool string, raw json.RawMessage) map[string][]string {
	t.Helper()
	var doc struct {
		Properties map[string]struct {
			Enum []json.RawMessage `json:"enum"`
		} `json:"properties"`
	}
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Errorf("%s: parameters are not readable JSON: %v", tool, err)
		return nil
	}
	out := map[string][]string{}
	for name, prop := range doc.Properties {
		var values []string
		for _, v := range prop.Enum {
			var s string
			if json.Unmarshal(v, &s) == nil {
				values = append(values, s)
			}
		}
		if len(values) > 0 {
			out[name] = values
		}
	}
	return out
}

// parseToolSources maps a tool's registered name to the parsed file that defines
// it, found by the Name() method rather than by a filename convention.
func parseToolSources(t *testing.T) map[string]*ast.File {
	t.Helper()
	out := map[string]*ast.File{}
	fset := token.NewFileSet()
	for _, dir := range toolSourceDirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		for _, e := range entries {
			if e.IsDir() || filepath.Ext(e.Name()) != ".go" {
				continue
			}
			path := filepath.Join(dir, e.Name())
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				continue // a file this cannot parse is one it has nothing to say about
			}
			for _, name := range toolNamesDefinedIn(file) {
				out[name] = file
			}
		}
	}
	return out
}

// toolNamesDefinedIn returns the tool names a file declares. It reads the
// methods called Name that return one string constant.
func toolNamesDefinedIn(file *ast.File) []string {
	var names []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "Name" || fn.Recv == nil || fn.Body == nil {
			continue
		}
		if len(fn.Body.List) != 1 {
			continue
		}
		ret, ok := fn.Body.List[0].(*ast.ReturnStmt)
		if !ok || len(ret.Results) != 1 {
			continue
		}
		if s, ok := stringLiteral(ret.Results[0]); ok {
			names = append(names, s)
		}
	}
	return names
}

// casesForParam finds the switches in a file that dispatch on one parameter and
// returns every value they have a case of their own for, and whether such a
// switch was found at all.
//
// A switch qualifies when its tag is an identifier that was read out of
// params["<name>"] somewhere in the same file. That is how every one of these
// tools is written — `action, _ := params["action"].(string)`, then
// `switch action` — and matching on the assignment rather than on the
// identifier's spelling keeps a local called `f` from being mistaken for the
// parameter `format`.
func casesForParam(file *ast.File, param string) (handled map[string]bool, found bool) {
	idents := identsReadFromParam(file, param)
	if len(idents) == 0 {
		return nil, false
	}
	handled = map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		sw, ok := n.(*ast.SwitchStmt)
		if !ok || sw.Tag == nil {
			return true
		}
		tag, ok := sw.Tag.(*ast.Ident)
		if !ok || !idents[tag.Name] {
			return true
		}
		found = true
		for _, stmt := range sw.Body.List {
			clause, ok := stmt.(*ast.CaseClause)
			if !ok {
				continue
			}
			if clause.List == nil {
				continue // the default, which covers no named value
			}
			for _, expr := range clause.List {
				s, ok := stringLiteral(expr)
				if !ok {
					continue
				}
				// A case for the empty string is the guard for a parameter that
				// was not supplied — service answers it with "action is
				// required" rather than "unknown action". No enum offers the
				// empty string as a behaviour, so this is never drift.
				if s == "" {
					continue
				}
				handled[s] = true
			}
		}
		return true
	})
	return handled, found
}

// identsReadFromParam returns the local names a file assigns from
// params["<param>"], in any of the shapes these tools use.
func identsReadFromParam(file *ast.File, param string) map[string]bool {
	out := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) == 0 || len(assign.Rhs) != 1 {
			return true
		}
		if !readsParam(assign.Rhs[0], param) {
			return true
		}
		if id, ok := assign.Lhs[0].(*ast.Ident); ok && id.Name != "_" {
			out[id.Name] = true
		}
		return true
	})
	return out
}

// readsParam reports whether an expression reads params["<param>"], through a
// type assertion, a helper call, or on its own.
func readsParam(expr ast.Expr, param string) bool {
	switch e := expr.(type) {
	case *ast.IndexExpr:
		if id, ok := e.X.(*ast.Ident); ok && id.Name == "params" {
			if s, ok := stringLiteral(e.Index); ok {
				return s == param
			}
		}
	case *ast.TypeAssertExpr:
		return readsParam(e.X, param)
	case *ast.CallExpr:
		// A helper reading it for us: ParamStr(params, "action") and friends.
		var sawParams bool
		for _, arg := range e.Args {
			if id, ok := arg.(*ast.Ident); ok && id.Name == "params" {
				sawParams = true
			}
		}
		if !sawParams {
			return false
		}
		for _, arg := range e.Args {
			if s, ok := stringLiteral(arg); ok && s == param {
				return true
			}
		}
	}
	return false
}

func stringLiteral(expr ast.Expr) (string, bool) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return s, true
}

func sortedNames(m map[string]toolapi.Tool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
