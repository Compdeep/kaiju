package plugins

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

type fakeTool struct{ n string }

func (t fakeTool) Name() string                { return t.n }
func (t fakeTool) Description() string         { return "" }
func (t fakeTool) Parameters() json.RawMessage { return json.RawMessage("{}") }
func (t fakeTool) Impact(map[string]any) int   { return toolapi.ImpactObserve }
func (t fakeTool) Execute(context.Context, map[string]any) (string, error) {
	return "", nil
}

type fakePlugin struct{ name string }

func (f fakePlugin) Name() string        { return f.name }
func (f fakePlugin) Description() string { return "fake " + f.name }
func (f fakePlugin) Register(h Host) {
	h.AddTool(fakeTool{f.name + "_tool"})
	h.RegisterBinaryDecoder("application/x-"+f.name, func([]byte) (string, error) { return "", nil })
	h.AddSkill(f.name, "## Planning Guidance\n\nprobe first")
}

// Add puts a plugin in the compiled set; Activate runs its Register and hands back
// its tools, and names what it was asked for and could not find.
//
// There is no active flag to assert on any more. A plugin is compiled in and live,
// or it is not installed, so Compiled() is the whole state.
func TestRegistryLifecycle(t *testing.T) {
	Add(fakePlugin{"testfake"})

	found := false
	for _, n := range Compiled() {
		if n == "testfake" {
			found = true
		}
	}
	if !found {
		t.Fatalf("Compiled() = %v, want it to contain testfake", Compiled())
	}

	tools, on, missing := Activate([]string{"testfake", "nope"}, Deps{})
	if len(on) != 1 || on[0] != "testfake" {
		t.Fatalf("on = %v, want [testfake]", on)
	}
	if len(missing) != 1 || missing[0] != "nope" {
		t.Fatalf("missing = %v, want [nope]", missing)
	}
	if len(tools) != 1 || tools[0].Name() != "testfake_tool" {
		t.Fatalf("tools = %v, want one testfake_tool", tools)
	}
}

// A plugin's skill reaches whatever Deps wired up. Before AddSkill existed the
// bridge fetched each card and logged that it held one, so a plugin could ship an
// instruction that nothing ever read.
func TestActivatePassesTheSkillOn(t *testing.T) {
	Add(fakePlugin{"skillful"})
	got := map[string]string{}
	Activate([]string{"skillful"}, Deps{AddSkill: func(name, md string) { got[name] = md }})
	if got["skillful"] == "" {
		t.Fatal("the plugin's skill did not reach Deps.AddSkill")
	}
}

// An unwired AddSkill must not panic: a plugin that ships a card into a host that
// cannot take one still has to load its tools.
func TestActivateWithNoSkillSinkDoesNotPanic(t *testing.T) {
	Add(fakePlugin{"nosink"})
	tools, on, _ := Activate([]string{"nosink"}, Deps{})
	if len(on) != 1 || len(tools) != 1 {
		t.Fatalf("on = %v, tools = %v; want the plugin to activate anyway", on, tools)
	}
}

// The catalogue answers what COULD be installed. Every Python plugin is carried by
// the one bridge, so asking for several of them asks for a single tag.
func TestCatalogueResolvesNamesToTags(t *testing.T) {
	if len(Installable()) == 0 {
		t.Fatal("the embedded catalogue is empty")
	}
	for _, e := range Installable() {
		if e.Name == "" || e.Description == "" || e.Kind == "" || e.Tier == "" {
			t.Fatalf("catalogue entry incomplete: %+v", e)
		}
		if e.Kind != KindGo && e.Kind != KindPython {
			t.Fatalf("%s has kind %q, want go or python", e.Name, e.Kind)
		}
		if e.Kind == KindPython && (e.Dir == "" || e.Port == 0) {
			t.Fatalf("python plugin %s needs a dir and a port: %+v", e.Name, e)
		}
	}

	tags, err := Tags([]string{"webreader", "illustrator"})
	if err != nil {
		t.Fatalf("Tags: %v", err)
	}
	if len(tags) != 1 || tags[0] != "plugin_remote" {
		t.Fatalf("tags = %v, want the single plugin_remote for two python plugins", tags)
	}
	if tags, err = Tags([]string{"pdf"}); err != nil || len(tags) != 1 || tags[0] != "plugin_pdf" {
		t.Fatalf("Tags(pdf) = %v, %v; want [plugin_pdf]", tags, err)
	}
}

// A name the catalogue does not list is an error that says so. Resolving is kept
// apart from building precisely so this can be answered without a compiler.
func TestTagsRefusesAnUnknownName(t *testing.T) {
	if _, err := Tags([]string{"pdf", "not_a_plugin"}); err == nil {
		t.Fatal("Tags accepted a name that is not in the catalogue")
	} else if !contains(err.Error(), "not_a_plugin") {
		t.Fatalf("the error does not name what was asked for: %v", err)
	}
}

// Missing is what converge keys off. A Python plugin counts as present when the
// bridge is, because the bridge is what carries it.
func TestMissingTreatsTheBridgeAsCarryingPythonPlugins(t *testing.T) {
	if got := Missing([]string{"illustrator"}); len(got) != 1 {
		t.Fatalf("Missing(illustrator) = %v with no bridge compiled in, want it reported missing", got)
	}
	Add(fakePlugin{"remote"})
	if got := Missing([]string{"illustrator"}); len(got) != 0 {
		t.Fatalf("Missing(illustrator) = %v with the bridge compiled in, want nothing missing", got)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}
