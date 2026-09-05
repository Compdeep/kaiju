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

// ToolSafe reads the flag through Thinks(), so a dropped entry cannot reach a
// picker and a declared one is judged on what it declared.
func TestToolSafeIsNonEmptyAndExcludesThinkers(t *testing.T) {
	safe := ToolSafe()
	if len(safe) == 0 {
		t.Fatal("ToolSafe is empty; every picker that filters through it shows nothing")
	}
	for _, m := range safe {
		if m.Thinks() {
			t.Errorf("%q thinks and is still offered as tool-safe", m.ID)
		}
		if !m.ToolCallOK {
			t.Errorf("%q is offered as tool-safe without tool_call_ok", m.ID)
		}
	}
}
