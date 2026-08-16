package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// What a stage is told about the run before it.
//
// The facts are assembled by code and the wording by a model, so these hold the
// facts. With no model configured the block is the facts themselves, which is
// also how every one of these reads them.

func reframeAgent(t *testing.T) *Agent {
	t.Helper()
	reg := toolapi.NewRegistry()
	return &Agent{registry: reg}
}

func withStep(g *Graph, tag, tool string, msg toolapi.ToolMessage) {
	id := g.AddNode(&Node{Type: NodeTool, Tag: tag, ToolName: tool})
	g.SetBody(id, toolMessageBody{msg: msg})
}

// A step that ran, returned, and produced nothing usable is not a failure — it
// resolved. Every other account of a run therefore shows it as a success: the
// graph's own summary counts it as resolved, and the gate's node returns list
// failures and successes with nothing in between. These four outcomes are the
// reason this exists.
func TestReframe_EachOutcomeIsNamed(t *testing.T) {
	a := reframeAgent(t)
	g := NewGraph()
	withStep(g, "look", "web_search", toolapi.ToolEmpty("search", "no results for that query"))
	withStep(g, "read", "file_read", toolapi.ToolOK("text", "listen = 8080", nil))
	withStep(g, "check", "lookup_hash", toolapi.ToolMessage{Type: "text", Status: toolapi.StatusUnclassified})

	facts := a.factsOf(g)
	if len(facts.Produced) != 3 {
		t.Fatalf("got %d steps, want 3: %v", len(facts.Produced), facts.Produced)
	}
	joined := strings.Join(facts.Produced, "\n")
	for _, want := range []string{
		"look (web_search): returned nothing — no results for that query",
		"read (file_read): produced a result",
		"check (lookup_hash): returned something but did not say whether it found anything",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q from:\n%s", want, joined)
		}
	}
}

// A step with no result at all is separate from one that returned nothing, and
// carries why.
func TestReframe_AFailedStepCarriesItsReason(t *testing.T) {
	a := reframeAgent(t)
	g := NewGraph()
	id := g.AddNode(&Node{Type: NodeTool, Tag: "reach", ToolName: "http_get"})
	g.SetError(id, errors.New("dial tcp: i/o timeout"))

	facts := a.factsOf(g)
	if len(facts.Failed) != 1 {
		t.Fatalf("got %d failures, want 1: %v", len(facts.Failed), facts.Failed)
	}
	if !strings.Contains(facts.Failed[0], "reach (http_get): failed") ||
		!strings.Contains(facts.Failed[0], "dial tcp") {
		t.Errorf("failure line = %q; it names neither the step nor why", facts.Failed[0])
	}
}

// The line that matters most: a value the run holds and nothing has used. It is
// the case a run concludes on while describing something it never opened.
func TestReframe_AValueInHandAndUnusedIsListed(t *testing.T) {
	a := refAgent("lister", listSchema("reader.path"))
	g := NewGraph()
	producedNode(g, "lister", payloadWith("a-handle"))

	facts := a.factsOf(g)
	if len(facts.Unfollowed) != 1 {
		t.Fatalf("got %d unfollowed, want 1: %v", len(facts.Unfollowed), facts.Unfollowed)
	}
	if !strings.Contains(facts.Unfollowed[0], "a-handle") {
		t.Errorf("unfollowed line = %q, want the value itself", facts.Unfollowed[0])
	}
}

// A run where everything worked still gets a block. The two edges this replaces
// were both gated on something having gone wrong, so neither could fire on a
// run that gathered ten results and read none of them — which is the run they
// existed for.
func TestReframe_ACleanRunIsStillDescribed(t *testing.T) {
	a := reframeAgent(t)
	g := NewGraph()
	withStep(g, "read", "file_read", toolapi.ToolOK("text", "listen = 8080", nil))

	block := a.EdgeReFrame(context.Background(), g, "what is the port?", "answer")
	if block == "" {
		t.Fatal("a run where every step succeeded got no account of itself")
	}
	if !strings.Contains(block, "read (file_read)") {
		t.Errorf("block does not name the step that ran:\n%s", block)
	}
}

