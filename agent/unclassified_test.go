package agent

import (
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

func unclassifiedBody(text string) toolMessageBody {
	return toolMessageBody{msg: toolapi.ToolUnclassified(text)}
}

// A tool that never declared an outcome used to enter the graph as a bare
// string. Everything it did then must still hold now that it enters as an
// envelope, or the consolidation changed behaviour it was meant to preserve.

func TestUnclassified_EvidenceIsTheTextUnchanged(t *testing.T) {
	const prose = "## Hash: abc\nNo results found in MalwareBazaar."
	if got := unclassifiedBody(prose).Evidence(); got != prose {
		t.Fatalf("Evidence() = %q, want the producer's text unchanged", got)
	}
}

func TestUnclassified_FieldResolvesFromTheTopOfTheText(t *testing.T) {
	// The shape jsonResult produces: fields at the top level, no envelope.
	body := unclassifiedBody(`{"text":"3 processes","count":3,"host":"host-1"}`)

	if v, ok := body.Field("count"); !ok || v != float64(3) {
		t.Errorf("Field(count) = (%v, %v), want (3, true)", v, ok)
	}
	if v, ok := body.Field("host"); !ok || v != "host-1" {
		t.Errorf("Field(host) = (%v, %v), want (host-1, true)", v, ok)
	}
	// The whole result, as RawTextBody returned for an empty path.
	if v, ok := body.Field(""); !ok || !strings.Contains(v.(string), "host-1") {
		t.Errorf("Field(\"\") = (%v, %v), want the whole text", v, ok)
	}
	// A miss is a miss, not a panic.
	if _, ok := body.Field("nope"); ok {
		t.Error("Field(nope) should miss")
	}
}

func TestUnclassified_FieldOnProseMissesRatherThanPanics(t *testing.T) {
	if _, ok := unclassifiedBody("not json at all").Field("anything"); ok {
		t.Error("a dot-path into prose should miss")
	}
}

func TestUnclassified_SummaryIsTheFirstLine(t *testing.T) {
	got := unclassifiedBody("\n\n  first real line  \nsecond").Summary()
	if got != "first real line" {
		t.Fatalf("Summary() = %q, want the first non-empty line as the trace showed before", got)
	}
}

// The point of the status: it must not be mistaken for either of the two
// claims it deliberately does not make.
func TestUnclassified_IsNotOkAndNotEmpty(t *testing.T) {
	env := unclassifiedBody("something").Envelope()
	if env.Status == toolapi.StatusOK {
		t.Error("must not claim the tool found something")
	}
	if env.Status == toolapi.StatusEmpty {
		t.Error("must not claim the tool found nothing")
	}
	if env.Status != toolapi.StatusUnclassified {
		t.Fatalf("status = %q", env.Status)
	}
}

// And it counts as evidence: a run whose tools all decline to declare an
// outcome has still gathered something, and must not be treated as having
// gathered nothing.
func TestUnclassified_CountsAsUsableEvidence(t *testing.T) {
	a := &Agent{}
	g := NewGraph()
	id := g.AddNode(&Node{Type: NodeTool, Tag: "probe", ToolName: "port_scan"})
	g.SetBody(id, unclassifiedBody("22/tcp open\n443/tcp open"))

	if !a.hasUsableEvidence(g) {
		t.Fatal("a readable result with an undeclared outcome is still evidence")
	}
}

func TestEmptyIsNotUsableEvidence(t *testing.T) {
	a := &Agent{}
	g := NewGraph()
	e := g.AddNode(&Node{Type: NodeTool, Tag: "procs", ToolName: "list_processes"})
	g.SetBody(e, toolMessageBody{msg: toolapi.ToolEmpty("listing", "no processes matched")})

	if a.hasUsableEvidence(g) {
		t.Error("an empty result is not usable evidence on its own")
	}
}
