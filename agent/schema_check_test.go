package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/llm"
)

// The stages that can be enforced, named one by one.
//
// Pinned rather than counted. A count going from eleven to ten says something
// changed and not which stage stopped being checked, and the whole point of the
// check is that nothing in a reply tells you.
//
// A stage added to StageSchemas and not to this list fails here, which is the
// reminder to look at its schema before it ships.
func TestStageSchemasAreEnforceableOrKnownNotToBe(t *testing.T) {
	clean := map[string]bool{
		"architect":    true,
		"coder(edit)":  true,
		"coder(write)": true,
		"curator":      true,
		"group_review": true,
		"debugger":     true,
		"holmes":       true,
		"observer":     true,
		"plan":         true,
		"preflight":    true,
		"reflector":    true,
		"route":        true,
	}
	// Nothing is open now. The map is kept rather than deleted: it is where a
	// stage goes when its schema cannot be closed yet, and an empty one says
	// there is no such stage rather than that nobody looked.
	knownOpen := map[string]bool{}

	a := newSchemaTestAgent(t)
	seen := map[string]bool{}
	for _, s := range a.StageSchemas() {
		seen[s.Stage] = true
		if !s.Converts {
			t.Errorf("%s no longer converts to a schema request, so strict does not apply to it at all", s.Stage)
			continue
		}
		if knownOpen[s.Stage] {
			if len(s.Problems) == 0 {
				t.Errorf("%s is enforceable now — move it into the clean list", s.Stage)
			}
			continue
		}
		if !clean[s.Stage] {
			t.Errorf("%s is new here — check its schema against strict mode and add it to the list", s.Stage)
			continue
		}
		if len(s.Problems) > 0 {
			t.Errorf("%s was enforceable and is not any more:", s.Stage)
			for _, p := range s.Problems {
				t.Errorf("    %s", p)
			}
		}
	}
	for name := range clean {
		if !seen[name] {
			t.Errorf("%s is no longer a stage that asks for one shape — was it removed, or does it offer tools now?", name)
		}
	}
}

// The plan schema was the last one that could not be enforced, and the reason
// is worth keeping: a step's params is a map whose keys are whatever the named
// tool takes, and a strict decoder cannot express a map with undeclared keys.
// Closing it would have forbidden the planner from passing any parameter.
//
// So params travels as a string holding the object. This test holds the shape
// that made it closable, because the object form is the obvious thing for
// someone to restore without knowing what it costs.
func TestThePlanSchemaCarriesParamsAsAString(t *testing.T) {
	a := newSchemaTestAgent(t)

	var plan *StageSchema
	for _, s := range a.StageSchemas() {
		if s.Stage == "plan" {
			cp := s
			plan = &cp
		}
	}
	if plan == nil {
		t.Fatal("there is no plan stage")
	}
	if len(plan.Problems) > 0 {
		t.Errorf("the plan schema stopped being enforceable:")
		for _, p := range plan.Problems {
			t.Errorf("    %s", p)
		}
	}

	var schema struct {
		Properties struct {
			Steps struct {
				Items struct {
					Properties struct {
						Params struct {
							Type string `json:"type"`
						} `json:"params"`
					} `json:"properties"`
				} `json:"items"`
			} `json:"steps"`
		} `json:"properties"`
	}
	raw := llm.SchemaAsSent(a.executivePlanSchema())
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("plan schema does not parse: %v", err)
	}
	if got := schema.Properties.Steps.Items.Properties.Params.Type; got != "string" {
		t.Errorf("params is declared %q — as an object it is a map with undeclared keys, "+
			"which reopens the schema and takes the enforcement with it", got)
	}
}

// Both shapes decode. The string is what the schema asks for; the object is
// what a tool call carries and what Anthropic still sends, since that provider
// is never converted.
func TestAPlanStepTakesParamsEitherWay(t *testing.T) {
	var asString PlanStep
	if err := json.Unmarshal([]byte(`{"tool":"file_write","tag":"w",
		"params":"{\"path\":\"/tmp/uchan\",\"content\":\"\"}"}`), &asString); err != nil {
		t.Fatalf("a string params did not decode: %v", err)
	}
	if asString.Params["path"] != "/tmp/uchan" {
		t.Errorf("path did not survive the string: %#v", asString.Params)
	}
	if asString.Tool != "file_write" || asString.Tag != "w" {
		t.Errorf("the other fields were lost unwrapping params: %+v", asString)
	}

	var asObject PlanStep
	if err := json.Unmarshal([]byte(`{"tool":"file_write","tag":"w",
		"params":{"path":"/tmp/uchan"}}`), &asObject); err != nil {
		t.Fatalf("an object params did not decode: %v", err)
	}
	if asObject.Params["path"] != "/tmp/uchan" {
		t.Errorf("the object form broke: %#v", asObject.Params)
	}
}

