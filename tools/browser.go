package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Compdeep/kaiju/agent"
	"github.com/Compdeep/kaiju/agent/toolapi"
	"github.com/Compdeep/kaiju/internal/browserbridge"
)

// The browser tools drive the person's own browser through the Kaiju Browser
// Relay extension (extension/ at the repository root), over the bridge in
// internal/browserbridge.
//
// Every one of them is about a browser that belongs to a person, not to the
// machine: the browser a run reaches is one its own principal paired, and none
// of them names a target, because a browser is reached through the extension
// wherever it is rather than by running something on a host.

// Browser is what the tools need from the bridge. An interface so a test can
// answer for a browser without a socket.
type Browser interface {
	Pair(username string) (string, time.Time, error)
	Status(username string) ([]browserbridge.BrowserStatus, error)
	Do(ctx context.Context, c browserbridge.Call) (json.RawMessage, error)
}

// browserParam is the optional parameter every browser tool that talks to a
// browser takes, for a person with more than one connected.
const browserParam = `"browser": {"type": "string", "description": "Which connected browser, by name or id. Leave out to use the one most recently used."}`

// browserFailure renders a bridge error as the tool's failure, saying which
// side failed: no browser, a dropped connection, or the extension refusing.
func browserFailure(kind string, err error) toolapi.ToolMessage {
	var remote *browserbridge.RemoteError
	switch {
	case errors.Is(err, browserbridge.ErrNoBrowser):
		return toolapi.ToolFail(kind, err.Error(), map[string]string{"reason": "no_browser"})
	case errors.Is(err, browserbridge.ErrDisconnected), errors.Is(err, browserbridge.ErrTimeout):
		return toolapi.ToolFail(kind, err.Error(), map[string]string{"reason": "connection"})
	case errors.As(err, &remote):
		return toolapi.ToolFail(kind, remote.Message, map[string]string{"reason": "browser_refused", "op": remote.Op})
	default:
		return toolapi.ToolFail(kind, err.Error(), nil)
	}
}

// ─── browser_pair ───────────────────────────────────────────────────────────

/*
 * BrowserPair creates a code the person pastes into the extension to connect
 * their browser.
 * desc: The code works once and expires in ten minutes. On first use the
 *       extension trades it for a secret of its own, so the code left behind
 *       in this conversation is worthless afterwards.
 */
type BrowserPair struct{ b Browser }

// NewBrowserPair returns the tool over a bridge.
func NewBrowserPair(b Browser) *BrowserPair { return &BrowserPair{b: b} }

func (t *BrowserPair) Name() string { return "browser_pair" }

func (t *BrowserPair) Description() string {
	return "Create a one-time pairing code that connects the person's own Chrome to Kaiju through the Kaiju Browser Relay extension. Use when they ask to connect, pair or link their browser. Show them the code and tell them to paste it into the extension's options page; it expires in 10 minutes if unused."
}

// Impact is control: what this creates is the means to drive someone's
// logged-in browser, which is as far as any tool here reaches.
func (t *BrowserPair) Impact(map[string]any) int { return toolapi.ImpactControl }

// RequiresHuman: a code nobody is there to paste is a credential lying in a
// transcript for no reason.
func (t *BrowserPair) RequiresHuman() bool { return true }

// RequiresTarget is false: see the note at the top of this file.
func (t *BrowserPair) RequiresTarget() bool { return false }

func (t *BrowserPair) Parameters() json.RawMessage {
	return json.RawMessage(`{"type": "object", "properties": {}, "additionalProperties": false}`)
}

type browserPairData struct {
	Code      string `json:"code" desc:"the pairing code to paste into the extension's options page"`
	ExpiresAt string `json:"expires_at" desc:"when the code stops working if it has not been used, RFC 3339"`
}

func (t *BrowserPair) OutputSchema() json.RawMessage {
	return toolapi.EnvelopeSchema(toolapi.PayloadSchemaOf(browserPairData{}))
}

func (t *BrowserPair) Execute(ctx context.Context, params map[string]any) (string, error) {
	return toolapi.StringResult(t.ExecuteTyped(ctx, params))
}

