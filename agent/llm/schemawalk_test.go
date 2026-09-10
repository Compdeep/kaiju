package llm

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
)

// visited returns every path the walk reached, sorted.
func visited(t *testing.T, doc string) []string {
	t.Helper()
	var root any
	if err := json.Unmarshal([]byte(doc), &root); err != nil {
		t.Fatalf("test document is not JSON: %v", err)
	}
	var seen []string
	eachSchemaNode(root, "", func(p string, _ map[string]any) { seen = append(seen, p) })
	sort.Strings(seen)
	return seen
}

func reaches(t *testing.T, doc, path string) bool {
	t.Helper()
	for _, p := range visited(t, doc) {
		if p == path {
			return true
		}
	}
	return false
}

// Every kind of link a schema nests through is followed.
//
// Following some and not others reaches part of a document and stops, and does
// so silently. That is what happened: the closer followed properties and
// items.properties, a plan step's per-tool shapes hung off anyOf, and every
// branch below was left open inside a document labelled strict.
func TestEachSchemaNode_FollowsEveryKindOfLink(t *testing.T) {
	for _, c := range []struct{ name, doc, want string }{
		{
			"properties",
			`{"type":"object","properties":{"a":{"type":"object"}}}`,
			".a",
		},
		{
			"items",
			`{"type":"object","properties":{"xs":{"type":"array","items":{"type":"object"}}}}`,
			".xs[]",
		},
		{
			"anyOf",
			`{"type":"object","properties":{"s":{"anyOf":[{"type":"object"},{"type":"object"}]}}}`,
			".s.anyOf[1]",
		},
		{
			"oneOf",
			`{"type":"object","properties":{"s":{"oneOf":[{"type":"object"}]}}}`,
			".s.oneOf[0]",
		},
		{
			"allOf",
			`{"type":"object","properties":{"s":{"allOf":[{"type":"object"}]}}}`,
			".s.allOf[0]",
		},
		{
			"$defs",
			`{"type":"object","$defs":{"Step":{"type":"object"}}}`,
			".$defs.Step",
		},
		{
			"definitions",
			`{"type":"object","definitions":{"Step":{"type":"object"}}}`,
			".definitions.Step",
		},
		{
			"not",
			`{"type":"object","properties":{"s":{"not":{"type":"object"}}}}`,
			".s.not",
		},
		{
			"prefixItems",
			`{"type":"object","properties":{"xs":{"type":"array","prefixItems":[{"type":"object"}]}}}`,
			".xs[0]",
		},
		{
			"tuple items",
			`{"type":"object","properties":{"xs":{"type":"array","items":[{"type":"object"}]}}}`,
			".xs[0]",
		},
		{
			"additionalProperties as a schema",
			`{"type":"object","additionalProperties":{"type":"object"}}`,
			".<key>",
		},
		{
			"nested through two link kinds",
			`{"type":"object","properties":{"s":{"type":"array","items":{"anyOf":[{"type":"object","properties":{"p":{"type":"object"}}}]}}}}`,
			".s[].anyOf[0].p",
		},
	} {
		if !reaches(t, c.doc, c.want) {
			t.Errorf("%s: the walk never reached %s — visited %v", c.name, c.want, visited(t, c.doc))
		}
	}
}

// The document is walked once per node, not once per route to it.
func TestEachSchemaNode_VisitsRootAndDoesNotRepeat(t *testing.T) {
	seen := visited(t, `{"type":"object","properties":{"a":{"type":"object"},"b":{"type":"object"}}}`)
	if len(seen) != 3 {
		t.Errorf("visited %v, want the root and its two properties", seen)
	}
	if seen[0] != "" {
		t.Errorf("the root was not visited: %v", seen)
	}
}

