package agent

import (
	"strconv"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// catTool is a tool that declares what kind of work it does.
type catTool struct {
	*countingTool
	cats []string
}

func (c *catTool) Categories() []string { return c.cats }

func scopeAgent(t *testing.T, tools ...toolapi.Tool) *Agent {
	t.Helper()
	a := &Agent{registry: toolapi.NewRegistry()}
	for _, tl := range tools {
		if err := a.registry.Register(tl); err != nil {
			t.Fatalf("register %s: %v", tl.Name(), err)
		}
	}
	return a
}

// names builds a registry bigger than the floor so scoping is even considered.
func scopeNames(n int) []string {
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, "t"+strconv.Itoa(i))
	}
	return out
}

// Below the floor nothing is decided: a small registry is shown whole, which is
// what this engine did before any of this existed.
func TestScopeLeavesASmallRegistryAlone(t *testing.T) {
	a := scopeAgent(t)
	g := &Graph{Preflight: &PreflightResult{RequiredCategories: []string{"network"}}}
	base := scopeNames(scopeFloor)
	if got := a.scopeToWork(g, base); len(got) != len(base) {
		t.Errorf("a registry of %d was narrowed to %d; nothing under the floor should be", len(base), len(got))
	}
}

// A tool that declares nothing is never removed for it. Silence means "cannot
// say", and treating it as "matches nothing" would take away a capability from
// every tool written before the interface existed.
func TestScopeKeepsToolsThatDeclareNothing(t *testing.T) {
	quiet := &countingTool{name: "quiet"}
	loud := &catTool{countingTool: &countingTool{name: "loud"}, cats: []string{"compute"}}
	a := scopeAgent(t, quiet, loud)
	g := &Graph{Preflight: &PreflightResult{RequiredCategories: []string{"network"}}}

	base := append(scopeNames(scopeFloor), "quiet", "loud")
	got := a.scopeToWork(g, base)
	if !hasName(got, "quiet") {
		t.Error("a tool that declares no category was removed by a category filter")
	}
	if hasName(got, "loud") {
		t.Error("a tool declaring only compute survived a network-only scope")
	}
}

// No categories from preflight is no opinion, so nothing is narrowed.
func TestScopeWithoutCategoriesChangesNothing(t *testing.T) {
	loud := &catTool{countingTool: &countingTool{name: "loud"}, cats: []string{"compute"}}
	a := scopeAgent(t, loud)
	base := append(scopeNames(scopeFloor), "loud")

	for _, g := range []*Graph{
		nil,
		{},
		{Preflight: &PreflightResult{}},
	} {
		if got := a.scopeToWork(g, base); len(got) != len(base) {
			t.Errorf("with no categories the list went from %d to %d", len(base), len(got))
		}
	}
}

// The ranking's best survive whatever their category — the floor is what stops
// a classifier that named the wrong kind from starving the plan.
func TestScopeKeepsTheRankingsBestWhateverTheirCategory(t *testing.T) {
	top := &catTool{countingTool: &countingTool{name: "top"}, cats: []string{"compute"}}
	a := scopeAgent(t, top)
	g := &Graph{Preflight: &PreflightResult{RequiredCategories: []string{"network"}}}

	// "top" is ranked first, and its category does not match.
	base := append([]string{"top"}, scopeNames(scopeFloor+5)...)
	got := a.scopeToWork(g, base)
	if !hasName(got, "top") {
		t.Error("the ranking's first choice was removed because its category did not match")
	}
}

func hasName(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
