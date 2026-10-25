package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
	"github.com/Compdeep/kaiju/internal/browserbridge"
)

func TestBrowserClickNeedsSomethingToClick(t *testing.T) {
	fb := &fakeBrowser{}
	msg, _ := NewBrowserClick(fb).ExecuteTyped(context.Background(), map[string]any{"x": float64(10)})
	if msg.Status != toolapi.StatusError || len(fb.calls) != 0 {
		t.Fatalf("a click with no target (x without y) reached the browser: %+v", msg)
	}
}

func TestBrowserClickReportsNavigationAndANewTab(t *testing.T) {
	fb := &fakeBrowser{result: json.RawMessage(`{"action":"click","point":{"x":5,"y":6},
		"target":{"role":"link","name":"Next page","tag":"a"},
		"url":"https://shop.example.com/2","title":"Two","navigated":true,
		"opened_tab":{"id":77,"url":"https://help.example.com"}}`)}
	msg, _ := NewBrowserClick(fb).ExecuteTyped(context.Background(), map[string]any{"ref": "e3"})
	for _, want := range []string{`Clicked link "Next page"`, "went to https://shop.example.com/2", "read it again", "opened tab 77"} {
		if !strings.Contains(msg.Content, want) {
			t.Errorf("content lacks %q: %s", want, msg.Content)
		}
	}
	c := fb.calls[0]
	if c.Op != "input.click" || c.Idempotent || c.Args.(map[string]any)["ref"] != "e3" {
		t.Fatalf("unexpected call %+v", c)
	}
}

func TestBrowserActionsAreNeverRetried(t *testing.T) {
	fb := &fakeBrowser{result: json.RawMessage(`{}`)}
	ctx := context.Background()
	NewBrowserClick(fb).ExecuteTyped(ctx, map[string]any{"ref": "e1"})
	NewBrowserType(fb).ExecuteTyped(ctx, map[string]any{"text": "hi"})
	NewBrowserKey(fb).ExecuteTyped(ctx, map[string]any{"keys": "Enter"})
	NewBrowserSelect(fb).ExecuteTyped(ctx, map[string]any{"ref": "e2", "option": "Large"})
	for _, c := range fb.calls {
		if c.Idempotent {
			t.Errorf("%s is marked idempotent; a dropped connection would send it twice", c.Op)
		}
	}
	if len(fb.calls) != 4 {
		t.Fatalf("expected 4 calls, got %d", len(fb.calls))
	}
}

func TestBrowserTypeShowsWhatTheFieldHolds(t *testing.T) {
	fb := &fakeBrowser{result: json.RawMessage(`{"typed":5,"target":{"role":"textbox","name":"Search","tag":"input"},"field":"kaiju","submitted":true}`)}
	msg, _ := NewBrowserType(fb).ExecuteTyped(context.Background(), map[string]any{"ref": "e4", "text": "kaiju", "clear": true, "submit": true})
	if !strings.Contains(msg.Content, `into textbox "Search"`) || !strings.Contains(msg.Content, `holds "kaiju"`) || !strings.Contains(msg.Content, "Pressed Enter") {
		t.Fatalf("unexpected content: %s", msg.Content)
	}
	args := fb.calls[0].Args.(map[string]any)
	if args["clear"] != true || args["submit"] != true {
		t.Fatalf("clear and submit not passed: %+v", args)
	}
}

func TestBrowserWaitThatTimesOutIsEmptyNotOK(t *testing.T) {
	fb := &fakeBrowser{result: json.RawMessage(`{"found":false,"waited_ms":10000}`)}
	msg, _ := NewBrowserWait(fb).ExecuteTyped(context.Background(), map[string]any{"text": "Saved"})
	if msg.Status != toolapi.StatusEmpty || !strings.Contains(msg.Detail, "had not appeared") {
		t.Fatalf("a wait that ran out should say so: %+v", msg)
	}
}

func TestBrowserRefusalsReachThePlannerAsTheBrowsersWords(t *testing.T) {
	fb := &fakeBrowser{err: &browserbridge.RemoteError{Op: "input.click", Message: `button "Pay" is covered by div#cookie-banner`}}
	msg, _ := NewBrowserClick(fb).ExecuteTyped(context.Background(), map[string]any{"ref": "e9"})
	if msg.Status != toolapi.StatusError || !strings.Contains(msg.Detail, "cookie-banner") {
		t.Fatalf("the extension's reason should be the failure: %+v", msg)
	}
}

func TestPhaseThreeBrowserToolImpacts(t *testing.T) {
	fb := &fakeBrowser{}
	cases := map[toolapi.Tool]int{
		NewBrowserClick(fb):  toolapi.ImpactAffect,
		NewBrowserType(fb):   toolapi.ImpactAffect,
		NewBrowserKey(fb):    toolapi.ImpactAffect,
		NewBrowserSelect(fb): toolapi.ImpactAffect,
		NewBrowserScroll(fb): toolapi.ImpactObserve,
		NewBrowserWait(fb):   toolapi.ImpactObserve,
	}
	for tool, want := range cases {
		if got := tool.Impact(nil); got != want {
			t.Errorf("%s impact %d, want %d", tool.Name(), got, want)
		}
		if toolapi.RequiresTarget(tool) {
			t.Errorf("%s requires a target", tool.Name())
		}
	}
}
