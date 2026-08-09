package agent

import (
	"strings"
	"testing"

	agenttools "github.com/Compdeep/kaiju/agent/tools"
)

func unclassifiedBody(text string) toolMessageBody {
	return toolMessageBody{msg: agenttools.ToolUnclassified(text)}
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
	body := unclassifiedBody(`{"text":"3 processes","count":3,"host":"queen-1"}`)

	if v, ok := body.Field("count"); !ok || v != float64(3) {
		t.Errorf("Field(count) = (%v, %v), want (3, true)", v, ok)
	}
	if v, ok := body.Field("host"); !ok || v != "queen-1" {
		t.Errorf("Field(host) = (%v, %v), want (queen-1, true)", v, ok)
	}
	// The whole result, as RawTextBody returned for an empty path.
	if v, ok := body.Field(""); !ok || !strings.Contains(v.(string), "queen-1") {
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
	if env.Status == agenttools.StatusOK {
		t.Error("must not claim the tool found something")
	}
	if env.Status == agenttools.StatusEmpty {
		t.Error("must not claim the tool found nothing")
	}
	if env.Status != agenttools.StatusUnclassified {
		t.Fatalf("status = %q", env.Status)
	}
}

// It reaches the model as a stated fact rather than as silence.
func TestUnclassified_IsReportedToTheCoverageStatement(t *testing.T) {
	a := &Agent{}
	g := NewGraph()
	id := g.AddNode(&Node{Type: NodeTool, Tag: "hashcheck", ToolName: "lookup_hash"})
	g.SetBody(id, unclassifiedBody("No results found in MalwareBazaar."))

	gaps := a.collectGaps(g)
	if len(gaps) != 1 {
		t.Fatalf("want the undeclared outcome reported, got %d entries", len(gaps))
	}
	if gaps[0].Tag != "hashcheck" || !strings.Contains(gaps[0].Detail, "did not report") {
		t.Fatalf("gap = %+v, want it to name the tool and say the outcome was not declared", gaps[0])
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

func TestEmptyAndError_StillCountAsGapsAndNotAsEvidence(t *testing.T) {
	a := &Agent{}
	g := NewGraph()
	e := g.AddNode(&Node{Type: NodeTool, Tag: "procs", ToolName: "list_processes"})
	g.SetBody(e, toolMessageBody{msg: agenttools.ToolEmpty("listing", "no processes matched")})

	if gaps := a.collectGaps(g); len(gaps) != 1 || gaps[0].Detail != "no processes matched" {
		t.Fatalf("empty must still be reported with its own detail, got %+v", gaps)
	}
	if a.hasUsableEvidence(g) {
		t.Error("an empty result is not usable evidence on its own")
	}
}
