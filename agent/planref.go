package agent

import "github.com/Compdeep/kaiju/agent/toolapi"

// A reference from one step to another, as the plan declares it.
//
// It used to be a string: "${step.0.content}", written inside an untyped map.
// The provider saw a string, so nothing about it could be checked at the point
// it was produced — not that the step existed, not that the field did, not that
// the index was even a number. Every guarantee had to be rebuilt afterwards by
// hand, which is what validatePlanReferences is, and what it missed for as long
// as it looked in the wrong half of a schema.
//
// As a declared shape the provider rejects what it cannot fill, and the step
// naming survives a plan changing shape — a position does not.

// Ref names another step's output.
//
// Step is the step's name, never its position: positions are counted from the
// first step of the plan they are in, so dropping a step renumbers everything
// after it and a reference that was right becomes a reference to its neighbour,
// or to itself.
//
// Field is a dot-path into that step's output. Empty means the whole result.
type Ref struct {
	Step  string `json:"step" desc:"the tag of an earlier step in this plan, whose output this reads"`
	Field string `json:"field,omitempty" desc:"a dot-path into that step's output — results.0.url reaches into a list; omit it to pass the whole result"`
}

// RefSchema is the shape a reference takes, for the plan schema the planner is
// given.
//
// Derived from the struct above rather than written beside it. A shape stated
// twice is a shape that drifts: the planner is told to produce one thing, Go
// looks for another, and the value is not lost loudly — the reference is simply
// not recognised, so it reaches the tool as a literal object where a value
// belonged. Nothing errors at any layer, which is the failure this whole path
// was built to stop.
//
// The same derivation every tool's output schema already uses.
func RefSchema() string { return toolapi.PayloadSchemaOf(Ref{}) }

// refFrom reads a reference out of a decoded parameter value.
//
// Params arrive as map[string]any from JSON, so a reference is a map with a
// "step" — the shape, not the Go type. A map with no "step" is an ordinary
// nested parameter and is left alone.
func refFrom(v any) (Ref, bool) {
	m, ok := v.(map[string]any)
	if !ok {
		return Ref{}, false
	}
	step, ok := m["step"].(string)
	if !ok || step == "" {
		return Ref{}, false
	}
	field, _ := m["field"].(string)
	// A map carrying anything besides step and field is a parameter that
	// happens to have a "step" in it, not a reference.
	for k := range m {
		if k != "step" && k != "field" {
			return Ref{}, false
		}
	}
	return Ref{Step: step, Field: field}, true
}

// template renders a reference in the form the dispatcher already resolves, so
// everything downstream of the plan is untouched by the change of shape.
func (r Ref) template(nodeID string) string {
	if r.Field == "" {
		return "${node." + nodeID + "}"
	}
	return "${node." + nodeID + "." + r.Field + "}"
}

// walkRefValues visits every declared reference in a parameter tree, replacing
// each with whatever replace returns.
//
// Separate from walkParams, which only ever sees strings: it walks INTO a map
// rather than offering it, so a reference — which is a map — could never be
// replaced by it, only descended into. Separate from walkRefs in templates.go,
// which finds the STRING form, "${step.0.content}". Two forms, two walkers,
// while both are accepted.
func walkRefValues(v any, replace func(Ref) (any, bool)) {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if r, ok := refFrom(val); ok {
				if out, ok := replace(r); ok {
					x[k] = out
				}
				continue
			}
			walkRefValues(val, replace)
		}
	case []any:
		for i, val := range x {
			if r, ok := refFrom(val); ok {
				if out, ok := replace(r); ok {
					x[i] = out
				}
				continue
			}
			walkRefValues(val, replace)
		}
	}
}

// refsIn returns every reference in a parameter tree, without changing it.
func refsIn(params map[string]any) []Ref {
	var out []Ref
	walkRefValues(params, func(r Ref) (any, bool) {
		out = append(out, r)
		return nil, false
	})
	return out
}
