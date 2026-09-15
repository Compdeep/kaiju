package agent

// The reflector's `next` used to carry two things that came from different
// places, and nothing downstream could tell them apart.
//
// On a run asking where the solar system's centre of mass is, a step failed with
// "Bad dates -- start must be earlier than stop". The reflector read that
// correctly — the two timestamps were equal — and then, in the same field, wrote
// START_TIME='2026-09-15_00:00', a format nothing had mentioned. reframe_plan is
// told to preserve operational specifics exactly, so it relayed both verbatim;
// the planner used the invented one and the round was spent learning the API
// cannot parse it.
//
// Across two runs of that request, every value the reflector took from a result
// was right and every value it supplied was wrong. So the fields are split by
// where the text came from: `failure` is copied, `next` is decided.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/llm"
)

// What the reflector actually returned on that run, with the parts that came
// from two places now in two fields.
const recordedReflection = `{
  "decision": "replan",
  "progress": "productive",
  "summary": "The Horizons query failed and no vector data was retrieved.",
  "failure": "START_TIME='2026-09-15' STOP_TIME='2026-09-15' → Bad dates -- start must be earlier than stop",
  "next": "Re-query the same endpoint with a start and stop that differ."
}`

func TestReflectionSplit_FailureAndNextArriveSeparately(t *testing.T) {
	var out reflectionOutput
	if err := json.Unmarshal([]byte(recordedReflection), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Failure == "" {
		t.Fatal("the quoted failure was dropped — reflectionOutput has no field for it")
	}
	if !strings.Contains(out.Failure, "Bad dates") {
		t.Errorf("the failure field does not hold the error text: %q", out.Failure)
	}
	if strings.Contains(out.Next, "START_TIME") {
		t.Errorf("next still carries a parameter value: %q", out.Next)
	}
}

// The schema is what the model reads as it fills the fields, so it has to draw
// the same line the prompt does.
func TestReflectionSplit_TheSchemaAsksForBothAndSeparatesThem(t *testing.T) {
	raw := string(reflectorSchema().Function.Parameters)
	var doc struct {
		Properties map[string]struct {
			Description string `json:"description"`
		} `json:"properties"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("schema: %v", err)
	}

	failure, ok := doc.Properties["failure"]
	if !ok {
		t.Fatal("the schema has no failure field, so the model has nowhere to put what it read")
	}
	if !strings.Contains(strings.ToLower(failure.Description), "quoted") {
		t.Errorf("the failure field does not say to quote: %q", failure.Description)
	}

	next := doc.Properties["next"].Description
	if !strings.Contains(strings.ToLower(next), "no parameter value") {
		t.Errorf("the next field does not forbid parameter values: %q", next)
	}
	// The two instructions that used to sit in this one description and pulled
	// against each other, plus the examples 163342f took out of the prompt and
	// left here.
	for _, gone := range []string{"exact error text", "debug step", "3 URLs"} {
		if strings.Contains(next, gone) {
			t.Errorf("next's description still carries %q, which belongs to failure or to nothing: %q", gone, next)
		}
	}
}

// Strict has to be able to carry the added field, or the stage silently drops
// back to unenforced tool calling.
func TestReflectionSplit_TheSchemaIsStillCarryableUnderStrict(t *testing.T) {
	sent := llm.SchemaAsSent(reflectorSchema())
	if sent == nil {
		t.Fatal("the reflector schema would not be sent as a schema request at all")
	}
	for _, p := range llm.StrictProblems(sent) {
		t.Errorf("%s: %s", p.Path, p.Why)
	}
}

// The planner is shown the failure as a record, under its own heading, with the
// inputs marked as the ones that did not work.
//
// This is the half that matters: the frame's closing rule lets the planner write
// any value it can point to in the material above. Before the split, a value the
// reflector invented sat in that material inside `next` and satisfied the rule.
func TestReflectionSplit_ThePlannerSeesWhatFailedAsARecord(t *testing.T) {
	block := replanFailureBlock("START_TIME='2026-09-15' STOP_TIME='2026-09-15' → Bad dates -- start must be earlier than stop")
	if block == "" {
		t.Fatal("a quoted failure produced no block")
	}
	if !strings.Contains(block, "Bad dates") {
		t.Errorf("the error text did not reach the planner: %q", block)
	}
	if !strings.Contains(block, "not the ones to use") {
		t.Errorf("the block does not say the inputs are the failed ones: %q", block)
	}

	frame := replanFrame("Re-query with a start and stop that differ.", block)
	if !strings.Contains(frame, "What was tried, and what came back:") {
		t.Errorf("the failure has no heading of its own in the frame:\n%s", frame)
	}
	if !strings.Contains(frame, "Reflector says the next move is:") {
		t.Errorf("the frame lost the next-move heading:\n%s", frame)
	}

	// A replan after a success has nothing to quote and gets no heading rather
	// than an empty one to interpret.
	if empty := replanFailureBlock("   "); empty != "" {
		t.Errorf("an absent failure produced a block: %q", empty)
	}
	if f := replanFrame("Fetch what the search surfaced.", ""); strings.Contains(f, "What was tried") {
		t.Errorf("a success replan was given a failure heading:\n%s", f)
	}
}
