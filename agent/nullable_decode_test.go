package agent

import (
	"encoding/json"
	"reflect"
	"testing"
)

// A field the closer made nullable must decode to the same thing as one that
// was left out.
//
// closedSchema has to put every declared key in required, because strict
// demands it. A field that was optional then has no legal way to say "not
// applicable" — a boolean cannot be empty, and an enum's values are all
// meanings — so its type gains null. That change is on the wire for every
// stage, and these are the paths a null now reaches.
//
// The one that could genuinely differ is a json.RawMessage: absent leaves it
// nil, null leaves it the four bytes "null". Everything else in Go treats the
// two identically.

func TestNullDecodesLikeAbsent_Reflector(t *testing.T) {
	for _, c := range []struct{ name, withNull, without string }{
		{
			"concluding with no next move",
			`{"decision":"conclude","summary":"done","next":null,"progress":null,"aggregate":null,"outcome":null}`,
			`{"decision":"conclude","summary":"done"}`,
		},
		{
			"replanning with no outcome",
			`{"decision":"replan","summary":"more to do","next":"fetch the URLs","outcome":null,"aggregate":null}`,
			`{"decision":"replan","summary":"more to do","next":"fetch the URLs"}`,
		},
	} {
		var a, b reflectionOutput
		if err := json.Unmarshal([]byte(c.withNull), &a); err != nil {
			t.Fatalf("%s: nulls did not decode: %v", c.name, err)
		}
		if err := json.Unmarshal([]byte(c.without), &b); err != nil {
			t.Fatalf("%s: the absent form did not decode: %v", c.name, err)
		}
		// RawOutcome is the field where the two forms genuinely differ on the
		// wire, so compare what the code makes of them rather than the bytes.
		normalizeOutcomeForTest(&a)
		normalizeOutcomeForTest(&b)
		a.RawOutcome, b.RawOutcome = nil, nil
		if !reflect.DeepEqual(a, b) {
			t.Errorf("%s:\n  with nulls: %+v\n  without:    %+v", c.name, a, b)
		}
	}
}

// The same parse the reflector runs, on the two fields that read RawOutcome.
func normalizeOutcomeForTest(o *reflectionOutput) {
	if len(o.RawOutcome) > 0 {
		var s string
		if json.Unmarshal(o.RawOutcome, &s) == nil {
			o.Outcome = s
		} else {
			o.Outcome = string(o.RawOutcome)
		}
	}
	if o.Decision == "conclude" && o.Outcome == "" {
		o.Outcome = o.Summary
	}
}

// An outcome of null must not become the STRING "null".
//
// The parse falls back to the raw bytes when the value is not a string, which
// is how an object outcome is kept. JSON null unmarshals into a string as a
// no-op with no error, so it takes the first branch and leaves "" — but the
// fallback is one failed type assertion away from putting the word null in
// front of a user as the answer to their question.
func TestANullOutcomeNeverBecomesTheWordNull(t *testing.T) {
	var o reflectionOutput
	if err := json.Unmarshal([]byte(`{"decision":"conclude","summary":"the fallback","outcome":null}`), &o); err != nil {
		t.Fatalf("decode: %v", err)
	}
	normalizeOutcomeForTest(&o)
	if o.Outcome == "null" {
		t.Fatal(`a null outcome became the string "null", which would be shown to the user as the answer`)
	}
	if o.Outcome != "the fallback" {
		t.Errorf("outcome = %q, want the summary it falls back to", o.Outcome)
	}
}

func TestNullDecodesLikeAbsent_Observer(t *testing.T) {
	var a, b observerOutput
	if err := json.Unmarshal([]byte(`{"action":"continue","reason":"fine","steps":null,"cancel":null}`), &a); err != nil {
		t.Fatalf("nulls did not decode: %v", err)
	}
	if err := json.Unmarshal([]byte(`{"action":"continue","reason":"fine"}`), &b); err != nil {
		t.Fatalf("the absent form did not decode: %v", err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Errorf("with nulls %+v, without %+v", a, b)
	}
	if a.Steps != nil || a.Cancel != nil {
		t.Errorf("a null array became a non-nil slice: steps=%v cancel=%v", a.Steps, a.Cancel)
	}
}

func TestNullDecodesLikeAbsent_Plan(t *testing.T) {
	var a, b executiveCallPayload
	if err := json.Unmarshal([]byte(`{"steps":[],"answer":null,"intent":null}`), &a); err != nil {
		t.Fatalf("nulls did not decode: %v", err)
	}
	if err := json.Unmarshal([]byte(`{"steps":[]}`), &b); err != nil {
		t.Fatalf("the absent form did not decode: %v", err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Errorf("with nulls %+v, without %+v", a, b)
	}
}

// A plan step's own optional fields, which the closer also made nullable.
func TestNullDecodesLikeAbsent_PlanStep(t *testing.T) {
	var a, b PlanStep
	if err := json.Unmarshal([]byte(`{"tool":"bash","tag":"run","params":{},"type":null,"depends_on":null}`), &a); err != nil {
		t.Fatalf("nulls did not decode: %v", err)
	}
	if err := json.Unmarshal([]byte(`{"tool":"bash","tag":"run","params":{}}`), &b); err != nil {
		t.Fatalf("the absent form did not decode: %v", err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Errorf("with nulls %+v, without %+v", a, b)
	}
}
