package models

import (
	"encoding/json"
	"testing"
)

// The catalog this binary ships must load whole.
//
// load() drops an entry that does not declare "thinking", and it drops it at
// package init where nothing fails — the model simply is not there, and the
// first sign is a picker missing an option. So the shipped file is checked
// here rather than trusted.
func TestTheShippedCatalogLoadsWhole(t *testing.T) {
	var cat catalog
	if err := json.Unmarshal(modelsJSON, &cat); err != nil {
		t.Fatalf("models.json does not parse: %v", err)
	}
	if len(cat.Models) == 0 {
		t.Fatal("models.json carries no models")
	}
	if got, want := len(All()), len(cat.Models); got != want {
		t.Errorf("%d of %d entries survived load(); the rest omit \"thinking\"", got, want)
	}
}

// Every entry says, one way or the other. This is the check load() enforces,
// stated against the file so a new entry fails here rather than vanishing.
func TestEveryCatalogEntryDeclaresThinking(t *testing.T) {
	var cat catalog
	if err := json.Unmarshal(modelsJSON, &cat); err != nil {
		t.Fatalf("models.json does not parse: %v", err)
	}
	for _, m := range cat.Models {
		if m.Thinking == nil {
			t.Errorf("%q does not declare \"thinking\"", m.ID)
		}
		if m.ID == "" {
			t.Error("an entry has no id")
		}
	}
}

// An entry missing the field is dropped, not defaulted. A default here would be
// "does not reason before answering", which is what gets a model offered for a
// forced tool call in a 16-token budget.
func TestAnEntryWithoutThinkingIsDroppedRatherThanDefaulted(t *testing.T) {
	raw := []byte(`{"version":1,"models":[
		{"id":"declared/yes","provider":"p","thinking":true,"tools":true,"tool_call_ok":true},
		{"id":"declared/no","provider":"p","thinking":false,"tools":true,"tool_call_ok":true},
		{"id":"undeclared","provider":"p","tools":true,"tool_call_ok":true}
	]}`)
	var cat catalog
	if err := json.Unmarshal(raw, &cat); err != nil {
		t.Fatalf("fixture does not parse: %v", err)
	}
	kept := 0
	for _, m := range cat.Models {
		if m.Thinking != nil {
			kept++
		}
	}
	if kept != 2 {
		t.Fatalf("%d entries declared thinking, want 2", kept)
	}
	// The distinction the pointer exists for: absent is not false.
	var undeclared, declaredFalse Info
	for _, m := range cat.Models {
		switch m.ID {
		case "undeclared":
			undeclared = m
		case "declared/no":
			declaredFalse = m
		}
	}
	if undeclared.Thinking != nil {
		t.Error("an omitted field decoded as a value")
	}
	if declaredFalse.Thinking == nil || *declaredFalse.Thinking {
		t.Error("an explicit false did not survive as an explicit false")
	}
	// Both read false through Thinks(); only one of them said so.
	if undeclared.Thinks() || declaredFalse.Thinks() {
		t.Error("Thinks() is true for a model that does not think")
	}
}

// ToolSafe offers what can call tools, and a reasoning mode no longer excludes a
// model from that.
//
// The third term used to be !Thinks(), and it hid most of the catalogue from
// every picker — including, on one deployment, the model the config already
// named, which is how a settings page shows an empty dropdown. It was also
// measured false: qwen3-32b reasons and returned a complete plan three times of
// three. On every line released since early 2026 reasoning is a switch a request
// turns off rather than a property the model carries.
func TestToolSafeOffersWhatCanCallTools(t *testing.T) {
	safe := ToolSafe()
	if len(safe) == 0 {
		t.Fatal("ToolSafe is empty; every picker that filters through it shows nothing")
	}
	for _, m := range safe {
		if !m.Tools {
			t.Errorf("%q is offered as tool-safe without tool support", m.ID)
		}
		if !m.ToolCallOK {
			t.Errorf("%q is offered as tool-safe without tool_call_ok", m.ID)
		}
	}
	// A reasoning model has to be reachable, or the whole current catalogue is.
	thinkers := 0
	for _, m := range safe {
		if m.Thinks() {
			thinkers++
		}
	}
	if thinkers == 0 {
		t.Error("no reasoning model is offered; the filter is excluding on Thinking again")
	}
}

// Whatever the picker offers, the lane warning still has to be able to say what a
// reasoning model costs — offering is not the same as recommending.
func TestAReasoningModelIsStillIdentifiable(t *testing.T) {
	m, ok := Find("qwen/qwen3-32b")
	if !ok {
		t.Fatal("the catalog no longer carries qwen3-32b, which a live deployment runs")
	}
	if !m.Thinks() {
		t.Error("qwen3-32b is not marked as reasoning, so nothing can warn about it")
	}
}
