package llm

import "fmt"

/*
 * Walking a schema document.
 *
 * Two things are done with a schema here: it is CLOSED so a provider can
 * enforce it, and it is CHECKED so we can say whether a provider would accept
 * it. Both have to reach every object in the document, and they were written as
 * two separate walks over the same tree.
 *
 * They then diverged. The checker learned to follow anyOf, oneOf, allOf and
 * $defs; the closer never did. A plan schema whose per-tool branches hung off
 * anyOf was closed at the array above them and left open in every branch below,
 * shipped with strict set, and refused by the provider — after which the client
 * reads the refusal as a missing capability and stops asking that model for
 * schemas at all. The checker could see a fault the closer had made, and
 * nothing put the two together.
 *
 * So the walk lives here, once. A new kind of link is taught to it in one
 * place, and both callers get it.
 */

// maxSchemaDepth is the deepest nesting the walk will follow.
//
// The documents built in this package are three or four levels; a tool's
// Parameters() comes from a plugin or a SKILL.md and is not ours. A schema
// deeper than this is not closed rather than being followed forever, and
// StrictProblems then refuses to call it strict, which is the safe direction.
const maxSchemaDepth = 64

/*
 * eachSchemaNode visits every node in a schema document, parents first.
 * desc: A node is reached through any of the links a schema may use to nest:
 *
 *         properties            each declared field
 *         items                 an array's element shape
 *         prefixItems           a tuple's positional shapes
 *         anyOf, oneOf, allOf   each alternative shape
 *         not                   the negated shape
 *         $defs, definitions    named shapes
 *         additionalProperties  when it holds a schema rather than a bool
 *
 *       Following some and not others reaches part of a document and stops,
 *       which is the whole reason this exists.
 *
 *       $ref is deliberately NOT followed. Resolving one needs a base document
 *       and a cycle guard, and neither caller has anything sensible to do with
 *       a reference it cannot see through — the checker reports it and the call
 *       declines strict, which is honest. Following it halfway would be worse.
 * param: node - the document, or any node within it.
 * param: path - where this node sits, for a caller that reports positions.
 * param: visit - called once per map node, before its children.
 * return: whether the walk stopped early at the depth cap, leaving part of the
 *         document unvisited. The checker turns that into a refusal: the cap is
 *         shared with the closer, so anything it hides is hidden from BOTH, and
 *         a document whose deep nodes were neither closed nor checked would
 *         otherwise pass the guard and go out claiming strict.
 */
func eachSchemaNode(node any, path string, visit func(path string, m map[string]any)) (truncated bool) {
	return walkSchema(node, path, visit, 0)
}

func walkSchema(node any, path string, visit func(string, map[string]any), depth int) (truncated bool) {
	m, ok := node.(map[string]any)
	if !ok {
		return false
	}
	if depth > maxSchemaDepth {
		return true
	}
	visit(path, m)

	if props, ok := m["properties"].(map[string]any); ok {
		for k, v := range props {
			truncated = walkSchema(v, path+"."+k, visit, depth+1) || truncated
		}
	}
	// items is one shape for every element. The array form is JSON Schema's
	// older tuple syntax; both are followed so neither hides a node.
	switch items := m["items"].(type) {
	case map[string]any:
		truncated = walkSchema(items, path+"[]", visit, depth+1) || truncated
	case []any:
		for i, it := range items {
			truncated = walkSchema(it, fmt.Sprintf("%s[%d]", path, i), visit, depth+1) || truncated
		}
	}
	if pre, ok := m["prefixItems"].([]any); ok {
		for i, it := range pre {
			truncated = walkSchema(it, fmt.Sprintf("%s[%d]", path, i), visit, depth+1) || truncated
		}
	}
	for _, kind := range []string{"anyOf", "oneOf", "allOf"} {
		branches, ok := m[kind].([]any)
		if !ok {
			continue
		}
		for i, b := range branches {
			truncated = walkSchema(b, fmt.Sprintf("%s.%s[%d]", path, kind, i), visit, depth+1) || truncated
		}
	}
	if n, ok := m["not"].(map[string]any); ok {
		truncated = walkSchema(n, path+".not", visit, depth+1) || truncated
	}
	for _, kind := range []string{"$defs", "definitions"} {
		defs, ok := m[kind].(map[string]any)
		if !ok {
			continue
		}
		for name, d := range defs {
			truncated = walkSchema(d, fmt.Sprintf("%s.%s.%s", path, kind, name), visit, depth+1) || truncated
		}
	}
	// A map whose keys nobody declared is a node in its own right: strict cannot
	// express one, and the checker has to be able to say so.
	if ap, ok := m["additionalProperties"].(map[string]any); ok {
		truncated = walkSchema(ap, path+".<key>", visit, depth+1) || truncated
	}
	return truncated
}

// declaresProperties reports whether a node lists the keys it permits.
//
// The distinction the closer turns on. An object WITH a property list can be
// closed: name the keys, forbid the rest. An object without one is either a map
// whose keys are not known in advance — a tool's parameters, chosen by the tool
// a sibling field names — or a bare {"type":"object"}. Closing either says "no
// key is permitted at all", so the only legal value becomes {}, which is not a
// weaker constraint than intended but a stronger and useless one. Anthropic
// honoured exactly that reading and answered a forced tool call with empty
// params.
func declaresProperties(m map[string]any) bool {
	props, ok := m["properties"].(map[string]any)
	return ok && len(props) > 0
}
