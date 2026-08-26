package agent

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// A tool has two ways to say it failed: return a Go error, or return a result
// whose status is error. A typed tool that ran and could not do the work uses
// the second, so its Go error is nil.
//
// Every stage above the dispatcher reads Err. Any stage that forgets the other
// half turns a failure into a success — the completion arrives with no error
// and no result, the node resolves carrying nothing, and the run counts a failed
// step as work that worked.
//
// Observed on a real run: a web_fetch of blockchair.com whose result said it
// failed was sent to the twotime repair tier, which looks for a shell command to
// rewrite and finds a URL. It resolved in 39ms with no result, no payload, no
// summary and no error.
//
// So the roll-up happens where a completion is built, once, and nothing above it
// has to know there were ever two places to look.
func TestCompletionOf_AFailureInTheResultReachesErr(t *testing.T) {
	body := toolMessageBody{msg: toolapi.ToolFail("page", "HTTP 429 Too Many Requests", nil)}

	comp := completionOf("n5", body.Evidence(), body, nil)

	if comp.Err == nil {
		t.Fatal("a tool that failed in its result packed a completion reporting success")
	}
	if !strings.Contains(comp.Err.Error(), "429") {
		t.Errorf("the failure lost what it said: %v", comp.Err)
	}
	// Rolled up, not moved: the detail lives in the body and stays there.
	if comp.Body == nil {
		t.Error("the body was dropped, so the step's own output is gone")
	}
}

// A Go error is left exactly as it was. It is more specific than anything
// rebuilt from a status, and errors.As is used on it for control flow.
func TestCompletionOf_AGoErrorPassesThrough(t *testing.T) {
	boom := errors.New("connection refused")
	if got := completionOf("n1", "", nil, boom); got.Err != boom {
		t.Errorf("the original error was replaced: %v", got.Err)
	}
}

// A step that worked is not made to look like one that did not.
func TestCompletionOf_SuccessStaysSuccess(t *testing.T) {
	body := toolMessageBody{msg: toolapi.ToolOK("page", "content", nil)}
	if got := completionOf("n1", "content", body, nil); got.Err != nil {
		t.Errorf("a successful step was packed as a failure: %v", got.Err)
	}
}

// A retry that declines must carry back what the tool produced. The bail-out it
// replaced sent a node id and an error alone, so the step's own output was lost
// along with the chance to repair it.
func TestRetryGaveUp_CarriesTheOutputBack(t *testing.T) {
	body := toolMessageBody{msg: toolapi.ToolFail("page", "HTTP 429", nil)}
	comp := nodeCompletion{NodeID: "n1", Err: errors.New("boom"), Body: body, Result: "detail"}
	got := retryGaveUp(comp)
	if got.Body == nil || got.Result != "detail" {
		t.Error("a declining retry dropped what the tool produced")
	}
}

// Neither kind of failure present means something upstream classified a healthy
// step as needing repair. Resolving it as a success hides that; saying so does
// not.
func TestRetryGaveUp_NeverReportsSuccess(t *testing.T) {
	if got := retryGaveUp(nodeCompletion{NodeID: "n1"}); got.Err == nil {
		t.Error("a retry with nothing to report still said the step succeeded")
	}
}

// Whatever the cause, a step that exists to produce something and produced
// nothing must not render as a tick with no text beside it — that is
// indistinguishable from a step that worked.
func TestNodeInfo_AnEmptyStepSaysSo(t *testing.T) {
	g := NewGraph()
	id := g.AddNode(&Node{Type: NodeTool, Tag: "fetch_blockchair", ToolName: "web_fetch"})
	g.SetState(id, StateResolved)

	info := g.SnapshotNode(id)
	if info.Summary == "" {
		t.Fatal("a step that produced nothing rendered as an empty row")
	}
	if !strings.Contains(info.Summary, "nothing") {
		t.Errorf("the row does not say the step produced nothing: %q", info.Summary)
	}
}

// A step that returned FIELDS and no prose has plenty to show. Its summary and
// payload used to be computed inside a `if n.Result != ""` guard, so a typed
// tool whose Evidence was empty rendered blank while holding a full payload.
func TestNodeInfo_FieldsShowWithoutProse(t *testing.T) {
	g := NewGraph()
	id := g.AddNode(&Node{Type: NodeTool, Tag: "probe", ToolName: "web_fetch"})
	g.SetBody(id, toolMessageBody{msg: toolapi.ToolOK("page", "",
		json.RawMessage(`{"url":"https://example.test/a","bytes":12}`))})

	info := g.SnapshotNode(id)
	if len(info.Payload) == 0 {
		t.Error("a step's fields were hidden because it returned no prose")
	}
	if info.Summary == "returned nothing" {
		t.Error("a step with fields was reported as having produced nothing")
	}
}

// A stage that decides or plans is allowed to be quiet — it exists to choose,
// not to produce. Naming those would put the line on most rows of most traces.
func TestNodeInfo_QuietStagesAreNotAccused(t *testing.T) {
	g := NewGraph()
	id := g.AddNode(&Node{Type: NodeReflection, Tag: "reflect"})
	g.SetState(id, StateResolved)
	if s := g.SnapshotNode(id).Summary; s == "returned nothing" {
		t.Errorf("a reflection was reported as an empty step: %q", s)
	}
}
