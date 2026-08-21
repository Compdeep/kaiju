package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// The file being whole and the text beside it being a fraction of that file are
// two different facts. A stage told only the first treats the text it holds as
// the whole document and reports an answer drawn from part of one as covering
// all of it — which is what happened on a 1.2 MB page whose inline text was
// 1.4% of it.
func TestEvidence_SaysWhenTheTextIsPartOfTheFile(t *testing.T) {
	body := NewToolBody(toolapi.ToolMessage{
		Type:    "web_fetch",
		Status:  toolapi.StatusOK,
		Content: "the opening of the document",
		Data: json.RawMessage(`{"path":"fetched/doc.txt","bytes":1234609,
			"content_bytes":17110,"content_truncated":true}`),
	})

	ev := body.Evidence()

	if !strings.Contains(ev, "fetched/doc.txt") {
		t.Fatalf("the file must still be named: %q", ev)
	}
	if !strings.Contains(ev, "not the whole of it") {
		t.Fatalf("the text being partial must be stated: %q", ev)
	}
	if !strings.Contains(ev, "17110 bytes of the 1234609") {
		t.Fatalf("both sizes must appear so the difference is readable: %q", ev)
	}
	if !strings.Contains(ev, "${step.N.path}") {
		t.Fatalf("the way to reach all of it must be given: %q", ev)
	}
}

// Saying it when it is not true would push every small page onto a file read.
func TestEvidence_SilentWhenTheTextIsTheWholeFile(t *testing.T) {
	body := NewToolBody(toolapi.ToolMessage{
		Type:    "web_fetch",
		Status:  toolapi.StatusOK,
		Content: "a short page, whole",
		Data:    json.RawMessage(`{"path":"fetched/small.txt","bytes":512,"content_bytes":512}`),
	})

	ev := body.Evidence()

	if strings.Contains(ev, "not the whole of it") {
		t.Fatalf("nothing was cut, so nothing should be claimed: %q", ev)
	}
	if !strings.Contains(ev, "fetched/small.txt") {
		t.Fatalf("the file is still named: %q", ev)
	}
}

// A tool that does not measure its inline value must not gain a claim about it.
func TestEvidence_UnmeasuredHandleSaysNothingAboutItsInline(t *testing.T) {
	body := NewToolBody(toolapi.ToolMessage{
		Type:    "bash",
		Status:  toolapi.StatusOK,
		Content: "some stdout",
		Data:    json.RawMessage(`{"output_path":"output/run.txt","bytes":900,"content_truncated":true}`),
	})

	if ev := body.Evidence(); strings.Contains(ev, "not the whole of it") {
		t.Fatalf("output_path names no inline flag, so nothing should be claimed: %q", ev)
	}
}
