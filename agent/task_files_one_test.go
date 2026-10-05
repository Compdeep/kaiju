package agent

import (
	"strings"
	"testing"
)

// A coder node writes one file, so a longer list has to say what it did not touch.
//
// dest is taskFiles[0] in every route and coderFileFacts reads that same first
// entry, so a second path was dropped without a word — a file the planner meant to
// have changed simply never was, and nothing in the run said so.
//
// Not refused: the first file is still work worth doing, and the reflector can
// plan the others once it knows they were missed.

// The architect is told one path, and told the consequence of sending two. The
// constraint cannot be a schema keyword: closedSchema strips maxItems out of the
// copy sent to the provider, so maxItems: 1 would never reach the model. The
// description does, because description is not in outsideStrict.
func TestTaskFiles_TheArchitectIsToldOneAndWhySendingTwoFails(t *testing.T) {
	src := readSource(t, "stage_schemas.go")
	i := strings.Index(src, `"task_files": {`)
	if i < 0 {
		t.Fatal("task_files is no longer in the architect's task schema")
	}
	block := src[i:min(len(src), i+400)]

	if !strings.Contains(block, "Exactly ONE file path") {
		t.Errorf("the architect is not told how many paths to send:\n%s", block)
	}
	if !strings.Contains(block, "not edited") {
		t.Errorf("the description states a rule without its consequence, so a model that "+
			"sends two has no reason to expect anything to be lost:\n%s", block)
	}
	if strings.Contains(block, "maxItems") {
		t.Error("maxItems is in the schema, and closedSchema strips it before the provider " +
			"sees it — the constraint reads as enforced and is not")
	}
}

// maxItems really is stripped, so the test above is not guessing.
func TestTaskFiles_MaxItemsWouldNotSurviveTheStrictCopy(t *testing.T) {
	src := readSource(t, "llm/structured.go")
	i := strings.Index(src, "var outsideStrict = []string{")
	if i < 0 {
		t.Skip("outsideStrict has moved; the claim above needs rechecking by hand")
	}
	if !strings.Contains(src[i:min(len(src), i+600)], `"maxItems"`) {
		t.Error("maxItems is no longer stripped, so it would now be worth putting in the schema")
	}
}