func (t *BrowserPair) ExecuteTyped(ctx context.Context, _ map[string]any) (toolapi.ToolMessage, error) {
	code, expires, err := t.b.Pair(agent.UsernameFrom(ctx))
	if err != nil {
		return toolapi.ToolFail("status", "could not create a pairing code: "+err.Error(), nil), nil
	}
	data := browserPairData{Code: code, ExpiresAt: expires.UTC().Format(time.RFC3339)}
	content := fmt.Sprintf("Pairing code: %s\n\n"+
		"1. Open the Kaiju Browser Relay extension's options page (right-click its icon → Options).\n"+
		"2. Check the server address, paste the code into \"Pairing code\" and press Pair.\n"+
		"3. The extension shows Connected when it is done.\n\n"+
		"The code works once and expires at %s if it is not used.", code, data.ExpiresAt)
	return toolapi.ToolOK("status", content, data), nil
}

// ─── browser_status ─────────────────────────────────────────────────────────

/*
 * BrowserStatus reports which of the person's browsers are connected.
 * desc: Connected browsers with their round-trip time and last contact, and
 *       paired ones that are not connected right now.
 */
type BrowserStatus struct{ b Browser }

// NewBrowserStatus returns the tool over a bridge.
func NewBrowserStatus(b Browser) *BrowserStatus { return &BrowserStatus{b: b} }

func (t *BrowserStatus) Name() string { return "browser_status" }

func (t *BrowserStatus) Description() string {
	return "Show whether the person's browser is connected to Kaiju: each paired browser, connected or not, with its ping time, extension version and last contact. Use before browser work when unsure a browser is there, or when a browser tool reported no browser."
}

func (t *BrowserStatus) Impact(map[string]any) int { return toolapi.ImpactObserve }

func (t *BrowserStatus) RequiresTarget() bool { return false }

func (t *BrowserStatus) Parameters() json.RawMessage {
	return json.RawMessage(`{"type": "object", "properties": {}, "additionalProperties": false}`)
}

type browserStatusData struct {
	Connected int                           `json:"connected" desc:"how many browsers are connected now"`
	Browsers  []browserbridge.BrowserStatus `json:"browsers" desc:"every paired browser, connected first"`
}

func (t *BrowserStatus) OutputSchema() json.RawMessage {
	return toolapi.EnvelopeSchema(toolapi.PayloadSchemaOf(browserStatusData{}))
}

func (t *BrowserStatus) Execute(ctx context.Context, params map[string]any) (string, error) {
	return toolapi.StringResult(t.ExecuteTyped(ctx, params))
}

func (t *BrowserStatus) ExecuteTyped(ctx context.Context, _ map[string]any) (toolapi.ToolMessage, error) {
	list, err := t.b.Status(agent.UsernameFrom(ctx))
	if err != nil {
		return toolapi.ToolFail("status", "could not read paired browsers: "+err.Error(), nil), nil
	}
	if len(list) == 0 {
		return toolapi.ToolEmpty("status", "no browser has been paired — use browser_pair to create a pairing code"), nil
	}
	data := browserStatusData{Browsers: list}
	var sb strings.Builder
	for _, s := range list {
		if s.Connected {
			data.Connected++
			fmt.Fprintf(&sb, "%s (%s): connected, ping %dms, extension %s, since %s\n", s.Name, s.ID, s.PingMS, s.Extension, s.ConnectedAt)
		} else {
			last := s.LastSeen
			if last == "" {
				last = "never"
			}
			fmt.Fprintf(&sb, "%s (%s): not connected, last seen %s\n", s.Name, s.ID, last)
		}
	}
	return toolapi.ToolOK("status", strings.TrimSpace(sb.String()), data), nil
}

// ─── browser_tabs ───────────────────────────────────────────────────────────

/*
 * BrowserTabs lists the tabs open in the person's browser.
 * desc: Every tab in every window, with the id the other browser tools take.
 *       Asked of the browser each time, never cached: the person may have
 *       closed half of them since the last look.
 */
type BrowserTabs struct{ b Browser }

// NewBrowserTabs returns the tool over a bridge.
func NewBrowserTabs(b Browser) *BrowserTabs { return &BrowserTabs{b: b} }

func (t *BrowserTabs) Name() string { return "browser_tabs" }

