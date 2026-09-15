package agent

import (
	"encoding/json"
	"slices"
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

// A heading is a claim about what sits under it, so an identifier goes under the
// one that describes it.
//
// Text() names each field to the planner — "URLs", "Paths", "Selectors and field
// names" — and Paths is documented as file and directory paths. A URL there tells
// the planner a web page is a file on this machine. Observed on a run that named
// JPL Horizons: the same address arrived in urls AND in paths, so the planner was
// shown it twice under two descriptions, only one of them true.
func TestPreflightContext_AWebAddressIsFiledAsAURLNotAPath(t *testing.T) {
	var raw preflightContextRaw
	if err := json.Unmarshal([]byte(`{
		"intent": "get the barycenter position",
		"urls": ["https://ssd.jpl.nasa.gov/horizons/"],
		"paths": ["https://ssd.jpl.nasa.gov/horizons/"]
	}`), &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(raw.Paths) != 0 {
		t.Errorf("a web address was left under Paths: %v", raw.Paths)
	}
	if len(raw.URLs) != 1 {
		t.Errorf("the duplicate was added again instead of folded in: %v", raw.URLs)
	}
	if text := raw.Text(); strings.Contains(text, "Paths:") {
		t.Errorf("the planner is shown a Paths heading with nothing true under it:\n%s", text)
	}
}

// Moved, not dropped. An identifier the model bothered to copy is one the task
// probably names, and losing it silently is worse than a wrong heading.
func TestPreflightContext_AMisfiledAddressIsMovedRatherThanLost(t *testing.T) {
	var raw preflightContextRaw
	if err := json.Unmarshal([]byte(`{
		"intent": "read the spec",
		"paths": ["https://example.com/spec.html", "docs/notes.md"]
	}`), &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !slices.Contains(raw.URLs, "https://example.com/spec.html") {
		t.Errorf("the address was dropped instead of moved: urls=%v", raw.URLs)
	}
	if !slices.Contains(raw.Paths, "docs/notes.md") {
		t.Errorf("a real path was moved with it: paths=%v", raw.Paths)
	}
	if len(raw.Paths) != 1 {
		t.Errorf("Paths should hold only the real path, got %v", raw.Paths)
	}
}

// Only the scheme decides it. A path can carry everything else a URL can, and a
// file named like a query string is still a file.
func TestPreflightContext_OnlyTheSchemeMakesItAURL(t *testing.T) {
	stay := []string{
		"report.html?v=2",
		"docs/api.json",
		"/var/log/syslog",
		"C:\\Users\\me\\notes.txt",
		"ssd.jpl.nasa.gov/horizons/", // no scheme: not this check's business
		"ftp://files.example.com/x",  // a scheme, but not one web_fetch takes
	}
	var raw preflightContextRaw
	body, _ := json.Marshal(map[string]any{"intent": "x", "paths": stay})
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(raw.Paths) != len(stay) {
		t.Errorf("entries were moved that are not web addresses: kept %v", raw.Paths)
	}
	if len(raw.URLs) != 0 {
		t.Errorf("something was filed as a URL that has no web scheme: %v", raw.URLs)
	}

	for _, moved := range []string{"http://example.com/a", "HTTPS://Example.com/B", "  https://example.com/c  "} {
		var r preflightContextRaw
		b, _ := json.Marshal(map[string]any{"intent": "x", "paths": []string{moved}})
		if err := json.Unmarshal(b, &r); err != nil {
			t.Fatalf("unmarshal %q: %v", moved, err)
		}
		if len(r.Paths) != 0 || len(r.URLs) != 1 {
			t.Errorf("%q was not filed as a URL: paths=%v urls=%v", moved, r.Paths, r.URLs)
		}
	}
}