// A reference has to survive the params string character for character. One
// escape lost on the way through and it names no step, so the wiring is lost.
func TestAReferenceSurvivesTheParamsString(t *testing.T) {
	var step PlanStep
	if err := json.Unmarshal([]byte(`{"tool":"web_fetch","tag":"read",
		"params":"{\"url\":\"${step.find_news.results.0.url}\"}"}`), &step); err != nil {
		t.Fatalf("did not decode: %v", err)
	}
	got, _ := step.Params["url"].(string)
	if got != "${step.find_news.results.0.url}" {
		t.Fatalf("the reference changed on the way through: %#v", step.Params["url"])
	}
	refs := FindRefs(step.Params)
	if len(refs) != 1 || refs[0].Tag != "find_news" ||
		strings.Join(refs[0].Path, ".") != "results.0.url" {
		t.Errorf("the reference did not survive as one: %+v", refs)
	}
}

// An empty string is no parameters, because a model told to write "{}"
// sometimes writes "". A string that is neither is an error naming what was
// wrong, which is what the plan retry hands back to the planner.
func TestAParamsStringThatIsNotAnObject(t *testing.T) {
	var empty PlanStep
	if err := json.Unmarshal([]byte(`{"tool":"sysinfo","tag":"s","params":""}`), &empty); err != nil {
		t.Fatalf("an empty params string was refused: %v", err)
	}
	if len(empty.Params) != 0 {
		t.Errorf("an empty params string produced parameters: %#v", empty.Params)
	}

	var broken PlanStep
	err := json.Unmarshal([]byte(`{"tool":"bash","tag":"b","params":"command: ls"}`), &broken)
	if err == nil {
		t.Fatal("a params string holding no object decoded, so the planner is never told")
	}
	if !strings.Contains(err.Error(), "JSON object") {
		t.Errorf("the error does not say what was wrong: %v", err)
	}
}

// A schema that obeys both rules is reported clean, and each break is reported
// once. Without this the check could return nothing at all and every stage
// above would pass.
func TestStrictProblems_ReadsTheTwoRules(t *testing.T) {
	clean := json.RawMessage(`{"type":"object","additionalProperties":false,
		"properties":{"a":{"type":"string"}},"required":["a"]}`)
	if got := llm.StrictProblems(clean); len(got) != 0 {
		t.Errorf("a closed schema was reported broken: %v", got)
	}

	open := json.RawMessage(`{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]}`)
	if got := llm.StrictProblems(open); len(got) != 1 {
		t.Errorf("an absent additionalProperties was not reported once: %v", got)
	}

	loose := json.RawMessage(`{"type":"object","additionalProperties":false,
		"properties":{"a":{"type":"string"},"b":{"type":"string"}},"required":["a"]}`)
	got := llm.StrictProblems(loose)
	if len(got) != 1 || !strings.Contains(got[0].Why, "[b]") {
		t.Errorf("a property missing from required was not named: %v", got)
	}
}

// The two constructs closeNested does not walk. A schema breaking a rule inside
// an anyOf branch, or inside an open map's value, used to travel unreported —
// which is how the plan schema passed for as long as it did.
func TestStrictProblems_LooksInsideBranchesAndOpenMaps(t *testing.T) {
	inBranch := json.RawMessage(`{"type":"object","additionalProperties":false,
		"properties":{"v":{"anyOf":[{"type":"object","properties":{"x":{"type":"string"}}}]}},
		"required":["v"]}`)
	found := false
	for _, p := range llm.StrictProblems(inBranch) {
		if strings.HasPrefix(p.Path, ".v.anyOf[0]") {
			found = true
		}
	}
	if !found {
		t.Error("a broken object inside an anyOf branch was not reported")
	}

	openMap := json.RawMessage(`{"type":"object","additionalProperties":false,
		"properties":{"m":{"type":"object","additionalProperties":{"type":"string"}}},
		"required":["m"]}`)
	found = false
	for _, p := range llm.StrictProblems(openMap) {
		if p.Path == ".m" && strings.Contains(p.Why, "undeclared keys") {
			found = true
		}
	}
	if !found {
		t.Error("a map with undeclared keys was not reported as inexpressible")
	}
}

// newSchemaTestAgent builds an agent whose intent registry is populated, because
// the plan schema's intent enum is read from it — an empty registry produces an
// empty enum and a problem the real request would never carry.
func newSchemaTestAgent(t *testing.T) *Agent {
	t.Helper()
	a := &Agent{intentRegistry: NewIntentRegistry()}
	if err := a.intentRegistry.Load(staticIntents{}); err != nil {
		t.Fatalf("load intents: %v", err)
	}
	return a
}

// staticIntents is the default ladder, so the test does not need a database.
type staticIntents struct{}

