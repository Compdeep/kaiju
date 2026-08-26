package tools

import (
	"encoding/json"
	"testing"
)

// file_write's content must keep "minLength": 0. That declaration is what tells
// the param validator an empty string is a value here and not an omission —
// without it, "create an empty file" and "truncate this file" are unplannable:
// the planner supplies content "", the validator calls it missing, and the
// correction loop asks for a value the planner already gave until the run fails.
//
// agent's TestEmptyStringSuppliedWhenSchemaAllows inlines a copy of this schema
// (tools imports agent, so it cannot import back). This guards the property that
// copy relies on.
func TestFileWriteContentAllowsEmpty(t *testing.T) {
	var schema struct {
		Properties map[string]struct {
			MinLength *int `json:"minLength"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal((&FileWrite{}).Parameters(), &schema); err != nil {
		t.Fatalf("file_write schema is not valid JSON: %v", err)
	}
	content, ok := schema.Properties["content"]
	if !ok {
		t.Fatal("file_write no longer declares a content property")
	}
	if content.MinLength == nil {
		t.Fatal(`file_write content lost "minLength": 0 — empty files become unplannable`)
	}
	if *content.MinLength != 0 {
		t.Fatalf(`file_write content has minLength %d, want 0`, *content.MinLength)
	}
}
