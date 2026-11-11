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

// The planner is shown the picked groups' tools, each once, and the tools that
// name no group.
func TestGroups_ShowOnlyThePickedGroupsTools(t *testing.T) {
	got := shown(t, groupedAgent(t), toolapi.GroupCode, toolapi.GroupFiles)
	want := []string{shellToolName, computeToolName, "file_read"}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("shown %v, want %v", got, want)
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

// compute comes only with code or data, so its section and the workspace
// listing are written only then.
func TestGroups_ComputeOnlyWithCodeOrData(t *testing.T) {
	a := groupedAgent(t)
	if got := shown(t, a, toolapi.GroupWeb); slices.Contains(got, computeToolName) {
		t.Errorf("a web request was shown compute: %v", got)
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
