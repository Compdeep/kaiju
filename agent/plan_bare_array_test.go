package agent

import "testing"

// The provider fault one step past the fence: the envelope dropped, leaving the
// steps array alone. Seen once during the fence measurements, fenced AND bare.
func TestABareStepsArrayIsAdopted(t *testing.T) {
	raw := `[
  {"tool":"web_search","tag":"search_barycenter","params":{"query":"barycenter"}},
  {"tool":"web_fetch","tag":"fetch_barycenter","params":{"url":"${step.search_barycenter.results.0.url}"}}
]`
	var p executiveCallPayload
	if err := parseExecutivePayload(raw, &p); err != nil {
		t.Fatalf("a bare steps array did not parse: %v", err)
	}
	if len(p.Steps) != 2 {
		t.Fatalf("got %d steps, want 2", len(p.Steps))
	}
	if p.Steps[0].Tag != "search_barycenter" || p.Steps[1].Tag != "fetch_barycenter" {
		t.Errorf("wrong steps: %+v", p.Steps)
	}
	// The envelope is genuinely absent, not invented. Both readers of these
	// fields already handle the zero value.
	if p.Intent != "" || p.Answer != "" {
		t.Errorf("a field the model did not send was filled in: intent=%q answer=%q", p.Intent, p.Answer)
	}
}

// Fenced and bare at once — the shape actually observed. The fence rung strips
// the wrapper and fails to adopt an array as the payload, so this rung reads
// what is left rather than the run dying between the two.
func TestAFencedBareStepsArrayIsAdopted(t *testing.T) {
	raw := "```json\n" + `[{"tool":"web_search","tag":"find","params":{"query":"x"}}]` + "\n```"

	var p executiveCallPayload
	if err := parseExecutivePayload(raw, &p); err != nil {
		t.Fatalf("a fenced bare array did not parse: %v", err)
	}
	if len(p.Steps) != 1 || p.Steps[0].Tag != "find" {
		t.Fatalf("wrong steps: %+v", p.Steps)
	}
}

// depends_on written as a tag still resolves, as it does through every rung.
func TestABareStepsArrayStillLinksItsTags(t *testing.T) {
	raw := `[{"tool":"web_search","tag":"find","params":{"query":"x"},"depends_on":[]},
	         {"tool":"web_fetch","tag":"read","params":{"url":"y"},"depends_on":["find"]}]`

	var p executiveCallPayload
	if err := parseExecutivePayload(raw, &p); err != nil {
		t.Fatalf("did not parse: %v", err)
	}
	if len(p.Steps) != 2 || len(p.Steps[1].DependsOn) != 1 {
		t.Fatalf("depends_on lost: %+v", p.Steps)
	}
}

// An empty array is not a plan. Adopting it would report success with no steps,
// which reads downstream as "the planner had nothing to add" rather than as the
// broken reply it is.
func TestAnEmptyArrayIsNotAdopted(t *testing.T) {
	var p executiveCallPayload
	if err := parseExecutivePayload(`[]`, &p); err == nil {
		t.Fatal("an empty array was adopted as a plan")
	}
}

// An array of something else must not be mistaken for steps.
func TestAnArrayOfNonStepsIsNotAdopted(t *testing.T) {
	var p executiveCallPayload
	if err := parseExecutivePayload(`["just", "some", "strings"]`, &p); err == nil {
		t.Fatal("an array of strings was adopted as a plan")
	}
}