// A document deeper than the cap stops rather than running out of stack. A
// tool's Parameters() comes from a plugin or a SKILL.md and is not ours.
func TestEachSchemaNode_StopsAtTheDepthCap(t *testing.T) {
	var b strings.Builder
	for i := 0; i < maxSchemaDepth+20; i++ {
		b.WriteString(`{"type":"object","properties":{"n":`)
	}
	b.WriteString(`{"type":"string"}`)
	for i := 0; i < maxSchemaDepth+20; i++ {
		b.WriteString(`}}`)
	}
	seen := visited(t, b.String()) // must return rather than exhaust the stack
	if len(seen) > maxSchemaDepth+2 {
		t.Errorf("the walk followed %d levels, past the cap of %d", len(seen), maxSchemaDepth)
	}
}

// The closer and the checker walk the same document by the same route.
//
// They were two walks and they diverged: the checker learned anyOf and the
// closer did not, so the checker could see faults the closer had made. Anything
// the closer can close, the checker must then find nothing wrong with.
func TestCloserAndChecker_AgreeOnTheSameDocument(t *testing.T) {
	doc := `{
	  "type":"object",
	  "properties":{
	    "steps":{"type":"array","items":{"anyOf":[
	      {"type":"object","required":["tool"],"properties":{
	         "tool":{"const":"bash"},
	         "params":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}}},
	      {"type":"object","required":["tool"],"properties":{
	         "tool":{"const":"web_fetch"},
	         "params":{"type":"object","properties":{"url":{"type":"string"}},"required":["url"]}}}
	    ]}}
	  },
	  "required":["steps"]
	}`
	closed := closedSchema(json.RawMessage(doc))
	if closed == nil {
		t.Fatal("a closeable document was refused")
	}
	if p := StrictProblems(closed); len(p) > 0 {
		t.Errorf("the closer left %d thing(s) the checker refuses:", len(p))
		for _, x := range p {
			t.Errorf("  %s", x)
		}
	}
}

// A document deeper than the cap is refused, not sent half-closed.
//
// The cap is shared by the closer and the checker, so anything it hides is
// hidden from both — and the guard in asSchemaRequest, which asks the checker
// whether the closed document is acceptable, would have been told yes on the
// strength of the shallow half alone. That is the exact shape of the fault this
// walk exists to prevent, arriving through the walk itself.
func TestEachSchemaNode_ReportsWhenItGivesUp(t *testing.T) {
	var b strings.Builder
	depth := maxSchemaDepth + 10
	for i := 0; i < depth; i++ {
		b.WriteString(`{"type":"object","properties":{"n":`)
	}
	b.WriteString(`{"type":"object","properties":{"deep":{"type":"string"}}}`)
	for i := 0; i < depth; i++ {
		b.WriteString(`}}`)
	}
	doc := b.String()

	var root any
	if err := json.Unmarshal([]byte(doc), &root); err != nil {
		t.Fatalf("test document is not JSON: %v", err)
	}
	if !eachSchemaNode(root, "", func(string, map[string]any) {}) {
		t.Fatal("the walk stopped at the cap and did not say so")
	}

	closed := closedSchema(json.RawMessage(doc))
	if closed == nil {
		return // refused earlier, which is also safe
	}
	if len(StrictProblems(closed)) == 0 {
		t.Error("a document the walk could not finish was reported as strict-clean")
	}

	req := &ChatRequest{
		Tools:      []ToolDef{{Type: "function", Function: FunctionDef{Name: "deep", Parameters: json.RawMessage(doc)}}},
		ToolChoice: ForceToolChoice("deep"),
	}
	if asSchemaRequest(req, ProviderOpenAI, "gpt-4o") != nil {
		t.Error("a partly-closed document was sent claiming strict")
	}
}

// A document within the cap is unaffected: the guard must refuse what it cannot
// examine, not everything.
func TestEachSchemaNode_DoesNotReportOnAnOrdinaryDocument(t *testing.T) {
	var root any
	_ = json.Unmarshal([]byte(`{"type":"object","properties":{"a":{"type":"object","properties":{"b":{"type":"string"}}}}}`), &root)
	if eachSchemaNode(root, "", func(string, map[string]any) {}) {
		t.Error("a three-level document was reported as too deep")
	}
}
