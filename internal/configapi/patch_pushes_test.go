package configapi

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// Every agent field this endpoint accepts must be PUSHED to the running agent,
// not only stored in the config.
//
// The engine reads its own copy of the config, taken when it was built. Four
// fields here were stored and never pushed, so a value changed in the settings
// pane was written to the file, persisted, shown back on the next GET, and
// ignored by every run until the process restarted — and llm.reasoning shipped
// the same way. A setting that persists without taking effect is worse than one
// that does neither, because the file then disagrees with the behaviour and the
// file is what an operator reads.
//
// This reads the handler rather than running it: constructing an Agent to prove
// a one-line call is heavier than the thing proved, and the regression this
// guards against is textual — a field added to the patch struct without a push
// beside it.
func TestEveryPatchedAgentFieldIsPushedToTheAgent(t *testing.T) {
	src, err := os.ReadFile("configapi.go")
	if err != nil {
		t.Fatal(err)
	}
	f, err := parser.ParseFile(token.NewFileSet(), "configapi.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parsing configapi.go: %v", err)
	}

	// The setter each patched field is expected to reach. A field with no live
	// effect belongs here with an empty string and a reason, so that adding one
	// is a decision rather than an omission.
	pushes := map[string]string{
		"DAGEnabled":        "SetDAGEnabled",
		"DAGMode":           "SetDAGMode",
		"SafetyLevel":       "SetClearance",
		"MaxInvestigations": "SetPlanLimits",
		"MaxReplans":        "SetPlanLimits",
		"ExecutionMode":     "SetExecutionMode",
		"RouteProvider":     "SetRouteModel",
		"RouteModel":        "SetRouteModel",
		"AnswerProvider":    "SetAnswerModel",
		"AnswerModel":       "SetAnswerModel",
	}

	fields := patchFields(t, f, "Agent")
	if len(fields) == 0 {
		t.Fatal("no agent patch fields found, so this test is looking in the wrong place")
	}
	body := handlerBody(t, src)
	for _, name := range fields {
		want, known := pushes[name]
		if !known {
			t.Errorf("agent patch field %q is new: say which setter pushes it to the "+
				"running agent, or record here that it has no live effect and why", name)
			continue
		}
		if want == "" {
			continue
		}
		if !strings.Contains(body, "c.agent."+want+"(") {
			t.Errorf("agent patch field %q is stored but %s is never called, so a "+
				"change to it is written to the file and ignored by every run", name, want)
		}
	}
}

// The reasoning switch is on the llm block rather than the agent one, and had
// this exact fault when it shipped.
func TestThePatchedReasoningSwitchIsPushedToTheLane(t *testing.T) {
	src, err := os.ReadFile("configapi.go")
	if err != nil {
		t.Fatal(err)
	}
	body := handlerBody(t, src)
	if !strings.Contains(body, "c.agent.SetReasoning(") {
		t.Error("llm.reasoning is stored but SetReasoning is never called, so the " +
			"switch persists while the running lane keeps its previous answer")
	}
}

// patchFields returns the field names of the named anonymous struct inside
// configPatch.
func patchFields(t *testing.T, f *ast.File, block string) []string {
	t.Helper()
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name.Name != "configPatch" {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			return false
		}
		for _, fld := range st.Fields.List {
			if len(fld.Names) == 0 || fld.Names[0].Name != block {
				continue
			}
			inner, ok := fld.Type.(*ast.StarExpr)
			if !ok {
				continue
			}
			sub, ok := inner.X.(*ast.StructType)
			if !ok {
				continue
			}
			for _, s := range sub.Fields.List {
				for _, n := range s.Names {
					out = append(out, n.Name)
				}
			}
		}
		return false
	})
	return out
}

// handlerBody returns the source of handleUpdateConfig.
func handlerBody(t *testing.T, src []byte) string {
	t.Helper()
	i := strings.Index(string(src), "func (c *API) handleUpdateConfig")
	if i < 0 {
		t.Fatal("handleUpdateConfig not found")
	}
	return string(src[i:])
}
