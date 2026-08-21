package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// producerNode stands up a resolved node carrying the payload a tool returned,
// so the check reads it exactly as it does at fire time.
func producerNode(t *testing.T, payload string) (*Graph, string) {
	t.Helper()
	g := NewGraph()
	id := g.AddNode(&Node{Type: NodeTool, Tag: "fetch", ToolName: "web_fetch"})
	g.SetBody(id, toolMessageBody{msg: toolapi.ToolMessage{
		Type:    "page",
		Status:  toolapi.StatusOK,
		Content: "the opening of the document",
		Data:    json.RawMessage(payload),
	}})
	return g, id
}

// The failure this exists for: a step counted inside 8,102 characters of a
// 1,219,043-character document and reported 17 as the document's total, three
// runs in a row, while the complete copy sat unread on disk.
func TestRefuseExcerptReference_RefusesAPartWhenTheWholeIsOnDisk(t *testing.T) {
	g, id := producerNode(t, `{"content":"the opening","path":"fetched/doc.txt","bytes":1219043}`)

	err := refuseExcerptReference(g, id, "content", "the opening")
	if err == nil {
		t.Fatal("a reference to part of a document must be refused while the whole exists")
	}
	for _, want := range []string{"fetched/doc.txt", "1219043", `"path"`} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal must name %s, got %q", want, err)
		}
	}
}

func TestRefuseExcerptReference_AllowsWhatCameBackWhole(t *testing.T) {
	whole := "a short page, entire"
	g, id := producerNode(t, `{"content":"`+whole+`","path":"fetched/small.txt","bytes":20}`)

	if err := refuseExcerptReference(g, id, "content", whole); err != nil {
		t.Fatalf("content matching the file is the whole of it: %v", err)
	}
}

// A tool with nowhere to write still returns its excerpt, and that excerpt is
// then all there is — refusing it would leave the step with nothing.
func TestRefuseExcerptReference_AllowsWhenTheToolKeptNothing(t *testing.T) {
	g, id := producerNode(t, `{"content":"some text","kept":"the page was read but could not be written"}`)

	if err := refuseExcerptReference(g, id, "content", "some text"); err != nil {
		t.Fatalf("no file means the excerpt is all there is: %v", err)
	}
}

// Only fields a tool declared as an excerpt are governed. A title is short
// beside a large file and must not be mistaken for a cut copy of it.
func TestRefuseExcerptReference_LeavesOtherFieldsAlone(t *testing.T) {
	g, id := producerNode(t, `{"content":"x","title":"A Page","path":"fetched/doc.txt","bytes":1219043}`)

	for _, field := range []string{"title", "path", "bytes", "status", "format"} {
		if err := refuseExcerptReference(g, id, field, "A Page"); err != nil {
			t.Fatalf("field %q is not an excerpt, got %v", field, err)
		}
	}
}

func TestRefuseExcerptReference_CoversTheCommandOutputPair(t *testing.T) {
	g := NewGraph()
	id := g.AddNode(&Node{Type: NodeTool, Tag: "run", ToolName: "bash"})
	g.SetBody(id, toolMessageBody{msg: toolapi.ToolMessage{
		Type:   "command",
		Status: toolapi.StatusOK,
		Data:   json.RawMessage(`{"stdout":"first lines","output_path":"output/run.txt","output_bytes":900000}`),
	}})

	err := refuseExcerptReference(g, id, "stdout", "first lines")
	if err == nil {
		t.Fatal("stdout cut from a much larger output must be refused too")
	}
	if !strings.Contains(err.Error(), "output_path") {
		t.Fatalf("the refusal must name the field holding all of it, got %q", err)
	}
}

// A size that never survived a JSON round trip, a missing node, and a non-string
// value must all pass rather than be guessed at.
func TestRefuseExcerptReference_SilentWhenItCannotTell(t *testing.T) {
	g, id := producerNode(t, `{"content":"x","path":"fetched/doc.txt"}`) // no size stated
	if err := refuseExcerptReference(g, id, "content", "x"); err != nil {
		t.Fatalf("an unstated size must not refuse: %v", err)
	}
	if err := refuseExcerptReference(g, "no-such-node", "content", "x"); err != nil {
		t.Fatalf("an unknown producer must not refuse: %v", err)
	}
	if err := refuseExcerptReference(g, id, "content", 42); err != nil {
		t.Fatalf("a non-text value is not an excerpt: %v", err)
	}
	if err := refuseExcerptReference(nil, id, "content", "x"); err != nil {
		t.Fatalf("no graph must not refuse: %v", err)
	}
}
