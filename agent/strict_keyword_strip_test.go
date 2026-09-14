package agent

import (
	"encoding/json"
	"testing"

	"github.com/Compdeep/kaiju/agent/llm"
)

func TestStrictStrip_AKeywordGoesAndAPropertyOfTheSameNameStays(t *testing.T) {
	raw := `{"type":"object","properties":{
	  "action":{"type":"string"},
	  "format":{"type":"string","enum":["zip","tar.gz"],"description":"Archive format"},
	  "path":{"type":"string","minLength":3}},
	  "required":["action"],"additionalProperties":false}`
	sent := llm.SchemaAsSent(llm.ToolDef{Type: "function",
		Function: llm.FunctionDef{Name: "archive", Parameters: json.RawMessage(raw)}})
	var m map[string]any
	json.Unmarshal(sent, &m)
	props, _ := m["properties"].(map[string]any)
	if _, ok := props["format"]; !ok {
		t.Fatalf("the format PROPERTY was deleted: %s", sent)
	}
	fp, _ := props["format"].(map[string]any)
	if _, bad := fp["enum"]; !bad {
		t.Fatalf("format lost its enum: %s", sent)
	}
	pp, _ := props["path"].(map[string]any)
	if _, bad := pp["minLength"]; bad {
		t.Fatalf("minLength keyword survived: %s", sent)
	}
	t.Logf("ok: %s", sent)
}
