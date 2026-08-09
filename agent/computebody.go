package agent

import (
	"encoding/json"
	"fmt"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// Turning a compute run into an envelope.
//
// compute and edit_file return JSON describing what they did: a blueprint for
// a deep run, a result for a shallow one, or a statement that nothing needed
// changing. That JSON is the payload, and the outcome is read off it here.
//
// It is read rather than asked for. A run that changed no files already says
// so in `no_changes` and says why in `reason`, so deriving the status costs
// nothing — there is no second model call and no guess. This is also why the
// payload stays exactly as the tool wrote it: three grafts in the scheduler
// read the plan out of it, and re-shaping it here would break them silently.

// computeDescriptor is the part of a compute result that says what happened.
// Everything else in the JSON — the follow-up graft instructions, the services,
// the validation steps — is the scheduler's business and is left untouched.
type computeDescriptor struct {
	Type         string   `json:"type"`
	ProjectRoot  string   `json:"project_root,omitempty"`
	FilesCreated []string `json:"files_created,omitempty"`
	FilesEdited  []string `json:"files_edited,omitempty"`
	NoChanges    bool     `json:"no_changes,omitempty"`
	Reason       string   `json:"reason,omitempty"`
}

// computeMessage wraps a compute run's JSON in the envelope, with the outcome
// read off the JSON itself.
//
// A run that changed nothing is empty, not ok: the coverage edge exists to tell
// an answering stage which steps produced nothing, and "no code was written"
// is exactly that. Its `reason` becomes the detail, which is the sentence the
// stage is shown.
func computeMessage(kind, raw string) toolapi.ToolMessage {
	var d computeDescriptor
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		// Not JSON. The tool still produced something and the run should see
		// it, so it goes through unclassified rather than being called a
		// failure the tool never reported.
		return toolapi.ToolUnclassified(raw)
	}

	if d.NoChanges {
		reason := d.Reason
		if reason == "" {
			reason = "the run made no changes"
		}
		return toolapi.ToolEmpty(kind, reason)
	}

	msg := toolapi.ToolOK(kind, "", nil)
	msg.Data = json.RawMessage(raw)
	msg.Detail = computeSummary(d)
	return msg
}

// computeSummary is the line a trace shows for a compute node. It was
// ComputeBody.Summary before compute carried an envelope, and it says the same
// things.
func computeSummary(d computeDescriptor) string {
	switch {
	case d.Type == "blueprint":
		if d.ProjectRoot != "" {
			return "blueprint: " + d.ProjectRoot
		}
		return "blueprint"
	case len(d.FilesCreated) > 0:
		return fmt.Sprintf("created %d file(s): %s", len(d.FilesCreated), d.FilesCreated[0])
	case len(d.FilesEdited) > 0:
		return fmt.Sprintf("edited %d file(s): %s", len(d.FilesEdited), d.FilesEdited[0])
	case d.Type != "":
		return d.Type
	}
	return ""
}

// computePayload returns a compute node's own JSON out of the envelope around
// it. Three grafts in the scheduler read the plan, the execute field and the
// services out of a compute result; they go through here so they all read the
// same place and a result that is not an envelope still works.
func computePayload(result string) string {
	if msg, ok := toolapi.ParseToolMessage(result); ok && len(msg.Data) > 0 {
		return string(msg.Data)
	}
	return result
}

// withComputePayload puts an updated plan back where computePayload found it,
// leaving the envelope around it intact. Used when an exec node's stdout is
// spliced onto its compute parent.
func withComputePayload(result, payload string) string {
	msg, ok := toolapi.ParseToolMessage(result)
	if !ok {
		return payload
	}
	msg.Data = json.RawMessage(payload)
	return msg.JSON()
}
