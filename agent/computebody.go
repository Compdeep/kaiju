package agent

import (
	"encoding/json"
	"fmt"
)

// ComputeBody is the typed output of a compute node — the discriminated
// envelope the compute tool emits, tagged by Type ("blueprint" for deep/plan
// mode, "result" for shallow code/edit mode). Raw holds the original JSON so
// Field (template access) and Evidence (downstream text) match pre-refactor
// behavior exactly; the parsed descriptor fields drive a useful trace Summary.
//
// The scheduler's blueprint/exec grafts still re-parse the raw JSON string
// (via comp.Result); migrating those to read these fields is a later, separate
// cleanup. Evidence() returning Raw keeps them working unchanged for now.
type ComputeBody struct {
	RawBacked    `json:"-"`
	Type         string   `json:"type"`
	ProjectRoot  string   `json:"project_root,omitempty"`
	FilesCreated []string `json:"files_created,omitempty"`
	FilesEdited  []string `json:"files_edited,omitempty"`
	NoChanges    bool     `json:"no_changes,omitempty"`
	Reason       string   `json:"reason,omitempty"`
}

// parseComputeBody parses a compute tool's JSON envelope into a typed body,
// keeping the raw JSON for faithful rendering and template access. On a parse
// error the descriptor fields stay zero and Raw still carries the text, so
// Field/Evidence degrade to raw-string behavior.
func parseComputeBody(raw string) ComputeBody {
	b := ComputeBody{RawBacked: RawBacked{Raw: raw}}
	_ = json.Unmarshal([]byte(raw), &b) // best-effort; Raw is the source of truth
	return b
}

// Summary renders a short, type-aware line for the frontend trace.
func (b ComputeBody) Summary() string {
	switch {
	case b.NoChanges:
		if b.Reason != "" {
			return "no changes: " + Text.TruncateLog(b.Reason, 140)
		}
		return "no changes"
	case b.Type == "blueprint":
		if b.ProjectRoot != "" {
			return "blueprint: " + b.ProjectRoot
		}
		return "blueprint"
	case len(b.FilesCreated) > 0:
		return fmt.Sprintf("created %d file(s): %s", len(b.FilesCreated), b.FilesCreated[0])
	case len(b.FilesEdited) > 0:
		return fmt.Sprintf("edited %d file(s): %s", len(b.FilesEdited), b.FilesEdited[0])
	case b.Type != "":
		return b.Type
	default:
		return RawText(b.Raw).Summary()
	}
}
