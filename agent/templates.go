// Locating template references in plan parameters.
//
// A step's parameters may embed ${node.<id>.<path>} or ${step.<n>.<path>} to
// inject an earlier step's output. This answers one question about them: which
// ones are there. Asking beats scanning for the substring "${node." — that
// accepts a placeholder only half written, which passes validation, matches no
// pattern at fire time, and reaches the tool as literal text.
//
// It once resolved and rewrote them too, through a caller-supplied lookup, and
// that half was a second implementation of what dispatcher.go and executive.go
// already do for real. Two models of one thing is worse than either, so it is
// gone; substituteTemplates and rewriteStepTemplates are where those jobs
// live.

package agent

import (
	"regexp"
	"strconv"
	"strings"
)

// TemplateRef describes a single ${step.N…} or ${node.<id>…} reference
// found inside a plan step's params tree.
type TemplateRef struct {
	Type   string   // "step" or "node"
	Index  int      // when Type == "step" and it named a position
	Tag    string   // when Type == "step" and it named a tag instead
	NodeID string   // when Type == "node"
	Path   []string // dot-path tokens after the leading index/id; may be empty
	Raw    string   // original "${...}" text including the braces
}

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
	ref := &TemplateRef{Type: kind, Raw: raw}
	switch kind {
	case "step":
		// A position or a tag. Returning nothing for a tag made the reference
		// indistinguishable from ordinary prose, so it survived every check that
		// looks for references and was handed to a model as text — which read it
		// as a data path and looked the value up under keys that do not exist.
		if idx, err := strconv.Atoi(parts[0]); err == nil {
			ref.Index = idx
		} else {
			ref.Tag = parts[0]
			ref.Index = -1
		}
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
