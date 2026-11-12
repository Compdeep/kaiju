package agent

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Compdeep/kaiju/agent/skillmd"
	"github.com/Compdeep/kaiju/agent/toolapi"
)

// groupedTool is a tool that names its groups.
type groupedTool struct {
	plainTool
	groups []string
}

func (g *groupedTool) Groups() []string { return g.groups }

// groupedAgent has one tool per group, a tool in two groups, and the shell,
// which names none.
func groupedAgent(t *testing.T) *Agent {
	t.Helper()
	reg := toolapi.NewRegistry()
	for _, tool := range []toolapi.Tool{
		&plainTool{name: shellToolName},
		&groupedTool{plainTool{"web_search"}, []string{toolapi.GroupWeb}},
		&groupedTool{plainTool{"process_kill"}, []string{toolapi.GroupOSProcess}},
		&groupedTool{plainTool{computeToolName}, []string{toolapi.GroupCode, toolapi.GroupData}},
		&groupedTool{plainTool{"file_read"}, []string{toolapi.GroupFiles, toolapi.GroupCode}},
	} {
		if err := reg.Register(tool); err != nil {
			t.Fatal(err)
		}
	}
	return &Agent{registry: reg}
}

func card(name string, groups ...string) *skillmd.SkillMD {
	return skillmd.NewSkillMD(&skillmd.Frontmatter{Name: name, Description: name, Groups: groups},
		"## Planning Guidance\nguidance for "+name+"\n", "", "", time.Time{}, nil)
}

func shown(t *testing.T, a *Agent, groups ...string) []string {
	t.Helper()
	g := NewGraph()
	g.Preflight = &PreflightResult{Groups: groups}
	got, _ := a.relevantTools(context.Background(), g, Trigger{}, "anything")
	slices.Sort(got)
	return got
}

// Every tool is listed whatever preflight picked. A pick is a guess made with
// thinking off, and a tool left out of it was a tool the planner could not use:
// Qwen 3.6 picked only data for "the center of gravity of the solar system
// right now", and the planner had no web tool to fetch the positions with.
func TestGroups_EveryToolIsListed(t *testing.T) {
	got := shown(t, groupedAgent(t), toolapi.GroupCode)
	if len(got) != 5 {
		t.Errorf("shown %v, want all five", got)
	}
}

// wordyGroupedTool has a two-sentence description and names its groups.
type wordyGroupedTool struct {
	wordyTool
	groups []string
}

func (w *wordyGroupedTool) Groups() []string { return w.groups }

// The picked groups' tools are described in full; the rest in one line, their
// signature and first sentence, so a wrong pick costs detail and not the tool.
func TestGroups_PickedGroupsInFullTheRestInOneLine(t *testing.T) {
	reg := toolapi.NewRegistry()
	desc := "Read a page on the web. Returns its text, cut to the budget, with the links it holds."
	for _, tool := range []toolapi.Tool{
		&wordyGroupedTool{wordyTool{"web_fetch", desc}, []string{toolapi.GroupWeb}},
		&wordyGroupedTool{wordyTool{"edit_file", "Change a file. Needs task_files."}, []string{toolapi.GroupCode}},
	} {
		if err := reg.Register(tool); err != nil {
			t.Fatal(err)
		}
	}
	index := compileToolIndex(reg, []string{"web_fetch", "edit_file"}, []string{toolapi.GroupCode})
	if !strings.Contains(index, "edit_file() — Change a file. Needs task_files.\n") {
		t.Errorf("the picked group's tool is not in full:\n%s", index)
	}
	if !strings.Contains(index, "web_fetch() — Read a page on the web.\n") || strings.Contains(index, "Returns its text") {
		t.Errorf("the unpicked tool is not one line:\n%s", index)
	}
	if full := compileToolIndex(reg, []string{"web_fetch"}, nil); !strings.Contains(full, "Returns its text") {
		t.Errorf("no group picked did not describe every tool in full:\n%s", full)
	}
}

// No group picked, or no preflight at all, is every tool, as before groups.
func TestGroups_NoneMeansEveryTool(t *testing.T) {
	a := groupedAgent(t)
	if got := shown(t, a); len(got) != 5 {
		t.Errorf("no group picked showed %v, want all five", got)
	}
	got, _ := a.relevantTools(context.Background(), nil, Trigger{}, "anything")
	if len(got) != 5 {
		t.Errorf("no preflight showed %v, want all five", got)
	}
}

