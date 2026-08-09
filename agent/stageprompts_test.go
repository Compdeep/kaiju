package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/llm"
	"github.com/Compdeep/kaiju/agent/toolapi"
)

// An edge that fires must do both halves. The block alone is not enough: the
// stage would be handed a "## Coverage" section with nothing telling it the
// section is authoritative, and would answer as though it were more evidence.
func TestAnEdgeAppliesItsBlockAndItsHook(t *testing.T) {
	p := NewStagePrompts("ROLE", "USER").withEdge("BLOCK", "HOOK")

	if !strings.HasPrefix(p.User, "BLOCK") {
		t.Errorf("the block must be prepended to the input, got %q", p.User)
	}
	if !strings.HasSuffix(p.User, "USER") {
		t.Errorf("the input must survive, got %q", p.User)
	}
	if !strings.HasPrefix(p.Role, "ROLE") || !strings.HasSuffix(p.Role, "HOOK") {
		t.Errorf("the hook must be appended to the role prompt, got %q", p.Role)
	}
}

// Every edge is gated on the run's shape, and most runs are clean. An edge that
// did not fire must leave the stage exactly as it was.
func TestAnEdgeThatDidNotFireChangesNothing(t *testing.T) {
	before := NewStagePrompts("ROLE", "USER")
	after := before.withEdge("", "HOOK")

	if after.Role != before.Role || after.User != before.User {
		t.Errorf("a silent edge altered the prompts: %+v", after)
	}
}

// The most recently applied edge is read first, which is how the two ran before
// this type existed and is the order the reflector's prompts assume.
func TestTheLastEdgeAppliedIsReadFirst(t *testing.T) {
	p := NewStagePrompts("ROLE", "USER").withEdge("FIRST", "HOOK-A").withEdge("SECOND", "HOOK-B")

	if p.User != "SECOND\n\nFIRST\n\nUSER" {
		t.Errorf("blocks stacked wrongly: %q", p.User)
	}
	if p.Role != "ROLE\n\nHOOK-A\n\nHOOK-B" {
		t.Errorf("hooks stacked wrongly: %q", p.Role)
	}
}

// An edge is asked what the evidence does and does not back, so it must be shown
// the stage's own request and evidence. If it were shown the running prompt
// instead, the second edge on a stage would be reframing against the first
// edge's output — reading an instruction not to fabricate as though it were part
// of what the user asked for.
func TestEachEdgeReframesAgainstTheStagesOwnInput(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		_ = json.Unmarshal(body, &req)
		for _, m := range req.Messages {
			seen = append(seen, m.Content)
		}
		fmt.Fprint(w, `{"choices":[{"message":{"content":"EDGE OUTPUT"}}]}`)
	}))
	defer srv.Close()

	a := &Agent{executor: llm.NewClient(srv.URL, "k", "test-light")}
	g := NewGraph()
	searchNode(g, "s1", "https://ref.example/unread") // grounded, never fetched
	failID := g.AddNode(&Node{Type: NodeTool, Tag: "reader", ToolName: "read_file"})
	g.SetBody(failID, toolMessageBody{msg: toolapi.ToolFail("file", "no such file", nil)})

	const stageInput = "THE ORIGINAL REQUEST AND EVIDENCE"
	p := NewStagePrompts("ROLE", stageInput)
	p = a.FrameCoverage(context.Background(), g, p)
	p = a.FrameGrounding(context.Background(), g, p)

	if !strings.Contains(p.User, "EDGE OUTPUT") {
		t.Fatalf("neither edge fired, so this proves nothing:\n%s", p.User)
	}
	var groundingCall string
	for _, m := range seen {
		if strings.HasPrefix(m, "REQUEST:\n") {
			groundingCall = m
		}
	}
	if groundingCall == "" {
		t.Fatal("the grounding edge never ran")
	}
	if !strings.Contains(groundingCall, stageInput) {
		t.Errorf("the grounding edge was not shown the stage's own input:\n%s", groundingCall)
	}
	if strings.Contains(groundingCall, "EDGE OUTPUT") {
		t.Error("the grounding edge was shown the coverage edge's output as though it were the request")
	}
}

// A stage that stops applying an edge still answers — it just answers on
// evidence that never arrived, with nothing to say so. Nothing else fails, so
// this pins the call sites. Both stages sit behind an LLM call and cannot be run
// here. Matched loosely on whitespace so gofmt realignment is not a false alarm.
func TestEveryAnsweringStageAppliesTheCoverageEdge(t *testing.T) {
	for _, c := range []struct{ file, stage string }{
		{"aggregator.go", "the aggregator"},
		{"reflection.go", "the reflector, which often writes the answer directly"},
	} {
		src, err := os.ReadFile(c.file)
		if err != nil {
			t.Fatalf("read %s: %v", c.file, err)
		}
		if !regexp.MustCompile(`FrameCoverage\(ctx,\s*graph,`).Match(src) {
			t.Errorf("%s no longer applies the coverage edge", c.stage)
		}
	}
}

func TestTheReflectorAppliesTheGroundingEdge(t *testing.T) {
	src, err := os.ReadFile("reflection.go")
	if err != nil {
		t.Fatalf("read reflection.go: %v", err)
	}
	if !regexp.MustCompile(`FrameGrounding\(ctx,\s*graph,`).Match(src) {
		t.Error("the reflector no longer applies the grounding edge; it can name URLs from memory again")
	}
}
