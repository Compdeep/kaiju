package agent

import (
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// The self-repair trigger: a failed command body MUST be detected so the node is
// marked failed → reflection → Holmes. If this breaks, bash failures silently
// look successful and self-repair stops.
func TestBashError_TriggersOnTypedFailure(t *testing.T) {
	fail := toolMessageBody{msg: toolapi.ToolFail("command", "exit 1", map[string]any{"exit_code": 1, "stderr": "boom"})}
	if _, failed := toolReportedFailure(nodeCompletion{Body: fail}); !failed {
		t.Fatal("a failed command body must be detected as a bash error (drives self-repair)")
	}
	ok := toolMessageBody{msg: toolapi.ToolOK("command", "hi", map[string]any{"exit_code": 0})}
	if _, failed := toolReportedFailure(nodeCompletion{Body: ok}); failed {
		t.Fatal("a successful command must not be flagged as a bash error")
	}
	// A completion with no envelope declares no outcome. Guessing from its text
	// is what the string search did, and it could only ever have matched a tool
	// whose output happened to contain the words.
	if _, failed := toolReportedFailure(nodeCompletion{Result: `{"bash_error":true}`}); failed {
		t.Fatal("a bare string was read as a failure")
	}
}

func TestExtractFailureDetail_TypedBash(t *testing.T) {
	n := &Node{Body: toolMessageBody{msg: toolapi.ToolFail("command", "exit 2",
		map[string]any{"exit_code": 2, "stderr": "kaboom", "command": "false"})}}
	if got := extractFailureDetail(n); !strings.Contains(got, "exit 2") || !strings.Contains(got, "kaboom") {
		t.Fatalf("extractFailureDetail(typed) = %q", got)
	}
}

func TestExtractBashStdout_TypedBash(t *testing.T) {
	env := toolapi.ToolOK("command", "combined", map[string]any{"stdout": "the-stdout", "exit_code": 0}).JSON()
	if got := extractBashStdout(env); got != "the-stdout" {
		t.Fatalf("extractBashStdout(typed) = %q want the-stdout", got)
	}
}

// A step that ran on another machine arrives with its envelope intact.
//
// The remote path handed back only the result text, so every consumer that
// reads the typed body saw nothing for a remote step. For bash that meant a
// command which failed elsewhere was recorded as having succeeded: the node was
// not marked errored, no failure reached the reflector, and the repair loop
// never started. Absence read as success.
func TestARemoteResultKeepsItsEnvelope(t *testing.T) {
	failed := toolapi.ToolFail("command", "exit 1: no such file", map[string]any{"exit_code": 1})

	// What the dispatcher does with what the far end sent back.
	var body NodeBody
	if msg, ok := toolapi.ParseToolMessage(failed.JSON()); ok {
		body = toolMessageBody{msg: msg}
	}
	if body == nil {
		t.Fatal("an envelope from the far end did not parse, so a remote step " +
			"carries no outcome at all")
	}
	if _, failed := toolReportedFailure(nodeCompletion{Body: body}); !failed {
		t.Error("a command that failed on another machine was not detected as a " +
			"failure, so the run treats it as done and never repairs it")
	}
}
