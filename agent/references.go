package agent

import (
	"encoding/json"
	"strings"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// Finding what a run referenced and never resolved, without knowing the domain.
//
// Some tool output is a fact and some of it is a handle on something else: a
// search result's URL, a record id, a machine name. A handle a run surfaced and never
// followed is the thing a model is most likely to talk about as though it had —
// citing a page it never opened, describing a record it never read.
//
// The engine cannot tell the two apart by looking. It used to decide by name:
// output whose Kind was "search" held URLs, and a Kind of "page" meant one had
// been read. That worked for two web tools and for nothing else, and it put the
// application's vocabulary inside the engine.
//
// A tool already publishes a schema for its output. Marking the field there says
// the same thing in the place that already describes the payload:
//
//	"url": {"type":"string", "x-reference":"web_fetch.url", …}
//
// The value is the tool that resolves the handle and the parameter it goes in,
// so a stage can act on the handle without the engine knowing either. Presence
// is what marks the field; the value is optional and only needed by a caller
// that wants to plan the follow-up rather than merely report it.
//
// Whether a handle was followed is read from the graph rather than declared: a
// value that appears in a later node's parameters was acted on. That needs no
// knowledge of which tool did it or what the value means.

// referenceAnnotation marks a schema property as a handle on something the run
// can retrieve, rather than a fact about what it already has.
const referenceAnnotation = "x-reference"

// reference is one handle a tool surfaced.
type reference struct {
	Value string // the handle itself, as it appeared in the payload
	Tool  string // the tool the producer says follows it, empty when undeclared
	Param string // the parameter the handle goes in, empty when undeclared
	Tag   string // the step that surfaced it
}

/*
 * collectReferences gathers every handle the run surfaced, from any tool.
 * desc: Walks each resolved tool node, reads its declared output schema, and
 *       pulls the values of properties marked with the reference annotation.
 *       A tool that declares no schema, or no reference fields, contributes
 *       nothing — so this is silent for every tool that has not opted in, and
 *       adding a tool needs no change here.
 * param: graph - the run so far.
 * return: the handles, in the order the steps ran, without duplicates.
 */
func (a *Agent) collectReferences(graph *Graph) []reference {
	if graph == nil || a == nil || a.registry == nil {
		return nil
	}
	var out []reference
	seen := map[string]bool{}
	for _, n := range graph.ResolvedByType(NodeTool) {
		tb, ok := n.Body.(toolMessageBody)
		if !ok {
			continue
		}
		env := tb.Envelope()
		if len(env.Data) == 0 {
			continue
		}
		skill, ok := a.registry.Get(n.ToolName)
		if !ok {
			continue
		}
		schema := toolapi.GetOutputSchema(skill)
		if schema == nil {
			continue
		}
		for _, path := range referencePaths(schema) {
			tool, param := splitResolver(path.resolvedBy)
			for _, v := range valuesAtPath(env.Data, path.path) {
				if v == "" || seen[v] {
					continue
				}
				seen[v] = true
				out = append(out, reference{Value: v, Tool: tool, Param: param, Tag: n.Tag})
			}
		}
	}
	return out
}

/*
 * unresolvedReferences returns the handles no later step acted on.
 * desc: A handle counts as followed when the tool its producer named was called
 *       with that value. Matching the tool matters: the first version matched
 *       the value against every parameter in the run, which is safe for a URL
 *       and wrong for a short identifier — a machine named "self" or a record id of
 *       "1" collides with an unrelated parameter and is reported as followed
 *       when nothing followed it.
 *
 *       A handle whose producer named no tool cannot be checked, so it is
 *       reported as outstanding. Over-reporting is the safe direction for a
 *       guard against claiming something was retrieved, and it gives a tool
 *       author a reason to name the resolver rather than only marking the field.
 * param: graph - the run so far.
 * return: the unfollowed handles, in the order they were surfaced.
 */
func (a *Agent) unresolvedReferences(graph *Graph) []reference {
	refs := a.collectReferences(graph)
	if len(refs) == 0 {
		return nil
	}
	byTool := valuesPassedToEachTool(graph)
	var out []reference
	for _, r := range refs {
		if r.Tool == "" || !byTool[r.Tool][r.Value] {
			out = append(out, r)
		}
	}
	return out
}

// splitResolver reads "tool.param" from the annotation. An annotation naming
// only a tool, or nothing at all, still marks the field — a handle is worth
// reporting even when nothing declared how to follow it.
func splitResolver(v string) (tool, param string) {
	if i := strings.LastIndex(v, "."); i > 0 {
		return v[:i], v[i+1:]
	}
	return v, ""
}

// schemaRef is a reference-marked property: where to find it, and what the
// producer says resolves it.
type schemaRef struct {
	path       []string // property names from the payload root, "" element = every array item
	resolvedBy string
}

// referencePaths walks a tool's output schema and returns the paths of every
// property carrying the reference annotation.
//
// The schema describes the ENVELOPE, so the walk starts at its data property —
// the payload is what a value is read from.
func referencePaths(schema json.RawMessage) []schemaRef {
	payload := toolapi.PayloadSchema(schema)
	if payload == nil {
		return nil
	}
	var data map[string]any
	if json.Unmarshal(payload, &data) != nil {
		return nil
	}
	var out []schemaRef
	walkSchemaForReferences(data, nil, &out)
	return out
}

func walkSchemaForReferences(node map[string]any, path []string, out *[]schemaRef) {
	if node == nil {
		return
	}
	if items, ok := node["items"].(map[string]any); ok {
		// An array: every element sits at the same path, marked by an empty
		// element so valuesAtPath knows to fan out rather than index.
		walkSchemaForReferences(items, append(append([]string{}, path...), ""), out)
	}
	props, _ := node["properties"].(map[string]any)
	for name, raw := range props {
		child, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		here := append(append([]string{}, path...), name)
		if v, marked := child[referenceAnnotation]; marked {
			resolver, _ := v.(string)
			*out = append(*out, schemaRef{path: here, resolvedBy: resolver})
			continue // a reference is a leaf; do not descend into it
		}
		walkSchemaForReferences(child, here, out)
	}
}

// valuesAtPath pulls every string at a path out of a payload. An empty path
// element means "each element of this array", so one path can yield many values.
func valuesAtPath(payload json.RawMessage, path []string) []string {
	var v any
	if json.Unmarshal(payload, &v) != nil {
		return nil
	}
	return descend(v, path)
}

func descend(v any, path []string) []string {
	if len(path) == 0 {
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			return []string{strings.TrimSpace(s)}
		}
		return nil
	}
	if path[0] == "" {
		arr, ok := v.([]any)
		if !ok {
			return nil
		}
		var out []string
		for _, item := range arr {
			out = append(out, descend(item, path[1:])...)
		}
		return out
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	return descend(obj[path[0]], path[1:])
}

// valuesPassedToEachTool returns, per tool name, every string that tool was
// called with at any depth. Keyed by tool so a handle is only counted as
// followed by the tool its producer named, not by whatever else in the run
// happened to be passed the same short string.
//
// The value may sit anywhere in the parameters rather than only in the declared
// one: the producer names a parameter so a follow-up can be PLANNED, and a plan
// that put the handle somewhere else in the same tool's call still followed it.
//
// Every node counts, not only the resolved ones: a step that was planned to
// follow a handle and then failed still means the run did not overlook it, and
// its failure is already reported by the coverage edge.
func valuesPassedToEachTool(graph *Graph) map[string]map[string]bool {
	byTool := map[string]map[string]bool{}
	graph.mu.RLock()
	defer graph.mu.RUnlock()
	for _, n := range graph.nodes {
		if n.ToolName == "" {
			continue
		}
		if byTool[n.ToolName] == nil {
			byTool[n.ToolName] = map[string]bool{}
		}
		collectParamStrings(n.Params, byTool[n.ToolName])
	}
	return byTool
}

func collectParamStrings(v any, into map[string]bool) {
	switch t := v.(type) {
	case string:
		if s := strings.TrimSpace(t); s != "" {
			into[s] = true
		}
	case map[string]any:
		for _, item := range t {
			collectParamStrings(item, into)
		}
	case []any:
		for _, item := range t {
			collectParamStrings(item, into)
		}
	}
}

/*
 * chainHints renders a tool's declared handles as wiring instructions for the
 * planner.
 * desc: The same declaration the edges read afterwards, stated forwards. A
 *       tool that marks a field as a handle and names what follows it has said
 *       everything the planner needs to wire the two steps together, so the
 *       line is generated from that rather than written again by hand in a
 *       description — where the two could drift apart, and one of them would be
 *       wrong.
 *
 *       Nothing is named here. The sentence is fixed and every noun in it comes
 *       from the tool: run against a fleet tool it reads
 *       "${step.N.peers.0.id} into inspect_host(target)".
 * param: schema - the tool's declared output schema.
 * return: one line per handle, empty when the tool declares none.
 */
func chainHints(schema json.RawMessage) []string {
	var out []string
	for _, ref := range referencePaths(schema) {
		if ref.resolvedBy == "" {
			continue // marked as a handle, but nothing said what follows it
		}
		tool, param := splitResolver(ref.resolvedBy)
		target := tool
		if param != "" {
			target = tool + "(" + param + ")"
		}
		out = append(out, "${step.N."+renderPath(ref.path)+"} into "+target)
	}
	sortStrings(out)
	return out
}

// renderPath writes a schema path the way the planner writes a template: an
// array element becomes a concrete index, since the planner has to pick one.
func renderPath(path []string) string {
	parts := make([]string, 0, len(path))
	for _, p := range path {
		if p == "" {
			parts = append(parts, "0")
			continue
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, ".")
}
