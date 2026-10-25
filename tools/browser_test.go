package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Compdeep/kaiju/agent/toolapi"
	"github.com/Compdeep/kaiju/internal/browserbridge"
)

// fakeBrowser answers for a bridge without a socket.
type fakeBrowser struct {
	result json.RawMessage
	err    error
	calls  []browserbridge.Call
	status []browserbridge.BrowserStatus
}

func (f *fakeBrowser) Pair(string) (string, time.Time, error) {
	return "kjp-testcode", time.Date(2026, 1, 1, 0, 10, 0, 0, time.UTC), nil
}

func (f *fakeBrowser) Status(string) ([]browserbridge.BrowserStatus, error) { return f.status, nil }

func (f *fakeBrowser) Do(_ context.Context, c browserbridge.Call) (json.RawMessage, error) {
	f.calls = append(f.calls, c)
	return f.result, f.err
}

func TestBrowserTabsFiltersAndReadsAsIdempotent(t *testing.T) {
	fb := &fakeBrowser{result: json.RawMessage(`{"browser":"home","tabs":[
		{"id":1,"window_id":9,"title":"Inbox","url":"https://mail.example.com","active":true},
		{"id":2,"window_id":9,"title":"Docs","url":"https://docs.example.com"}]}`)}
	msg, err := NewBrowserTabs(fb).ExecuteTyped(context.Background(), map[string]any{"filter": "DOCS"})
	if err != nil {
		t.Fatal(err)
	}
	if msg.Status != toolapi.StatusOK || !strings.Contains(msg.Content, "tab 2") || strings.Contains(msg.Content, "Inbox") {
		t.Fatalf("filter not applied: %+v", msg)
	}
	var data browserTabsData
	json.Unmarshal(msg.Data, &data)
	if data.Count != 1 || data.Total != 2 {
		t.Fatalf("count %d total %d, want 1 and 2", data.Count, data.Total)
	}
	if len(fb.calls) != 1 || fb.calls[0].Op != "tabs.list" || !fb.calls[0].Idempotent {
		t.Fatalf("expected one idempotent tabs.list, got %+v", fb.calls)
	}
}

func TestBrowserTabsSaysWhenNoBrowserIsConnected(t *testing.T) {
	fb := &fakeBrowser{err: browserbridge.ErrNoBrowser}
	msg, _ := NewBrowserTabs(fb).ExecuteTyped(context.Background(), nil)
	if msg.Status != toolapi.StatusError || !strings.Contains(msg.Detail, "browser_pair") {
		t.Fatalf("a missing browser should fail and point at browser_pair: %+v", msg)
	}
}

func TestBrowserPairShowsTheCodeAndIsControlForAHuman(t *testing.T) {
	p := NewBrowserPair(&fakeBrowser{})
	msg, _ := p.ExecuteTyped(context.Background(), nil)
	if msg.Status != toolapi.StatusOK || !strings.Contains(msg.Content, "kjp-testcode") {
		t.Fatalf("the code should be in the result: %+v", msg)
	}
	if p.Impact(nil) != toolapi.ImpactControl || !toolapi.RequiresHuman(p) {
		t.Fatal("browser_pair must be control impact and only offered with a person present")
	}
}

func TestBrowserToolsNameNoTarget(t *testing.T) {
	fb := &fakeBrowser{}
	for _, tool := range []toolapi.Tool{NewBrowserPair(fb), NewBrowserStatus(fb), NewBrowserTabs(fb)} {
		if toolapi.RequiresTarget(tool) {
			t.Errorf("%s requires a target; a browser is reached through the extension, not a host", tool.Name())
		}
	}
}

func TestBrowserStatusTellsConnectedFromPaired(t *testing.T) {
	fb := &fakeBrowser{status: []browserbridge.BrowserStatus{
		{ID: "brw-a", Name: "home", Connected: true, PingMS: 12, Extension: "0.1.0"},
		{ID: "brw-b", Name: "work"},
	}}
	msg, _ := NewBrowserStatus(fb).ExecuteTyped(context.Background(), nil)
	if !strings.Contains(msg.Content, "home (brw-a): connected, ping 12ms") || !strings.Contains(msg.Content, "work (brw-b): not connected, last seen never") {
		t.Fatalf("unexpected status: %s", msg.Content)
	}
}
