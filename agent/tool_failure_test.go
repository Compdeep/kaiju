package agent

import (
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// A tool has two ways to say it failed: a Go error, or an envelope whose status
// is error. Only the first used to reach the node, so the second resolved like a
// success — the run's failure list stayed empty and no repair could be asked for.

func completionWith(msg toolapi.ToolMessage) nodeCompletion {
	return nodeCompletion{NodeID: "n1", Body: toolMessageBody{msg: msg}}
}

func TestToolReportedFailure_AnyToolNotJustTheShell(t *testing.T) {
	cases := []struct {
		name string
		msg  toolapi.ToolMessage
		want bool
	}{
		{"a shell command", toolapi.ToolFail("command", "exit 1: no such file", nil), true},
		{"a page that came back 400", toolapi.ToolFail("page", "HTTP 400 400 Bad Request", nil), true},
		{"a listing", toolapi.ToolFail("listing", "the query could not be read", nil), true},
		{"a stored value", toolapi.ToolFail("kv", "the store rejected it", nil), true},

		{"a result", toolapi.ToolOK("page", "the page text", nil), false},
		{"nothing found", toolapi.ToolEmpty("search", "no results"), false},
		{"prose", toolapi.ToolText("some words"), false},
	}

	for _, c := range cases {
		err, failed := toolReportedFailure(completionWith(c.msg))
		if failed != c.want {
			t.Errorf("%s: failed = %v, want %v", c.name, failed, c.want)
		}
		if failed && err == nil {
			t.Errorf("%s: reported a failure with no error", c.name)
		}
	}
}

// The reason survives, because it is what the reflector reads to decide.
func TestToolReportedFailure_CarriesTheReason(t *testing.T) {
	err, failed := toolReportedFailure(completionWith(
		toolapi.ToolFail("page", "HTTP 400 400 Bad Request", nil)))
	if !failed {
		t.Fatal("not reported as a failure")
	}
	if !strings.Contains(err.Error(), "HTTP 400") {
		t.Errorf("the reason is missing: %v", err)
	}
	if !strings.Contains(err.Error(), "page") {
		t.Errorf("the kind is missing, so the reflector cannot tell what failed: %v", err)
	}
}

// An envelope with no reason still has to fail, and say something.
func TestToolReportedFailure_EmptyDetailStillSaysSomething(t *testing.T) {
	err, failed := toolReportedFailure(completionWith(
		toolapi.ToolMessage{Type: "net", Status: toolapi.StatusError}))
	if !failed {
		t.Fatal("an error envelope with no detail was not reported as a failure")
	}
	if strings.TrimSpace(err.Error()) == "" || strings.HasSuffix(err.Error(), ": ") {
		t.Errorf("the error says nothing: %q", err.Error())
	}
}

// A node that arrived without a typed body is not guessed at.
func TestToolReportedFailure_NoBodyIsNotAFailure(t *testing.T) {
	if _, failed := toolReportedFailure(nodeCompletion{NodeID: "n1"}); failed {
		t.Error("a completion with no envelope was read as a failure")
	}
}
