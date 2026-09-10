package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Compdeep/kaiju/agent/llm"
	"github.com/Compdeep/kaiju/agent/toolapi"
)

/*
 * planStepBranches describes each tool's own parameters, so the shape the
 * planner is held to knows what a step of that tool looks like.
 * desc: params cannot be described in one object. It carries whatever the NAMED
 *       tool's signature needs, and the tool is chosen by a sibling field — so a
 *       single object can only say "anything", and that is what it said.
 *
 *       Every model resolved that differently and all of them resolved it
 *       wrong. On one live run the planner emitted three steps calling
 *       clipboard — a real tool — with city_list, endpoint and format as its
 *       parameters, none of which it takes. Detecting that afterwards cost a
 *       76-second correction re-sending the whole 51KB prompt. A branch per tool
 *       makes it unrepresentable instead: the model picks a branch by writing
 *       the tool's name, and that same act binds params to that tool's
 *       parameters, in the same object, so there is no moment where the choice
 *       is known and the shape is not.
 *
 *       Built from the tools this run was SHOWN, not from the registry. A
 *       branch for a tool the planner cannot see would let it name one it was
 *       never offered, which is the narrowing undone.
 * param: names - the tools shown to the planner, in the order shown.
 * param: registry - where each tool's own parameter schema comes from.
 * return: the JSON for steps.items, and false when the open shape must be used.
 */
func planStepBranches(names []string, registry *toolapi.Registry) (json.RawMessage, bool) {
	if len(names) == 0 || registry == nil {
		return nil, false
	}
	var branches []string
	for _, name := range names {
		tool, ok := registry.Get(name)
		if !ok {
			continue
		}
		params := tool.Parameters()
		if !closeableParams(params) {
			// One tool that strict cannot express costs the whole document,
			// because strict is all-or-nothing per schema. Leaving that tool out
			// instead would be worse: the model could not name it at all, and a
			// plan that needs it could not be written.
			//
			// So the whole plan falls back to the open shape, the guard in
			// asSchemaRequest declines to claim strict, and the boot log names
			// the stage. Visible, and the tool keeps working.
			return nil, false
		}
		nameJSON, err := json.Marshal(name)
		if err != nil {
			return nil, false
		}
		branches = append(branches, fmt.Sprintf(`{
			"type": "object",
			"required": ["tool", "params", "tag"],
			"properties": {
				"tool": {"const": %s},
				"params": %s,
				"tag": {"type": "string", "description": "This step's name, unique within the plan: letters, digits, _ or - with no spaces. Other steps reference this step by it."},
				"type": {"type": "string", "enum": ["tool","compute"]},
				"depends_on": {"type": "array", "items": {"type": "integer"}}
			}
		}`, nameJSON, params))
	}
	if len(branches) == 0 {
		return nil, false
	}
	return json.RawMessage("{\"anyOf\": [" + strings.Join(branches, ",") + "]}"), true
}

/*
 * closeableParams reports whether a tool's parameters can be carried by strict.
 * desc: Asked of the real closer and the real checker rather than guessed at.
 *
 *       This used to be a keyword test — does the document mention type, or
 *       properties, or required — which passes {"type":"object"} while saying
 *       nothing about shape, and passes a map with undeclared keys, which strict
 *       cannot express at all. Both would have gone into a branch that looked
 *       specific and constrained nothing.
 *
 *       The question is not whether the schema is well formed. It is whether
 *       this exact document survives the rewrite that sends it, and the two
 *       functions that decide are the ones asked.
 * param: raw - the tool's declared parameter schema.
 * return: whether a branch built around it could be enforced.
 */
func closeableParams(raw json.RawMessage) bool {
	if len(raw) == 0 || !json.Valid(raw) {
		return false
	}

	// It has to say it is a params object. An empty document is valid JSON and
	// valid JSON Schema, and it means "any value at all" — strict has no
	// objection to it, because it never claims to be an object, so the checker
	// alone lets it through. A branch carrying one is exactly as permissive as
	// the open shape it was meant to replace, written per tool and harder to
	// see because the schema now looks specific.
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return false
	}
	var declaredType string
	if t, ok := doc["type"]; ok {
		_ = json.Unmarshal(t, &declaredType)
	}
	if declaredType != "object" {
		return false
	}
	// Wrapped as a branch is, because a schema is judged in the position it
	// occupies: params sits under an object that declares it, and closing walks
	// down into it from there.
	wrapped := json.RawMessage(fmt.Sprintf(
		`{"type":"object","required":["tool","params"],"properties":{"tool":{"type":"string"},"params":%s}}`,
		string(raw)))
	sent := llm.SchemaAsSent(llm.ToolDef{Function: llm.FunctionDef{Name: "probe", Parameters: wrapped}})
	if sent == nil {
		return false
	}
	return len(llm.StrictProblems(sent)) == 0
}
