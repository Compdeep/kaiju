package agent

import (
	"fmt"
	"log"
	"slices"
	"strings"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

/*
 * Groups narrow a run to the work it needs.
 *
 * Preflight picks groups from toolapi.Groups. The planner is shown every tool:
 * those in the picked groups, and those that name none, in full, the rest in
 * one line. It is given the cards that come with those groups. Picking none,
 * or no preflight, shows every tool in full, as before groups existed.
 */

// maxCards is the most cards one run carries, picked and added together.
const maxCards = 6

// groupList is the list of groups preflight picks from, one per line.
func groupList() string {
	var sb strings.Builder
	for _, g := range toolapi.Groups {
		fmt.Fprintf(&sb, "- %s: %s\n", g.Name, g.About)
	}
	return sb.String()
}

// groupEnum is the group names as a JSON array, for the preflight schema.
func groupEnum() string {
	names := make([]string, len(toolapi.Groups))
	for i, g := range toolapi.Groups {
		names[i] = `"` + g.Name + `"`
	}
	return "[" + strings.Join(names, ", ") + "]"
}

// knownGroups keeps the names preflight gave that are groups, once each.
func knownGroups(names []string) []string {
	var out []string
	for _, n := range names {
		n = strings.ToLower(strings.TrimSpace(n))
		if slices.Contains(out, n) {
			continue
		}
		if !slices.ContainsFunc(toolapi.Groups, func(g toolapi.Group) bool { return g.Name == n }) {
			if n != "" {
				log.Printf("[dag] preflight: unknown group %q, dropping", n)
			}
			continue
		}
		out = append(out, n)
	}
	return out
}

// withComputeGroup adds the group that brings compute when preflight asked for
// compute and picked groups without it: data for a calculation, code for a
// build. Without it the planner is told the compute mode and not shown compute.
func withComputeGroup(groups []string, computeMode string) []string {
	if computeMode == "" || len(groups) == 0 ||
		slices.Contains(groups, toolapi.GroupCode) || slices.Contains(groups, toolapi.GroupData) {
		return groups
	}
	add := toolapi.GroupData
	if computeMode == "deep" {
		add = toolapi.GroupCode
	}
	log.Printf("[dag] preflight: compute_mode %s with groups %v, adding %s", computeMode, groups, add)
	return append(groups, add)
}

// inGroups reports whether a tool or card naming own belongs to a run that
// picked picked. Naming none belongs everywhere.
func inGroups(own, picked []string) bool {
	if len(own) == 0 || len(picked) == 0 {
		return true
	}
	return slices.ContainsFunc(own, func(g string) bool { return slices.Contains(picked, g) })
}

/*
 * groupCards is the cards a run carries.
 * desc: Preflight's picks first, kept when they exist and belong to a picked
 *       group, then every card that comes with a picked group. Each once,
 *       at most maxCards.
 * param: picked - the card keys preflight named.
 * param: groups - the groups preflight picked, already checked.
 * return: card keys.
 */
func (a *Agent) groupCards(picked, groups []string) []string {
	var out []string
	for _, key := range picked {
		card, ok := a.skillGuidance[key]
		switch {
		case !ok:
			log.Printf("[dag] preflight: unknown skill %q, dropping", key)
		case !inGroups(card.Groups(), groups):
			log.Printf("[dag] preflight: card %s is in %v, none of them picked, dropping", key, card.Groups())
		case !slices.Contains(out, key):
			out = append(out, key)
		}
	}
	if len(groups) > 0 {
		for _, key := range a.guidanceKeys() {
			own := a.skillGuidance[key].Groups()
			if len(own) > 0 && inGroups(own, groups) && !slices.Contains(out, key) {
				out = append(out, key)
			}
		}
	}
	if len(out) > maxCards {
		log.Printf("[dag] preflight: %d cards, keeping the first %d", len(out), maxCards)
		out = out[:maxCards]
	}
	return out
}

// pickedGroups is what preflight picked for this run, or nil.
func pickedGroups(graph *Graph) []string {
	if graph == nil || graph.Preflight == nil {
		return nil
	}
	return graph.Preflight.Groups
}

// toolsInGroups keeps the tools that belong to the picked groups, in order.
func (a *Agent) toolsInGroups(names, groups []string) []string {
	if len(groups) == 0 {
		return names
	}
	kept := make([]string, 0, len(names))
	for _, name := range names {
		if tool, ok := a.registry.Get(name); ok && !inGroups(toolapi.ToolGroups(tool), groups) {
			continue
		}
		kept = append(kept, name)
	}
	return kept
}

// inFull reports whether the planner is shown name in full: it is listed, and
// it is in a picked group or none was picked.
func (a *Agent) inFull(graph *Graph, relevant []string, name string) bool {
	if !slices.Contains(relevant, name) {
		return false
	}
	return len(a.toolsInGroups([]string{name}, pickedGroups(graph))) == 1
}

// groupTools is the registered tools that name group g, sorted.
func (a *Agent) groupTools(g string) []string {
	var out []string
	if a.registry == nil {
		return nil
	}
	for _, name := range a.registry.List() {
		if tool, ok := a.registry.Get(name); ok && slices.Contains(toolapi.ToolGroups(tool), g) {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}
