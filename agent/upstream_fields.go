package agent

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// A stage that receives values wired from earlier steps is shown the values and
// nothing about them. It sees a key holding a string and has to work out what
// the string is for. Meanwhile the tool that produced it already says, in its
// own output schema, what each of its fields is — and that description reaches
// the planner and stops there.
//
// This carries those descriptions to whoever is about to use the values. It
// names no tool and no field: everything comes from what a tool declares and
// from which keys are actually present, so a tool added tomorrow is described
// on the same terms as one added today, and a tool that declares no output
// schema is simply not described.

/*
 * upstreamFieldMeanings describes the context's fields using the output schemas
 * of the tools that produced them.
 * desc: The Coder writes a script against values it has never had described. It
 *       sees a key holding a string and has to guess what the string is for. In
 *       one run it was handed a page's shortened text beside the path of the
 *       file holding all of it, read the shortened text, and counted inside
 *       0.66% of a document — while the tool's own schema said, in words, which
 *       field was cut down and which was whole. That sentence existed and
 *       stopped at the planner.
 *
 *       Only fields actually present in the context are described, so a tool's
 *       whole schema is not pasted in, and only the tools this step depends on
 *       are consulted.
 * param: graph - the run graph, for looking up the producing nodes
 * param: n - this compute node, for its dependencies
 * param: ctxData - the context about to be shown to the Coder
 * return: a markdown block, or "" when nothing can be described
 */
func (a *Agent) upstreamFieldMeanings(graph *Graph, n *Node, ctxData any) string {
	if graph == nil || n == nil || a.registry == nil || ctxData == nil {
		return ""
	}
	present := map[string]bool{}
	collectContextKeys(ctxData, present)
	if len(present) == 0 {
		return ""
	}

	var sb strings.Builder
	described := map[string]bool{}
	for _, depID := range n.DependsOn {
		dep := graph.Get(depID)
		if dep == nil || dep.ToolName == "" || described[dep.ToolName] {
			continue
		}
		skill, known := a.registry.Get(dep.ToolName)
		if !known {
			continue
		}
		outSchema := toolapi.GetOutputSchema(skill)
		if outSchema == nil {
			continue
		}
		// A tool's fields sit under the envelope's data, which is also where a
		// ${step.N.field} reference resolves — so describe the same level.
		fields := envelopeData(outSchema)
		if fields == nil {
			fields = outSchema
		}
		var parsed struct {
			Properties map[string]struct {
				Description string `json:"description"`
			} `json:"properties"`
		}
		if json.Unmarshal(fields, &parsed) != nil {
			continue
		}
		names := make([]string, 0, len(parsed.Properties))
		for name := range parsed.Properties {
			names = append(names, name)
		}
		sort.Strings(names)

		var lines []string
		for _, name := range names {
			desc := strings.TrimSpace(parsed.Properties[name].Description)
			if !present[name] || desc == "" {
				continue
			}
			lines = append(lines, fmt.Sprintf("- `%s` — %s", name, desc))
		}
		if len(lines) > 0 {
			described[dep.ToolName] = true
			sb.WriteString(fmt.Sprintf("\nFrom `%s`:\n%s\n", dep.ToolName, strings.Join(lines, "\n")))
		}
	}
	return sb.String()
}

// collectContextKeys gathers every key name in the context, at any depth, so a
// field described by a tool can be matched wherever the planner wired it.
func collectContextKeys(v any, into map[string]bool) {
	switch t := v.(type) {
	case map[string]any:
		for k, inner := range t {
			into[k] = true
			collectContextKeys(inner, into)
		}
	case []any:
		for _, inner := range t {
			collectContextKeys(inner, into)
		}
	}
}
