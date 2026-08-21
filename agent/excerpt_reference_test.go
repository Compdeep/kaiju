package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// excerptTool declares that one of its fields is truncated. plainTool is the
// same tool declaring nothing. The pair is what proves the engine reads the
// declaration rather than recognising field names of its own.
//
// The names here are deliberately not the ones any real tool uses: if the engine
// ever starts looking for "content" or "path", these tests keep passing while
// production breaks, so odd names are the point.
type excerptTool struct {
	name string
	dec  []toolapi.Excerpt
}

func (e *excerptTool) Name() string                { return e.name }
func (e *excerptTool) Description() string         { return "" }
func (e *excerptTool) Parameters() json.RawMessage { return json.RawMessage(`{}`) }
func (e *excerptTool) Impact(map[string]any) int   { return 0 }
func (e *excerptTool) Execute(context.Context, map[string]any) (string, error) {
	return "", nil
}
func (e *excerptTool) Excerpts() []toolapi.Excerpt { return e.dec }

var _ toolapi.Tool = (*excerptTool)(nil)
var _ toolapi.Excerpting = (*excerptTool)(nil)

const (
	partField   = "part"
	wholeField  = "everything"
	sizeField   = "how_big"
	useWording  = "read everything in this step, part is only the opening"
	partialData = `{"part":"the opening","everything":"kept/doc.txt","how_big":1219043}`
)

func excerptGraph(t *testing.T, payload string, declares bool) (*toolapi.Registry, *Graph, string) {
	t.Helper()
	reg := toolapi.NewRegistry()
	tool := &excerptTool{name: "page_reader"}
	if declares {
		tool.dec = []toolapi.Excerpt{{Field: partField, Whole: wholeField, Size: sizeField, Use: useWording}}
	}
	reg.Replace(tool, "builtin")

	g := NewGraph()
	id := g.AddNode(&Node{Type: NodeTool, Tag: "read", ToolName: "page_reader"})
	g.SetBody(id, toolMessageBody{msg: toolapi.ToolMessage{
		Type:    "page",
		Status:  toolapi.StatusOK,
		Content: "the opening",
		Data:    json.RawMessage(payload),
	}})
	return reg, g, id
}

// The failure this exists for: a step was handed 8,102 of 1,219,043 characters,
// counted inside them, and reported the number as the document's — three runs
// running — while the whole copy sat unread in a file the same result named.
func TestWholeBesideExcerpt_HandsOverBothWhenOnlyPartWasInline(t *testing.T) {
	reg, g, id := excerptGraph(t, partialData, true)

	widened, ok := wholeBesideExcerpt(reg, g, id, partField, "the opening")
	if !ok {
		t.Fatal("a reference to the truncated field must be widened while the whole exists")
	}
	if widened[partField] != "the opening" {
		t.Fatalf("the text must survive, got %v", widened[partField])
	}
	if widened[wholeField] != "kept/doc.txt" {
		t.Fatalf("the file must be named, got %v", widened[wholeField])
	}
	if widened[sizeField] != 1219043 {
		t.Fatalf("the size must be carried, got %v", widened[sizeField])
	}
	if widened["note"] != useWording {
		t.Fatalf("the tool's own wording must ride along, got %v", widened["note"])
	}
}

// THE GUARD AGAINST THE ENGINE KNOWING TOOL FIELD NAMES.
//
// Byte-for-byte the payload widened above. The only difference is that the tool
// declares nothing. If this fails, something in the engine has started
// recognising field names instead of reading toolapi.Excerpting.
func TestWholeBesideExcerpt_EngineHoldsNoFieldNamesOfItsOwn(t *testing.T) {
	reg, g, id := excerptGraph(t, partialData, false)

	if _, ok := wholeBesideExcerpt(reg, g, id, partField, "the opening"); ok {
		t.Fatal("a tool that declared nothing must be left alone, whatever its fields are called")
	}
}

func TestWholeBesideExcerpt_LeavesWhatCameBackWholeAlone(t *testing.T) {
	whole := "a short page, entire"
	reg, g, id := excerptGraph(t, `{"part":"`+whole+`","everything":"kept/small.txt","how_big":20}`, true)

	if _, ok := wholeBesideExcerpt(reg, g, id, partField, whole); ok {
		t.Fatal("text matching the file is the whole of it; nothing to widen")
	}
}

// A tool with nowhere to write still returns its text, and that text is then all
// there is — widening would name a file that does not exist.
func TestWholeBesideExcerpt_LeavesItAloneWhenTheToolKeptNothing(t *testing.T) {
	reg, g, id := excerptGraph(t, `{"part":"some text","kept":"read but not written"}`, true)

	if _, ok := wholeBesideExcerpt(reg, g, id, partField, "some text"); ok {
		t.Fatal("no file means the text is all there is")
	}
}

func TestWholeBesideExcerpt_LeavesUndeclaredFieldsAlone(t *testing.T) {
	reg, g, id := excerptGraph(t, partialData, true)

	for _, field := range []string{wholeField, sizeField, "title", "status"} {
		if _, ok := wholeBesideExcerpt(reg, g, id, field, "something"); ok {
			t.Fatalf("field %q was never declared as truncated", field)
		}
	}
}

func TestWholeBesideExcerpt_SilentWhenItCannotTell(t *testing.T) {
	reg, g, id := excerptGraph(t, `{"part":"x","everything":"kept/doc.txt"}`, true) // no size
	if _, ok := wholeBesideExcerpt(reg, g, id, partField, "x"); ok {
		t.Fatal("an unstated size must not widen")
	}
	if _, ok := wholeBesideExcerpt(reg, g, "no-such-node", partField, "x"); ok {
		t.Fatal("an unknown producer must not widen")
	}
	if _, ok := wholeBesideExcerpt(reg, g, id, partField, 42); ok {
		t.Fatal("a value that is not text is not truncated text")
	}
	if _, ok := wholeBesideExcerpt(nil, g, id, partField, "x"); ok {
		t.Fatal("no registry must not widen")
	}
	if _, ok := wholeBesideExcerpt(reg, nil, id, partField, "x"); ok {
		t.Fatal("no graph must not widen")
	}
}

// Widening only holds where the whole parameter is one reference, because that
// is the only branch that keeps the value's type. A reference sitting inside a
// longer sentence is rendered as text, and an object rendered into the middle of
// a sentence would be worse than the text it replaced — so that case keeps the
// text it always had.
func TestSubstituteTemplates_WidensOnlyABareReference(t *testing.T) {
	reg, g, id := excerptGraph(t, partialData, true)

	bare := &Node{Type: NodeCompute, Params: map[string]any{"context": "${node." + id + "." + partField + "}"}}
	if err := substituteTemplates(bare, g, reg); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	got, isObject := bare.Params["context"].(map[string]any)
	if !isObject {
		t.Fatalf("a bare reference must arrive as an object, got %T", bare.Params["context"])
	}
	if got[wholeField] != "kept/doc.txt" {
		t.Fatalf("the file must be named in it, got %v", got)
	}

	embedded := &Node{Type: NodeCompute, Params: map[string]any{
		"context": "the page said: ${node." + id + "." + partField + "} — count in it",
	}}
	if err := substituteTemplates(embedded, g, reg); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	text, isText := embedded.Params["context"].(string)
	if !isText {
		t.Fatalf("an embedded reference must stay text, got %T", embedded.Params["context"])
	}
	if !strings.Contains(text, "the opening") || strings.Contains(text, "kept/doc.txt") {
		t.Fatalf("an embedded reference keeps the text it always had, got %q", text)
	}
}