func (staticIntents) ListIntents() ([]Intent, error) {
	return []Intent{
		{Name: "observe", Rank: 0, PromptDescription: "read-only"},
		{Name: "operate", Rank: 100, PromptDescription: "reversible", IsDefault: true},
		{Name: "override", Rank: 200, PromptDescription: "destructive"},
	}, nil
}

func (staticIntents) ListToolIntents() (map[string]string, error) { return nil, nil }

// A Holmes action takes params either way, like a plan step.
//
// Its schema declares a string for the same reason the plan's does. The object
// form still has to decode: it is what Anthropic sends, and every Holmes state
// already written to disk holds one — an investigation resumed after this
// change reads its own earlier turns back.
func TestAHolmesActionTakesParamsEitherWay(t *testing.T) {
	var asString holmesAction
	if err := json.Unmarshal([]byte(
		`{"tool":"file_read","params":"{\"path\":\"project/app/package.json\"}"}`), &asString); err != nil {
		t.Fatalf("a string params did not decode: %v", err)
	}
	if asString.Tool != "file_read" || asString.Params["path"] != "project/app/package.json" {
		t.Errorf("the action did not survive the string: %+v", asString)
	}

	var asObject holmesAction
	if err := json.Unmarshal([]byte(
		`{"tool":"file_read","params":{"path":"project/app/package.json"}}`), &asObject); err != nil {
		t.Fatalf("an object params did not decode: %v", err)
	}
	if asObject.Params["path"] != "project/app/package.json" {
		t.Errorf("the object form broke: %#v", asObject.Params)
	}
}

// The observer and the debugger both graft PlanSteps, so they decode through
// the same path the planner does — this is the check that they actually do,
// rather than carrying a second shape nobody updated.
func TestGraftedStepsTakeParamsAsAString(t *testing.T) {
	var obs observerOutput
	if err := json.Unmarshal([]byte(`{"action":"inject","steps":[
		{"tool":"bash","tag":"look","params":"{\"command\":\"ls -la\"}","depends_on":[]}]}`), &obs); err != nil {
		t.Fatalf("the observer's steps did not decode: %v", err)
	}
	if len(obs.Steps) != 1 || obs.Steps[0].Params["command"] != "ls -la" {
		t.Errorf("the observer's params did not survive: %+v", obs.Steps)
	}

	var dbg microPlannerOutput
	if err := json.Unmarshal([]byte(`{"summary":"missing export","steps":[
		{"tool":"edit_file","tag":"fix","params":"{\"goal\":\"add the export\"}","depends_on":[]}]}`), &dbg); err != nil {
		t.Fatalf("the debugger's steps did not decode: %v", err)
	}
	if len(dbg.Steps) != 1 || dbg.Steps[0].Params["goal"] != "add the export" {
		t.Errorf("the debugger's params did not survive: %+v", dbg.Steps)
	}
}

// The architect's two open objects travel as strings, and both forms read back.
//
// Their keys are the endpoint, type and table names the architect is choosing
// in this reply, so they cannot be declared in the request schema — which is
// what left this stage the last one a provider could not enforce.
//
// The object form still reads because it is what Anthropic sends, and because
// every interfaces.json already on disk holds one: a session resumed after this
// change reads its own earlier contracts back.
func TestTheArchitectsInterfacesReadEitherWay(t *testing.T) {
	asString := json.RawMessage(`"{\"POST /api/todos\":{\"returns\":\"Todo\"},\"Todo\":{\"id\":\"number\"}}"`)
	got, err := objectFromRaw(asString)
	if err != nil {
		t.Fatalf("the string form did not read: %v", err)
	}
	if _, ok := got["POST /api/todos"]; !ok {
		t.Errorf("the invented key did not survive: %#v", got)
	}

	asObject := json.RawMessage(`{"POST /api/todos":{"returns":"Todo"}}`)
	got, err = objectFromRaw(asObject)
	if err != nil {
		t.Fatalf("the object form did not read: %v", err)
	}
	if _, ok := got["POST /api/todos"]; !ok {
		t.Errorf("the object form broke: %#v", got)
	}
}

// An empty string is a project with no database, not a fault — it is how a
// field strict has made mandatory says it has nothing. A string holding
// something that is not an object is an error, and the error is logged at the
// call site rather than discarded, which is what it used to be.
func TestTheArchitectsSchemaCanBeEmptyOrWrong(t *testing.T) {
	got, err := objectFromRaw(json.RawMessage(`""`))
	if err != nil || len(got) != 0 {
		t.Errorf("an empty string was not read as nothing: %#v, %v", got, err)
	}

	if _, err := objectFromRaw(json.RawMessage(`"postgres, table todos"`)); err == nil {
		t.Error("prose in the field read as an object, so nothing would say the contracts were lost")
	}

	if got, err := objectFromRaw(nil); err != nil || got != nil {
		t.Errorf("an absent field was treated as a fault: %#v, %v", got, err)
	}
}
