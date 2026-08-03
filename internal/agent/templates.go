// Template references in plan parameters.
//
// A step's parameters may embed ${node.<id>.<path>} or ${step.<n>.<path>} to
// inject an earlier step's output. This file is the reference model: finding
// them, rewriting the step form into the node form once ids are known,
// deriving dependencies from them, and resolving them through a caller-supplied
// lookup.
//
// It is deliberately separate from how a VALUE is extracted from a node's
// result — that is the lookup's business, and lets a typed body answer a field
// access without this file knowing anything about it.

package agent

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// TemplateRef describes a single ${step.N…} or ${node.<id>…} reference
// found inside a plan step's params tree.
type TemplateRef struct {
	Kind   string   // "step" or "node"
	Index  int      // when Kind == "step"
	NodeID string   // when Kind == "node"
	Path   []string // dot-path tokens after the leading index/id; may be empty
	Raw    string   // original "${...}" text including the braces
}

// Key returns a stable identity for the reference's base (without the path),
// useful for collecting unique upstream dependencies.
func (r TemplateRef) Key() string {
	if r.Kind == "node" {
		return "node:" + r.NodeID
	}
	return "step:" + strconv.Itoa(r.Index)
}

// Lookup returns the parsed result associated with a reference's base
// (the step index or node ID). The walker handles path traversal itself,
// so the callback only needs to surface the root result.
type Lookup func(ref TemplateRef) (any, bool)

// templatePattern matches ${step.<body>} or ${node.<body>} placeholders.
// Body content stops at the first closing brace; anything else (alphanumerics,
// dashes, dots, digits) is permitted so opaque identifiers — a target name, a
// hash, a uuid — flow through unchanged.
var templatePattern = regexp.MustCompile(`\$\{(step|node)\.([^}]+)\}`)

// FindRefs recursively walks a JSON-shaped value and returns every template
// reference it finds inside string leaves. Map and slice containers are
// traversed; non-string scalars are ignored.
func FindRefs(v any) []TemplateRef {
	var out []TemplateRef
	walkRefs(v, &out)
	return out
}

func walkRefs(v any, out *[]TemplateRef) {
	switch x := v.(type) {
	case string:
		for _, m := range templatePattern.FindAllStringSubmatch(x, -1) {
			if ref := parseRef(m[0], m[1], m[2]); ref != nil {
				*out = append(*out, *ref)
			}
		}
	case map[string]any:
		for _, vv := range x {
			walkRefs(vv, out)
		}
	case []any:
		for _, vv := range x {
			walkRefs(vv, out)
		}
	}
}

func parseRef(raw, kind, body string) *TemplateRef {
	if body == "" {
		return nil
	}
	parts := strings.Split(body, ".")
	if len(parts) == 0 || parts[0] == "" {
		return nil
	}
	ref := &TemplateRef{Kind: kind, Raw: raw}
	switch kind {
	case "step":
		idx, err := strconv.Atoi(parts[0])
		if err != nil {
			return nil
		}
		ref.Index = idx
	case "node":
		ref.NodeID = parts[0]
	default:
		return nil
	}
	if len(parts) > 1 {
		ref.Path = parts[1:]
	}
	return ref
}

// ResolveTemplates walks v and substitutes every template reference using
// the supplied lookup. It returns a fresh value tree — input is not mutated.
//
// A bare placeholder (whole-string template) preserves the resolved value's
// underlying type, so objects and arrays pass through without stringification.
// Mid-string interpolation always produces a string.
func ResolveTemplates(v any, lookup Lookup) (any, error) {
	return resolveValue(v, lookup)
}