func (t *BrowserTabs) Description() string {
	return "List the tabs open in the person's own browser: id, title, URL, window, and which one is active. Optionally filter by words in the title or URL. The tab id is what other browser tools take."
}

func (t *BrowserTabs) Impact(map[string]any) int { return toolapi.ImpactObserve }

func (t *BrowserTabs) RequiresTarget() bool { return false }

func (t *BrowserTabs) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"filter": {"type": "string", "description": "Only tabs whose title or URL contains this, case-insensitive."},
			` + browserParam + `
		},
		"additionalProperties": false
	}`)
}

// BrowserTab is one tab as the extension reports it.
type BrowserTab struct {
	ID       int    `json:"id" desc:"the tab id other browser tools take"`
	WindowID int    `json:"window_id" desc:"the window the tab is in"`
	Index    int    `json:"index" desc:"position in its window, from 0"`
	Active   bool   `json:"active" desc:"the selected tab in its window"`
	Pinned   bool   `json:"pinned,omitempty" desc:"pinned"`
	Audible  bool   `json:"audible,omitempty" desc:"playing sound"`
	Status   string `json:"status,omitempty" desc:"loading or complete"`
	Title    string `json:"title" desc:"the page title"`
	URL      string `json:"url" desc:"the page address"`
}

type browserTabsData struct {
	Browser string       `json:"browser" desc:"the browser that answered"`
	Count   int          `json:"count" desc:"tabs returned, after the filter"`
	Total   int          `json:"total" desc:"tabs open in the browser"`
	Filter  string       `json:"filter,omitempty" desc:"the filter applied, when one was given"`
	Tabs    []BrowserTab `json:"tabs" desc:"the tabs, by window then position"`
}

func (t *BrowserTabs) OutputSchema() json.RawMessage {
	return toolapi.EnvelopeSchema(toolapi.PayloadSchemaOf(browserTabsData{}))
}

func (t *BrowserTabs) Execute(ctx context.Context, params map[string]any) (string, error) {
	return toolapi.StringResult(t.ExecuteTyped(ctx, params))
}

func (t *BrowserTabs) ExecuteTyped(ctx context.Context, params map[string]any) (toolapi.ToolMessage, error) {
	filter, _ := params["filter"].(string)
	browser, _ := params["browser"].(string)
	raw, err := t.b.Do(ctx, browserbridge.Call{
		Username: agent.UsernameFrom(ctx), Browser: browser,
		Op: "tabs.list", Idempotent: true,
	})
	if err != nil {
		return browserFailure("listing", err), nil
	}
	var res struct {
		Browser string       `json:"browser"`
		Tabs    []BrowserTab `json:"tabs"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return toolapi.ToolFail("listing", "the browser's tab list could not be read: "+err.Error(), nil), nil
	}
	data := browserTabsData{Browser: res.Browser, Total: len(res.Tabs), Filter: filter, Tabs: []BrowserTab{}}
	needle := strings.ToLower(strings.TrimSpace(filter))
	var sb strings.Builder
	for _, tab := range res.Tabs {
		if needle != "" && !strings.Contains(strings.ToLower(tab.Title), needle) && !strings.Contains(strings.ToLower(tab.URL), needle) {
			continue
		}
		data.Tabs = append(data.Tabs, tab)
		mark := " "
		if tab.Active {
			mark = "*"
		}
		fmt.Fprintf(&sb, "%s tab %d (window %d): %s — %s\n", mark, tab.ID, tab.WindowID, tab.Title, tab.URL)
	}
	data.Count = len(data.Tabs)
	if data.Count == 0 {
		if needle != "" {
			return toolapi.ToolEmptyWith("listing", fmt.Sprintf("no tab's title or URL contains %q (%d tabs open)", filter, data.Total), data), nil
		}
		return toolapi.ToolEmptyWith("listing", "the browser has no tabs open", data), nil
	}
	return toolapi.ToolOK("listing", strings.TrimSpace(sb.String()), data), nil
}

var (
	_ toolapi.Tool = (*BrowserPair)(nil)
	_ toolapi.Tool = (*BrowserStatus)(nil)
	_ toolapi.Tool = (*BrowserTabs)(nil)
)
