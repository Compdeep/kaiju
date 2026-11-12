package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// The planner is shown output fields by name and the tool's note, so a rule
// about using a field has to be in the note: in a field's own description it
// no longer reaches the plan.
func TestOutputRulesAreInTheToolsNote(t *testing.T) {
	for _, c := range []struct {
		tool toolapi.Tool
		want []string
	}{
		{&Bash{}, []string{"stdout", "output_path"}},
		{&WebFetch{}, []string{"full_content_path", "CONSUMES URLs"}},
	} {
		var s struct{ Description string }
		_ = json.Unmarshal(toolapi.PayloadSchema(toolapi.GetOutputSchema(c.tool)), &s)
		for _, w := range c.want {
			if !strings.Contains(s.Description, w) {
				t.Errorf("%s: the note does not mention %s: %q", c.tool.Name(), w, s.Description)
			}
		}
	}
}
