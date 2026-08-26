package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

// An identifier has a field, so there is nowhere to paraphrase it to.
//
// It used to be a paragraph, and the prompt asked in capital letters for URLs
// to be quoted into it verbatim — a rule about wording, enforced by nothing, on
// the only channel carrying them to a planner that cannot see the conversation.
// The failure the prompt named as the one to avoid was "the user wants to
// update the CSV with the correct URLs", which says nothing a later stage can
// use and violates no schema.
func TestPreflightContext_IdentifiersSurviveAsData(t *testing.T) {
	var raw preflightContextRaw
	if err := json.Unmarshal([]byte(`{
		"intent": "scrape the exchange rates and save them",
		"urls": ["https://www.murc-kawasesouba.jp/fx/past_3month_result.php?y=2025&m=8&d=1"],
		"paths": ["uploads/session/data.csv"],
		"selectors": ["table.data-table5", "TTM"],
		"constants": ["5 second delay between requests", "round to 2 decimals"]
	}`), &raw); err != nil {
		t.Fatalf("the object shape did not decode: %v", err)
	}
	c := raw.PreflightContext

	if len(c.URLs) != 1 || !strings.Contains(c.URLs[0], "y=2025&m=8&d=1") {
		t.Errorf("the URL lost its query parameters: %v", c.URLs)
	}
	if len(c.Selectors) != 2 || len(c.Constants) != 2 || len(c.Paths) != 1 {
		t.Errorf("an identifier list was dropped: %+v", c)
	}

	// The prompt reads it back whole, so a planner sees every one.
	text := c.Text()
	for _, want := range []string{
		"scrape the exchange rates",
		"y=2025&m=8&d=1",
		"uploads/session/data.csv",
		"table.data-table5",
		"round to 2 decimals",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("%q did not survive into the prompt:\n%s", want, text)
		}
	}
}

// The old shape still decodes: a paragraph becomes the intent, with no
// identifiers held apart. That is exactly the old behaviour, not a failure — a
// model that has not moved is not a broken run.
func TestPreflightContext_APlainStringStillDecodes(t *testing.T) {
	var raw preflightContextRaw
	if err := json.Unmarshal([]byte(`"the user wants the exchange rates"`), &raw); err != nil {
		t.Fatalf("the string shape did not decode: %v", err)
	}
	if raw.Intent != "the user wants the exchange rates" {
		t.Errorf("the paragraph did not become the intent: %q", raw.Intent)
	}
	if len(raw.URLs) != 0 {
		t.Errorf("a paragraph produced identifiers from nowhere: %v", raw.URLs)
	}
}

// A context that says nothing is left out of the prompt entirely, rather than
// adding an empty heading for a planner to read past.
func TestPreflightContext_EmptyIsEmpty(t *testing.T) {
	if !(PreflightContext{}).Empty() {
		t.Error("a context with nothing in it did not report empty")
	}
	if (PreflightContext{URLs: []string{"https://x"}}).Empty() {
		t.Error("a context holding a URL reported empty")
	}
	if got := (PreflightContext{Intent: "do the thing"}).Text(); got != "do the thing" {
		t.Errorf("framing alone must render as itself, got %q", got)
	}
}

// The schema the model is given comes from the struct, so the field Go reads
// and the field the model is told to write are the same word.
func TestPreflightContext_SchemaComesFromTheStruct(t *testing.T) {
	var schema struct {
		Properties map[string]struct {
			Description string `json:"description"`
		} `json:"properties"`
	}
	if err := json.Unmarshal([]byte(PreflightContextSchema()), &schema); err != nil {
		t.Fatalf("the derived schema is not valid JSON: %v", err)
	}
	for _, field := range []string{"intent", "urls", "paths", "selectors", "constants"} {
		p, ok := schema.Properties[field]
		if !ok {
			t.Errorf("the schema does not declare %q, so the model is never asked for it", field)
			continue
		}
		if p.Description == "" {
			t.Errorf("%q has no description; the model is told the name and not what goes in it", field)
		}
	}
}
