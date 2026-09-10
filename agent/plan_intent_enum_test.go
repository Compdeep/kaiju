package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/llm"
)

// With no intent registry, the enum is left out — not sent empty, and not sent
// null.
//
// The comment here always claimed the enum was omitted and the code never did
// it. A nil slice marshals to `"enum": null`, which Anthropic answers 400 to;
// sending `[]` instead only makes the fault quieter, since an empty enum parses
// and offers the model no legal value at all. A property with no enum is a
// plain string, which is what "we do not know the intent names" means.
func TestPlanSchema_OmitsTheIntentEnumWhenThereAreNoNames(t *testing.T) {
	raw := (&Agent{}).executivePlanSchema().Function.Parameters

	if strings.Contains(string(raw), `"enum": null`) || strings.Contains(string(raw), `"enum":null`) {
		t.Error(`the schema carries "enum": null, which Anthropic refuses outright`)
	}

	var doc struct {
		Properties struct {
			Intent map[string]any `json:"intent"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("the plan schema is not valid JSON: %v", err)
	}
	if _, present := doc.Properties.Intent["enum"]; present {
		t.Errorf("intent carries an enum with no registry to fill it: %v", doc.Properties.Intent)
	}
	if doc.Properties.Intent["type"] != "string" {
		t.Errorf("intent is no longer a string: %v", doc.Properties.Intent)
	}
}

// With a registry, the enum is there and holds its names — omitting it must be
// what happens when there is nothing to say, not always.
func TestPlanSchema_CarriesTheIntentEnumWhenThereAreNames(t *testing.T) {
	a := newSchemaTestAgent(t)
	raw := a.executivePlanSchema().Function.Parameters

	var doc struct {
		Properties struct {
			Intent struct {
				Enum []string `json:"enum"`
			} `json:"intent"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("the plan schema is not valid JSON: %v", err)
	}
	if len(doc.Properties.Intent.Enum) == 0 {
		t.Fatal("a loaded registry produced no intent enum")
	}
}

// Neither form may be one the strict checker refuses on the enum.
//
// An empty enum is reported as "no value is legal", so a schema built without a
// registry used to carry a fault that had nothing to do with its shape.
func TestPlanSchema_TheIntentPropertyIsNeverAStrictFault(t *testing.T) {
	for _, a := range []*Agent{{}, newSchemaTestAgent(t)} {
		sent := llm.SchemaAsSent(a.executivePlanSchema())
		if sent == nil {
			continue // declined for another reason; the enum is not it
		}
		for _, p := range llm.StrictProblems(sent) {
			if strings.Contains(p.Why, "enum") {
				t.Errorf("the intent enum is a strict fault: %s", p)
			}
		}
	}
}