// Before anything has run there is nothing to describe, and a block saying so
// would be noise in every stage's prompt.
func TestReframe_NothingRunYetIsNoBlock(t *testing.T) {
	a := reframeAgent(t)
	if block := a.EdgeReFrame(context.Background(), NewGraph(), "q", "answer"); block != "" {
		t.Errorf("an empty run produced a block:\n%s", block)
	}
}

// With no model the facts go through unwritten. A stage never loses its account
// of the run because the wording could not be produced.
func TestReframe_NoModelStillGivesTheFacts(t *testing.T) {
	a := reframeAgent(t)
	g := NewGraph()
	withStep(g, "look", "web_search", toolapi.ToolEmpty("search", "no results"))

	block := a.EdgeReFrame(context.Background(), g, "find the advisory", "answer")
	if !strings.Contains(block, "## What happened so far") {
		t.Errorf("block has no heading:\n%s", block)
	}
	for _, want := range []string{"find the advisory", "look (web_search): returned nothing"} {
		if !strings.Contains(block, want) {
			t.Errorf("missing %q from:\n%s", want, block)
		}
	}
}

// The block and the instruction that tells a stage how to read it move
// together. A block with no instruction is text a model may or may not credit.
func TestReframe_TheBlockAndItsInstructionMoveTogether(t *testing.T) {
	role, user := WithReframe("role", "input", "## What happened so far\n\nsomething")
	if !strings.Contains(user, "something") || !strings.HasSuffix(user, "input") {
		t.Errorf("the block was not prepended to the input: %q", user)
	}
	if role == "role" {
		t.Error("the stage was given a block and no instruction about it")
	}

	role, user = WithReframe("role", "input", "")
	if role != "role" || user != "input" {
		t.Errorf("no block should leave both prompts alone, got %q and %q", role, user)
	}
}

// The engine must not tell an application what kind of value its tools return.
// The edge this replaces told every run it had "real URLs from a search",
// whatever its tools had produced.
func TestReframe_TheFactsNameNoKindOfValue(t *testing.T) {
	a := refAgent("lister", listSchema("reader.path"))
	g := NewGraph()
	producedNode(g, "lister", payloadWith("4021"))
	withStep(g, "look", "search_telemetry", toolapi.ToolEmpty("search", "nothing matched"))

	block := a.EdgeReFrame(context.Background(), g, "which process is this?", "answer")
	for _, forbidden := range []string{"URL", "url", "web", "page", "fetch"} {
		if strings.Contains(block, forbidden) {
			t.Errorf("the facts name %q, which is one application's kind of value:\n%s", forbidden, block)
		}
	}
}

// A step the planner gave no label to is named once, not twice.
//
// "search_telemetry (search_telemetry)" reads as a label and a tool and is one
// word said twice — the same fault the gap line had, in a new place.
func TestReframe_AStepWithNoLabelIsNamedOnce(t *testing.T) {
	a := reframeAgent(t)
	g := NewGraph()
	withStep(g, "", "search_telemetry", toolapi.ToolEmpty("search", "nothing matched"))
	withStep(g, "look for logins", "bash", toolapi.ToolOK("text", "root", nil))

	facts := a.factsOf(g)
	joined := strings.Join(facts.Produced, "\n")
	if strings.Contains(joined, "search_telemetry (search_telemetry)") {
		t.Errorf("the tool is named twice:\n%s", joined)
	}
	if !strings.Contains(joined, "- search_telemetry: returned nothing") {
		t.Errorf("the unlabelled step is not named plainly:\n%s", joined)
	}
	if !strings.Contains(joined, "- look for logins (bash): produced a result") {
		t.Errorf("a labelled step should carry both:\n%s", joined)
	}
}
