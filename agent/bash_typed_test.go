package agent

import (
	"strings"
	"testing"

	agenttools "github.com/Compdeep/kaiju/agent/tools"
)

// The self-repair trigger: a failed command body MUST be detected so the node is
// marked failed → reflection → Holmes. If this breaks, bash failures silently
// look successful and self-repair stops.
func TestBashError_TriggersOnTypedFailure(t *testing.T) {
	fail := toolMessageBody{msg: agenttools.ToolFail("command", "exit 1", map[string]any{"exit_code": 1, "stderr": "boom"})}
	if _, isBash := bashError(nodeCompletion{Body: fail}); !isBash {
		t.Fatal("a failed command body must be detected as a bash error (drives self-repair)")
	}
	ok := toolMessageBody{msg: agenttools.ToolOK("command", "hi", map[string]any{"exit_code": 0})}
	if _, isBash := bashError(nodeCompletion{Body: ok}); isBash {
		t.Fatal("a successful command must not be flagged as a bash error")
	}
	// legacy string path still detected
	if _, isBash := bashError(nodeCompletion{Result: `{"bash_error":true}`}); !isBash {
		t.Fatal("legacy bash_error string must still be detected")
	}
}

func TestExtractFailureDetail_TypedBash(t *testing.T) {
	n := &Node{Body: toolMessageBody{msg: agenttools.ToolFail("command", "exit 2",
		map[string]any{"exit_code": 2, "stderr": "kaboom", "command": "false"})}}
	if got := extractFailureDetail(n); !strings.Contains(got, "exit 2") || !strings.Contains(got, "kaboom") {
		t.Fatalf("extractFailureDetail(typed) = %q", got)
	}
}

func TestExtractBashStdout_TypedBash(t *testing.T) {
	env := agenttools.ToolOK("command", "combined", map[string]any{"stdout": "the-stdout", "exit_code": 0}).JSON()
	if got := extractBashStdout(env); got != "the-stdout" {
		t.Fatalf("extractBashStdout(typed) = %q want the-stdout", got)
	}
}
