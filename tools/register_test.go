package tools

import (
	"context"
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
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
	reg := toolapi.NewRegistry()
	registered, err := Register(reg, Deps{})
	if err != nil {
		t.Fatalf("Register into an empty registry: %v", err)
	}

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
	reg := toolapi.NewRegistry()
	if _, err := Register(reg, Deps{}); err != nil {
		t.Fatal(err)
	} // no memory, no executor

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

// Exclude leaves a tool out, so an application can take its name.
func TestExcludeLeavesAToolOut(t *testing.T) {
	reg := toolapi.NewRegistry()
	registered, err := Register(reg, Deps{Exclude: []string{"clipboard"}})
	if err != nil {
		t.Fatal(err)
	}

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

// An exclusion that matches nothing is reported. Ignoring it registers the tool
// the application meant to leave out, and the application finds out one line
// later as a name collision — which points at the wrong mistake.
func TestAnExclusionThatMatchesNothingIsReported(t *testing.T) {
	reg := toolapi.NewRegistry()
	registered, err := Register(reg, Deps{Exclude: []string{"proces_kill"}}) // misspelt
	if err == nil {
		t.Fatal("a misspelt exclusion was accepted, and process_kill is registered anyway")
	}
	if !strings.Contains(err.Error(), "proces_kill") {
		t.Errorf("the error should name the entry that matched nothing: %v", err)
	}
	// The set is still registered — this is a report, not a refusal, since the
	// tools themselves are fine and the caller may want to carry on.
	if len(registered) == 0 {
		t.Error("nothing was registered")
	}
	if _, ok := reg.Get("process_kill"); !ok {
		t.Error("process_kill should be registered — that is the point being reported")
	}
}

// stubBash stands in for an application's own implementation under the
// engine's name.
type stubBash struct{}

func (stubBash) Name() string                { return "bash" }
func (stubBash) Description() string         { return "the application's own" }
func (stubBash) Impact(map[string]any) int   { return toolapi.ImpactObserve }
func (stubBash) Parameters() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (stubBash) Execute(context.Context, map[string]any) (string, error) {
	return "from the application", nil
}

// The override: exclude the name from the core set, then register your own
// under it. The planner still sees one tool for the job and every graft still
// resolves, and the substitution is visible in the call that made it.
func TestAnApplicationTakesANameByExcludingIt(t *testing.T) {
	reg := toolapi.NewRegistry()
	if _, err := Register(reg, Deps{Exclude: []string{"bash"}}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(stubBash{}); err != nil {
		t.Fatalf("registering into the excluded name: %v", err)
	}

	got, ok := reg.Get("bash")
	if !ok {
		t.Fatal("bash is not registered at all")
	}
	out, err := got.Execute(context.Background(), nil)
	if err != nil || out != "from the application" {
		t.Errorf("Get(bash) = %q, %v — want the application's own implementation", out, err)
	}
}

// A name is registered once and never reassigned. Whichever order the calls
// come in, the collision is reported rather than resolved silently.
func TestANameTakenTwiceIsAnError(t *testing.T) {
	// Application first, then the core set.
	reg := toolapi.NewRegistry()
	if err := reg.Register(stubBash{}); err != nil {
		t.Fatal(err)
	}
	registered, err := Register(reg, Deps{})
	if err == nil {
		t.Fatal("the core set took a name the application already held, without saying so")
	}
	if !strings.Contains(err.Error(), "bash") || !strings.Contains(err.Error(), "exclude") {
		t.Errorf("the error should name the tool and say what to do: %v", err)
	}
	// What it did register before stopping is reported, so a caller can see how
	// far it got rather than guessing.
	for _, name := range registered {
		if _, ok := reg.Get(name); !ok {
			t.Errorf("reported %q as registered and it is not", name)
		}
	}

	// Core set first, then the application.
	reg2 := toolapi.NewRegistry()
	if _, err := Register(reg2, Deps{}); err != nil {
		t.Fatal(err)
	}
	if err := reg2.Register(stubBash{}); err == nil {
		t.Error("the application took a name the core set already held, without saying so")
	}
}

// Every core tool is reachable by the name it declares, and no two declare the
// same one.
func TestEveryRegisteredNameIsTheToolsOwn(t *testing.T) {
	reg := toolapi.NewRegistry()
	registered, err := Register(reg, Deps{})
	if err != nil {
		t.Fatal(err)
	}

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

// message_search is registered only when there is somewhere to search. Without a
// store it would register and then answer every call by saying it cannot.
func TestMessageSearchRegistersOnlyWithAStore(t *testing.T) {
	bare := toolapi.NewRegistry()
	names, err := Register(bare, Deps{})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if slices.Contains(names, "message_search") {
		t.Error("message_search registered with no store to read")
	}

	withStore := toolapi.NewRegistry()
	names, err = Register(withStore, Deps{Messages: &stubStore{}})
	if err != nil {
		t.Fatalf("register with a store: %v", err)
	}
	if !slices.Contains(names, "message_search") {
		t.Errorf("message_search not registered though a store was given: %v", names)
	}
}
