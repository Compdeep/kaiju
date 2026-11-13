package agent

import (
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// The "### Failed" section and the aggregator's failure summary read a failed
// command through extractFailureDetail, which rebuilt the line from the exit
// code and the streams and left out the tool's own account of the failure. A
// command that sent its errors to /dev/null reached them as "exit 1" and
// nothing else (run 19:40 2026-11-12), and the stages read that as an empty
// result.
func TestAFailedCommandsCauseReachesTheFailedSection(t *testing.T) {
	g := NewGraph()
	id := g.AddNode(&Node{Type: NodeTool, Tag: "check_sudoers", ToolName: "bash"})
	why := "exit 1: exit status 1: it printed no error message, because the command sends its errors to /dev/null, so the cause is not known"
	g.SetBody(id, toolMessageBody{msg: toolapi.ToolFail("command", why, map[string]any{
		"exit_code": 1, "stdout": "=== SUDOERS.D CONTENTS ===\n", "stderr": "", "command": "cat /etc/sudoers.d/* 2>/dev/null",
	})})
	got := extractFailureDetail(g.Get(id))
	if !strings.Contains(got, "printed no error message") {
		t.Errorf("the cause is missing from the failure detail: %s", got)
	}
	if !strings.Contains(got, "SUDOERS.D CONTENTS") {
		t.Errorf("what the command printed is missing: %s", got)
	}
}