// compute's section and the workspace listing come only with code or data;
// compute itself is still listed, in one line.
func TestGroups_ComputeSectionsOnlyWithCodeOrData(t *testing.T) {
	a := groupedAgent(t)
	if got := shown(t, a, toolapi.GroupWeb); !slices.Contains(got, computeToolName) {
		t.Errorf("a web request was not listed compute: %v", got)
	}
	g := NewGraph()
	g.Preflight = &PreflightResult{Groups: []string{toolapi.GroupWeb}}
	relevant, _ := a.relevantTools(context.Background(), g, Trigger{}, "anything")
	p := a.executiveSystemPrompt(context.Background(), g, relevant, "", "observe", "")
	for _, section := range []string{"## Compute Nodes", "## Workspace Layout"} {
		if strings.Contains(p, section) {
			t.Errorf("a web request's planner prompt has %q", section)
		}
	}
}

// Preflight keeps the groups that exist and drops the rest.
func TestGroups_PreflightKeepsOnlyKnownGroups(t *testing.T) {
	a := &Agent{}
	out := a.validatePreflight(&preflightRaw{Groups: []string{"web", " OS_Process ", "network", "web"}})
	if !slices.Equal(out.Groups, []string{"web", "os_process"}) {
		t.Errorf("groups = %v, want [web os_process]", out.Groups)
	}
}

// A card in a picked group comes with it; a card preflight picked from a group
// it did not pick is dropped; a card naming no group is kept when picked.
func TestGroups_CardsFollowTheGroups(t *testing.T) {
	a := &Agent{skillGuidance: map[string]*skillmd.SkillMD{
		"coding":   card("coding", toolapi.GroupCode),
		"browsing": card("browsing", toolapi.GroupBrowser),
		"weather":  card("weather"),
		"unpicked": card("unpicked"),
	}}
	out := a.validatePreflight(&preflightRaw{
		Groups: []string{toolapi.GroupCode},
		Skills: []string{"browsing", "weather"},
	})
	if !slices.Equal(out.Skills, []string{"weather", "coding"}) {
		t.Errorf("cards = %v, want [weather coding]", out.Skills)
	}
}

// Every group preflight is offered is listed with what it means.
func TestGroups_PreflightIsShownEachGroup(t *testing.T) {
	list := groupList()
	for _, g := range toolapi.Groups {
		if !strings.Contains(list, "- "+g.Name+": "+g.About) {
			t.Errorf("group %q is not offered to preflight", g.Name)
		}
	}
}

// The salvage path reads groups like the rest.
func TestGroups_SalvagedFromAPartReply(t *testing.T) {
	raw := `{"mode":"agent","intent":"observe","groups":["web"],"context":{"intent":"x"`
	out, err := parsePreflight(raw)
	if err != nil || !slices.Equal(out.Groups, []string{"web"}) {
		t.Errorf("salvaged groups = %v (%v)", out, err)
	}
}

// Every group a card names is one preflight can pick, or the card never comes
// with it: the cards built into the binary and the ones shipped beside it.
func TestGroups_CardsNameKnownGroups(t *testing.T) {
	cards := loadBuiltinSkills(nil)
	shipped, err := skillmd.LoadDir("../skills/bundled", nil)
	if err != nil || len(shipped) == 0 {
		t.Fatalf("no shipped cards at ../skills/bundled: %v", err)
	}
	for _, c := range shipped {
		cards["shipped "+c.Name()] = c
	}
	named := 0
	for key, c := range cards {
		for _, g := range c.Groups() {
			named++
			if !slices.ContainsFunc(toolapi.Groups, func(k toolapi.Group) bool { return k.Name == g }) {
				t.Errorf("card %s names %q, which preflight cannot pick", key, g)
			}
		}
	}
	if named < 8 {
		t.Errorf("%d card groups read, want at least 8; the groups field is not being read", named)
	}
}

// A compute_mode with neither code nor data picked would leave the planner
// without compute, so the group that brings it is added.
func TestGroups_ComputeModeBringsItsGroup(t *testing.T) {
	a := &Agent{}
	for mode, want := range map[string]string{"shallow": toolapi.GroupData, "deep": toolapi.GroupCode} {
		out := a.validatePreflight(&preflightRaw{Groups: []string{"web"}, ComputeMode: mode})
		if !slices.Contains(out.Groups, want) {
			t.Errorf("compute_mode %s with groups %v, want %s added", mode, out.Groups, want)
		}
	}
	out := a.validatePreflight(&preflightRaw{Groups: []string{"web", "code"}, ComputeMode: "shallow"})
	if !slices.Equal(out.Groups, []string{"web", "code"}) {
		t.Errorf("code already brings compute; groups = %v", out.Groups)
	}
}