func resolveValue(v any, lookup Lookup) (any, error) {
	switch x := v.(type) {
	case string:
		return resolveString(x, lookup)
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, vv := range x {
			r, err := resolveValue(vv, lookup)
			if err != nil {
				return nil, err
			}
			out[k] = r
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i, vv := range x {
			r, err := resolveValue(vv, lookup)
			if err != nil {
				return nil, err
			}
			out[i] = r
		}
		return out, nil
	default:
		return v, nil
	}
}

func resolveString(s string, lookup Lookup) (any, error) {
	matches := templatePattern.FindAllStringSubmatchIndex(s, -1)
	if len(matches) == 0 {
		return s, nil
	}
	// Whole-string is a single placeholder → preserve type by returning the
	// resolved value directly instead of stringifying it.
	if len(matches) == 1 {
		m := matches[0]
		if m[0] == 0 && m[1] == len(s) {
			ref := parseRef(s, s[m[2]:m[3]], s[m[4]:m[5]])
			if ref == nil {
				return nil, fmt.Errorf("malformed template: %s", s)
			}
			val, err := resolveOne(*ref, lookup)
			if err != nil {
				return nil, err
			}
			return val, nil
		}
	}
	// Mid-string interpolation → string with substitutions.
	var b strings.Builder
	last := 0
	for _, m := range matches {
		b.WriteString(s[last:m[0]])
		raw := s[m[0]:m[1]]
		ref := parseRef(raw, s[m[2]:m[3]], s[m[4]:m[5]])
		if ref == nil {
			return nil, fmt.Errorf("malformed template: %s", raw)
		}
		val, err := resolveOne(*ref, lookup)
		if err != nil {
			return nil, err
		}
		b.WriteString(stringifyTemplateValue(val))
		last = m[1]
	}
	b.WriteString(s[last:])
	return b.String(), nil
}

func resolveOne(ref TemplateRef, lookup Lookup) (any, error) {
	base, ok := lookup(ref)
	if !ok {
		return nil, fmt.Errorf("unresolved template %s: upstream not available", ref.Raw)
	}
	if len(ref.Path) == 0 {
		return base, nil
	}
	v, ok := ResolvePath(base, ref.Path)
	if !ok {
		return nil, fmt.Errorf("unresolved template %s: path %q not found in upstream output", ref.Raw, strings.Join(ref.Path, "."))
	}
	return v, nil
}

// ResolvePath traverses a JSON-shaped value via dot-path tokens. Integer
// tokens index into slices; everything else looks up keys in maps. Returns
// (nil, false) on any miss or type mismatch.
func ResolvePath(root any, path []string) (any, bool) {
	cur := root
	for _, tok := range path {
		switch c := cur.(type) {
		case map[string]any:
			v, ok := c[tok]
			if !ok {
				return nil, false
			}
			cur = v
		case []any:
			idx, err := strconv.Atoi(tok)
			if err != nil || idx < 0 || idx >= len(c) {
				return nil, false
			}
			cur = c[idx]
		default:
			return nil, false
		}
	}
	return cur, true
}

// RewriteStepIndicesToNodeIDs walks v and rewrites every ${step.N…}
// reference to ${node.<nodeID>…} using the supplied index→ID map. Called
// at plan finalization, after the graph has assigned node IDs, so the
// dispatcher only ever resolves ${node…} refs at fire time.
//
// References whose index is out of range or maps to an empty ID are left
// unchanged — they will surface as unresolved-template errors at fire time.
// Returns a fresh value tree; the input is not mutated.
func RewriteStepIndicesToNodeIDs(v any, nodeIDs []string) any {
	return rewriteValue(v, nodeIDs)
}

// RewriteParamsIndicesToNodeIDs is a convenience wrapper for the common
// case of rewriting templates inside a node's Params map.
func RewriteParamsIndicesToNodeIDs(params map[string]any, nodeIDs []string) map[string]any {
	if params == nil {
		return nil
	}
	if rewritten, ok := rewriteValue(params, nodeIDs).(map[string]any); ok {
		return rewritten
	}
	return params
}

func rewriteValue(v any, nodeIDs []string) any {
	switch x := v.(type) {
	case string:
		return rewriteString(x, nodeIDs)
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, vv := range x {
			out[k] = rewriteValue(vv, nodeIDs)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, vv := range x {
			out[i] = rewriteValue(vv, nodeIDs)
		}
		return out
	default:
		return v
	}
}

func rewriteString(s string, nodeIDs []string) string {
	return templatePattern.ReplaceAllStringFunc(s, func(match string) string {
		sub := templatePattern.FindStringSubmatch(match)
		if len(sub) != 3 {
			return match
		}
		kind, body := sub[1], sub[2]
		if kind != "step" {
			return match
		}
		parts := strings.Split(body, ".")
		if len(parts) == 0 {
			return match
		}
		idx, err := strconv.Atoi(parts[0])
		if err != nil || idx < 0 || idx >= len(nodeIDs) || nodeIDs[idx] == "" {
			return match
		}
		var b strings.Builder
		b.WriteString("${node.")
		b.WriteString(nodeIDs[idx])
		for _, p := range parts[1:] {
			b.WriteString(".")
			b.WriteString(p)
		}
		b.WriteString("}")
		return b.String()
	})
}

// AutoDeriveTemplateDeps walks each step's params for ${step.N…} references
// and unions the referenced indices into that step's DependsOn. Returns an
// error if any template references a step outside the plan or itself —
// those are planner mistakes that would crash at fire time, so reject the
// plan up front rather than silently skipping.
//
// ${node.<id>…} references are not auto-derived; plan-time parsing has not
// yet assigned node IDs. The dispatcher resolves them at fire time.
func AutoDeriveTemplateDeps(steps []PlanStep) error {
	for i := range steps {
		refs := FindRefs(steps[i].Params)
		for _, ref := range refs {
			if ref.Kind != "step" {
				continue
			}
			if ref.Index < 0 || ref.Index >= len(steps) {
				return fmt.Errorf("step %d: template %s references step %d which does not exist (plan has %d steps)",
					i, ref.Raw, ref.Index, len(steps))
			}
			if ref.Index == i {
				return fmt.Errorf("step %d: template %s references itself", i, ref.Raw)
			}
			if !containsInt(steps[i].DependsOn, ref.Index) {
				steps[i].DependsOn = append(steps[i].DependsOn, ref.Index)
			}
		}
	}
	return nil
}

func containsInt(haystack []int, needle int) bool {
	for _, v := range haystack {
		if v == needle {
			return true
		}
	}
	return false
}

func stringifyTemplateValue(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		if x {
			return "true"
		}
		return "false"
	case float64:
		// JSON numbers come through as float64; format ints without decimals.
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'g', -1, 64)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		return string(b)
	}
}
