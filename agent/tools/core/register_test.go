package core

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/tools"
)

// The two calls an embedding application makes.
//
// These are the contract other people build against, so what is checked here is
// what they are entitled to rely on: the set arrives under the engine's own
// names, a dependency that is absent omits its tools rather than registering
// ones that can only fail, and an override takes the name it is given.

// The names the engine's scheduler spawns by hand. An application that does not
// have these registered will see those steps fail at dispatch with "unknown
// tool", and nothing else will say why.
var namesTheSchedulerSpawns = []string{"bash", "service"}

func TestRegisterSuppliesTheNamesTheEngineExpects(t *testing.T) {
	reg := tools.NewRegistry()
	registered := Register(reg, Deps{})

	for _, name := range namesTheSchedulerSpawns {
		if _, ok := reg.Get(name); !ok {
			t.Errorf("%q is not registered — the scheduler spawns nodes with that name, "+
				"and every one of them would fail at dispatch", name)
		}
	}
	if len(registered) == 0 {
		t.Fatal("Register reported nothing registered")
	}
	// What it says it did and what it did are the same thing.
	for _, name := range registered {
		if _, ok := reg.Get(name); !ok {
			t.Errorf("Register reported %q and did not register it", name)
		}
	}
}

// A dependency that is absent omits its tools. A planner shown a tool whose
// every call returns an error reads that as the task being impossible, not the
// tool being missing.
func TestAnAbsentDependencyOmitsItsTools(t *testing.T) {
	reg := tools.NewRegistry()
	Register(reg, Deps{}) // no memory, no executor

	for _, name := range []string{"memory_store", "memory_recall", "memory_search"} {
		if _, ok := reg.Get(name); ok {
			t.Errorf("%q is registered with no memory store behind it", name)
		}
	}
	if _, ok := reg.Get("web_research"); ok {
		t.Error("web_research is registered with no model to summarise with")
	}
	// web_fetch does not need one, and is registered either way.
	if _, ok := reg.Get("web_fetch"); !ok {
		t.Error("web_fetch needs no model and should be registered regardless")
	}
}

// Exclude is the escape hatch, and a name that is not a core tool is ignored
// rather than being an error — the set changes between versions.
func TestExcludeLeavesAToolOut(t *testing.T) {
	reg := tools.NewRegistry()
	registered := Register(reg, Deps{Exclude: []string{"clipboard", "not_a_core_tool"}})

	if _, ok := reg.Get("clipboard"); ok {
		t.Error("clipboard was excluded and is registered")
	}
	for _, name := range registered {
		if name == "clipboard" {
			t.Error("Register reported clipboard as registered")
		}
	}
	if _, ok := reg.Get("bash"); !ok {
		t.Error("excluding one tool dropped another")
	}
}

// stubBash stands in for an application's own implementation under the
// engine's name.
type stubBash struct{}

func (stubBash) Name() string                { return "bash" }
func (stubBash) Description() string         { return "the application's own" }
func (stubBash) Impact(map[string]any) int   { return tools.ImpactObserve }
func (stubBash) Parameters() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (stubBash) Execute(context.Context, map[string]any) (string, error) {
	return "from the application", nil
}

// The override half. An application keeps the engine's name and supplies its
// own behaviour, so the planner still sees one tool for the job and the
// scheduler's grafts still resolve.
func TestAnApplicationCanReplaceACoreTool(t *testing.T) {
	reg := tools.NewRegistry()
	Register(reg, Deps{})

	// Register refuses a name already taken. That is the guard against two
	// tools for one job arriving by accident.
	if err := reg.Register(stubBash{}); err == nil {
		t.Error("Register accepted a name already registered — an application would " +
			"silently get whichever was registered first")
	}

	// Replace takes it.
	reg.Replace(stubBash{}, "myapp")
	got, ok := reg.Get("bash")
	if !ok {
		t.Fatal("bash disappeared")
	}
	out, err := got.Execute(context.Background(), nil)
	if err != nil || out != "from the application" {
		t.Errorf("Get(bash) = %q, %v — want the application's own implementation", out, err)
	}
	if src := reg.GetSource("bash"); src != "myapp" {
		t.Errorf("source = %q, want the application's, so a trace says which is running", src)
	}
}

// Register replaces rather than refuses, so calling it after an application has
// registered its own does not produce a silent mix that depends on call order.
// The documented order is core first, then Replace.
func TestRegisterReplacesWhatIsAlreadyThere(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Replace(stubBash{}, "myapp")

	Register(reg, Deps{})

	got, _ := reg.Get("bash")
	if out, _ := got.Execute(context.Background(), nil); out == "from the application" {
		t.Error("Register left the application's tool in place — an application calling " +
			"them in the wrong order would get a mix, and would have no way to tell")
	}
	if src := reg.GetSource("bash"); src != "core" {
		t.Errorf("source = %q, want core", src)
	}
}

// Every core tool is reachable by the name it declares, and no two declare the
// same one.
func TestEveryRegisteredNameIsTheToolsOwn(t *testing.T) {
	reg := tools.NewRegistry()
	registered := Register(reg, Deps{})

	seen := map[string]bool{}
	for _, name := range registered {
		if seen[name] {
			t.Errorf("%q was registered twice", name)
		}
		seen[name] = true
		tool, _ := reg.Get(name)
		if tool.Name() != name {
			t.Errorf("registered under %q, declares %q", name, tool.Name())
		}
	}
	sort.Strings(registered)
	t.Logf("core tools (%d): %s", len(registered), strings.Join(registered, ", "))
}
