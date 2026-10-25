package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Compdeep/kaiju/agent"
	"github.com/Compdeep/kaiju/agent/toolapi"
	"github.com/Compdeep/kaiju/internal/browserbridge"
)

// Phase three of the browser tools: acting on a page. The extension sends the
// mouse and keyboard through Chrome's DevTools protocol, so a page receives the
// same trusted events a person's hand makes.
//
// None of these is retried after a dropped connection: the first attempt may
// have landed, and clicking Pay twice is worse than saying it may not have.

const refParam = `"ref": {"type": "string", "description": "The element's ref from the latest browser_read of this tab, such as e12."}`
const selectorParam = `"selector": {"type": "string", "description": "A CSS selector, when there is no ref."}`

// targetArgs copies the ways of naming an element into a call's arguments.
func targetArgs(params map[string]any, args map[string]any) {
	args["tab"] = tabArg(params)
	for _, k := range []string{"ref", "selector"} {
		if v, _ := params[k].(string); strings.TrimSpace(v) != "" {
			args[k] = strings.TrimSpace(v)
		}
	}
}

// inputCall sends an action and decodes the extension's answer into out.
func inputCall(ctx context.Context, b Browser, params map[string]any, op string, args map[string]any, timeout time.Duration, out any) *toolapi.ToolMessage {
	browser, _ := params["browser"].(string)
	raw, err := b.Do(ctx, browserbridge.Call{
		Username: agent.UsernameFrom(ctx), Browser: browser,
		Op: op, Args: args, Timeout: timeout,
	})
	if err != nil {
		m := browserFailure("status", err)
		return &m
	}
	if err := json.Unmarshal(raw, out); err != nil {
		m := toolapi.ToolFail("status", "the browser's answer could not be read: "+err.Error(), nil)
		return &m
	}
	return nil
}

// pageAfter is what an action reports about the page once it has settled.
type pageAfter struct {
	URL       string `json:"url,omitempty" desc:"the tab's address after the action"`
	Title     string `json:"title,omitempty" desc:"the tab's title after the action"`
	Navigated bool   `json:"navigated,omitempty" desc:"the action took the tab to a different address"`
	TimedOut  bool   `json:"timed_out,omitempty" desc:"a page load it started had not finished when the wait ran out"`
	TabClosed bool   `json:"tab_closed,omitempty" desc:"the tab closed itself"`
}

func (p pageAfter) sentence() string {
	switch {
	case p.TabClosed:
		return " The tab closed."
	case p.Navigated:
		s := fmt.Sprintf(" The tab went to %s — %s; read it again before using element refs.", p.URL, p.Title)
		if p.TimedOut {
			s += " It was still loading when the wait ran out."
		}
		return s
	}
	return ""
}

type elementSummary struct {
	Role string `json:"role,omitempty" desc:"the element's role"`
	Name string `json:"name,omitempty" desc:"its visible label"`
	Tag  string `json:"tag,omitempty" desc:"its HTML tag"`
}

func (e *elementSummary) String() string {
	if e == nil {
		return ""
	}
	what := e.Role
	if what == "" {
		what = e.Tag
	}
	if e.Name != "" {
		return fmt.Sprintf("%s %q", what, e.Name)
	}
	return what
}

// ─── browser_click ──────────────────────────────────────────────────────────

/*
 * BrowserClick clicks, double-clicks, right-clicks or hovers on a page.
 * desc: By element ref from browser_read, by CSS selector, or at a point. The
 *       element is scrolled into view first, and the click is refused when
 *       something else covers it, a modal or a cookie banner, rather than
 *       landing on whatever is on top.
 */
type BrowserClick struct{ b Browser }

// NewBrowserClick returns the tool over a bridge.
func NewBrowserClick(b Browser) *BrowserClick { return &BrowserClick{b: b} }

func (t *BrowserClick) Name() string { return "browser_click" }

func (t *BrowserClick) Description() string {
	return "Click something on a page in the person's own browser: a link, button, checkbox or any element, by its ref from browser_read (e12), by CSS selector, or at x,y. Also double-click, right-click or hover. The tab is brought to the front if it is not. Reports whether the page changed and any tab the click opened."
}

// Impact is affect. A click can do anything the page lets it — submit, send,
// buy — and the tool cannot see which; the skill tells the planner to ask
// before the ones that cannot be undone.
func (t *BrowserClick) Impact(map[string]any) int { return toolapi.ImpactAffect }

func (t *BrowserClick) RequiresTarget() bool { return false }

func (t *BrowserClick) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			` + tabParam + `,
			` + refParam + `,
			` + selectorParam + `,
			"x": {"type": "number", "description": "With y: a point in CSS pixels from the top left of the visible page, when there is no element to name."},
			"y": {"type": "number"},
			"action": {"type": "string", "enum": ["click", "double", "right", "hover"], "default": "click"},
			"force": {"type": "boolean", "default": false, "description": "Click even though something covers the element."},
			` + browserParam + `
		},
		"additionalProperties": false
	}`)
}

type browserClickData struct {
	Action string `json:"action" desc:"click, double, right or hover"`
	Point  struct {
		X int `json:"x"`
		Y int `json:"y"`
	} `json:"point" desc:"where the pointer went, in CSS pixels"`
	Target    *elementSummary `json:"target,omitempty" desc:"the element acted on, when one was named"`
	OpenedTab *struct {
		ID  int    `json:"id"`
		URL string `json:"url"`
	} `json:"opened_tab,omitempty" desc:"a tab the click opened"`
	pageAfter
}

func (t *BrowserClick) OutputSchema() json.RawMessage {
	return toolapi.EnvelopeSchema(toolapi.PayloadSchemaOf(browserClickData{}))
}

func (t *BrowserClick) Execute(ctx context.Context, params map[string]any) (string, error) {
	return toolapi.StringResult(t.ExecuteTyped(ctx, params))
}

func (t *BrowserClick) ExecuteTyped(ctx context.Context, params map[string]any) (toolapi.ToolMessage, error) {
	args := map[string]any{}
	targetArgs(params, args)
	x, okX := toolapi.ParamNum(params, "x")
	y, okY := toolapi.ParamNum(params, "y")
	if args["ref"] == nil && args["selector"] == nil && !(okX && okY) {
		return toolapi.ToolFail("status", "say what to click: ref from browser_read, a selector, or x and y", nil), nil
	}
	if okX && okY {
		args["x"], args["y"] = x, y
	}
	if a, _ := params["action"].(string); a != "" {
		args["action"] = a
	}
	if f, _ := params["force"].(bool); f {
		args["force"] = true
	}
	var d browserClickData
	if fail := inputCall(ctx, t.b, params, "input.click", args, 30*time.Second, &d); fail != nil {
		return *fail, nil
	}
	verb := map[string]string{"click": "Clicked", "double": "Double-clicked", "right": "Right-clicked", "hover": "Hovered over"}[d.Action]
	what := d.Target.String()
	if what == "" {
		what = fmt.Sprintf("the point %d,%d", d.Point.X, d.Point.Y)
	}
	content := fmt.Sprintf("%s %s.%s", verb, what, d.sentence())
	if d.OpenedTab != nil {
		content += fmt.Sprintf(" It opened tab %d (%s).", d.OpenedTab.ID, d.OpenedTab.URL)
	}
	return toolapi.ToolOK("status", content, d), nil
}

// ─── browser_type ───────────────────────────────────────────────────────────

/*
 * BrowserType types text into a field.
 * desc: Clicks into the field named by ref or selector, or types where the
 *       focus already is. Can clear the field first and press Enter after.
 *       Reports what the field holds afterwards, so the text can be checked.
 */
type BrowserType struct{ b Browser }

// NewBrowserType returns the tool over a bridge.
func NewBrowserType(b Browser) *BrowserType { return &BrowserType{b: b} }

func (t *BrowserType) Name() string { return "browser_type" }

func (t *BrowserType) Description() string {
	return "Type text into a field on a page in the person's own browser, by its ref from browser_read or a CSS selector; without either, types where the cursor already is. clear empties the field first; submit presses Enter after. Reports what the field holds afterwards."
}

func (t *BrowserType) Impact(map[string]any) int { return toolapi.ImpactAffect }

func (t *BrowserType) RequiresTarget() bool { return false }

func (t *BrowserType) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			` + tabParam + `,
			` + refParam + `,
			` + selectorParam + `,
			"text": {"type": "string", "description": "What to type."},
			"clear": {"type": "boolean", "default": false, "description": "Empty the field first."},
			"submit": {"type": "boolean", "default": false, "description": "Press Enter after typing."},
			` + browserParam + `
		},
		"required": ["text"],
		"additionalProperties": false
	}`)
}

type browserTypeData struct {
	Typed     int             `json:"typed" desc:"characters typed"`
	Target    *elementSummary `json:"target,omitempty" desc:"the field typed into, when one was named"`
	Field     string          `json:"field,omitempty" desc:"what the focused field holds afterwards; a password field gives only its length"`
	Submitted bool            `json:"submitted,omitempty" desc:"Enter was pressed after typing"`
	pageAfter
}

func (t *BrowserType) OutputSchema() json.RawMessage {
	return toolapi.EnvelopeSchema(toolapi.PayloadSchemaOf(browserTypeData{}))
}

func (t *BrowserType) Execute(ctx context.Context, params map[string]any) (string, error) {
	return toolapi.StringResult(t.ExecuteTyped(ctx, params))
}

func (t *BrowserType) ExecuteTyped(ctx context.Context, params map[string]any) (toolapi.ToolMessage, error) {
	text, ok := params["text"].(string)
	if !ok {
		return toolapi.ToolFail("status", "give the text to type", nil), nil
	}
	args := map[string]any{"text": text}
	targetArgs(params, args)
	for _, k := range []string{"clear", "submit"} {
		if v, _ := params[k].(bool); v {
			args[k] = true
		}
	}
	var d browserTypeData
	if fail := inputCall(ctx, t.b, params, "input.type", args, 30*time.Second, &d); fail != nil {
		return *fail, nil
	}
	where := "where the cursor was"
	if d.Target != nil {
		where = "into " + d.Target.String()
	}
	content := fmt.Sprintf("Typed %d characters %s.", d.Typed, where)
	if d.Field != "" {
		content += fmt.Sprintf(" The field now holds %q.", d.Field)
	}
	if d.Submitted {
		content += " Pressed Enter."
	}
	return toolapi.ToolOK("status", content+d.sentence(), d), nil
}

// ─── browser_key ────────────────────────────────────────────────────────────

/*
 * BrowserKey presses keys: Enter, Escape, Tab, arrows, shortcuts.
 */
type BrowserKey struct{ b Browser }

// NewBrowserKey returns the tool over a bridge.
func NewBrowserKey(b Browser) *BrowserKey { return &BrowserKey{b: b} }

func (t *BrowserKey) Name() string { return "browser_key" }

func (t *BrowserKey) Description() string {
	return "Press keys in a tab of the person's own browser: Enter, Escape, Tab, ArrowDown, PageDown, Backspace, or a combination such as Control+A or Shift+Tab. Several space-separated keys are pressed in order. Goes to whatever has focus on the page."
}

func (t *BrowserKey) Impact(map[string]any) int { return toolapi.ImpactAffect }

func (t *BrowserKey) RequiresTarget() bool { return false }

func (t *BrowserKey) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			` + tabParam + `,
			"keys": {"type": "string", "description": "Key names separated by spaces, pressed in order: Enter, Tab, Escape, Backspace, Delete, ArrowUp, ArrowDown, ArrowLeft, ArrowRight, Home, End, PageUp, PageDown, F1–F12, a single character, or a combination like Control+A."},
			"repeat": {"type": "integer", "minimum": 1, "maximum": 50, "default": 1, "description": "Press the whole sequence this many times."},
			` + browserParam + `
		},
		"required": ["keys"],
		"additionalProperties": false
	}`)
}

type browserKeyData struct {
	Pressed string `json:"pressed" desc:"the keys pressed"`
	Repeat  int    `json:"repeat" desc:"times the sequence was pressed"`
	pageAfter
}

func (t *BrowserKey) OutputSchema() json.RawMessage {
	return toolapi.EnvelopeSchema(toolapi.PayloadSchemaOf(browserKeyData{}))
}

func (t *BrowserKey) Execute(ctx context.Context, params map[string]any) (string, error) {
	return toolapi.StringResult(t.ExecuteTyped(ctx, params))
}

func (t *BrowserKey) ExecuteTyped(ctx context.Context, params map[string]any) (toolapi.ToolMessage, error) {
	keys, _ := params["keys"].(string)
	if strings.TrimSpace(keys) == "" {
		return toolapi.ToolFail("status", "give the keys to press", nil), nil
	}
	args := map[string]any{"tab": tabArg(params), "keys": keys}
	if n, ok := toolapi.ParamInt(params, "repeat"); ok {
		args["repeat"] = n
	}
	var d browserKeyData
	if fail := inputCall(ctx, t.b, params, "input.key", args, 30*time.Second, &d); fail != nil {
		return *fail, nil
	}
	content := fmt.Sprintf("Pressed %s", d.Pressed)
	if d.Repeat > 1 {
		content += fmt.Sprintf(" ×%d", d.Repeat)
	}
	return toolapi.ToolOK("status", content+"."+d.sentence(), d), nil
}

// ─── browser_select ─────────────────────────────────────────────────────────

/*
 * BrowserSelect chooses an option in a dropdown (a <select>).
 * desc: A native dropdown's list is drawn by the browser, not the page, so it
 *       cannot be clicked; the option is set directly and the page told.
 */
type BrowserSelect struct{ b Browser }

// NewBrowserSelect returns the tool over a bridge.
func NewBrowserSelect(b Browser) *BrowserSelect { return &BrowserSelect{b: b} }

func (t *BrowserSelect) Name() string { return "browser_select" }

func (t *BrowserSelect) Description() string {
	return "Choose an option in a dropdown (a combobox from browser_read) in the person's own browser, by the option's visible text or value. If the dropdown is not a real <select>, click it and then the option instead."
}

func (t *BrowserSelect) Impact(map[string]any) int { return toolapi.ImpactAffect }

func (t *BrowserSelect) RequiresTarget() bool { return false }

func (t *BrowserSelect) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			` + tabParam + `,
			` + refParam + `,
			` + selectorParam + `,
			"option": {"type": "string", "description": "The option's visible text or its value."},
			` + browserParam + `
		},
		"required": ["option"],
		"additionalProperties": false
	}`)
}

type browserSelectData struct {
	Selected string `json:"selected" desc:"the option's visible text"`
	Value    string `json:"value" desc:"the option's value"`
}

func (t *BrowserSelect) OutputSchema() json.RawMessage {
	return toolapi.EnvelopeSchema(toolapi.PayloadSchemaOf(browserSelectData{}))
}

func (t *BrowserSelect) Execute(ctx context.Context, params map[string]any) (string, error) {
	return toolapi.StringResult(t.ExecuteTyped(ctx, params))
}

func (t *BrowserSelect) ExecuteTyped(ctx context.Context, params map[string]any) (toolapi.ToolMessage, error) {
	option, _ := params["option"].(string)
	args := map[string]any{"option": option}
	targetArgs(params, args)
	if args["ref"] == nil && args["selector"] == nil {
		return toolapi.ToolFail("status", "say which dropdown, by its ref from browser_read", nil), nil
	}
	var d browserSelectData
	if fail := inputCall(ctx, t.b, params, "input.select", args, 20*time.Second, &d); fail != nil {
		return *fail, nil
	}
	return toolapi.ToolOK("status", fmt.Sprintf("Selected %q.", d.Selected), d), nil
}

// ─── browser_scroll ─────────────────────────────────────────────────────────

/*
 * BrowserScroll scrolls a page, or brings an element into view.
 */
type BrowserScroll struct{ b Browser }

// NewBrowserScroll returns the tool over a bridge.
func NewBrowserScroll(b Browser) *BrowserScroll { return &BrowserScroll{b: b} }

func (t *BrowserScroll) Name() string { return "browser_scroll" }

func (t *BrowserScroll) Description() string {
	return "Scroll a page in the person's own browser up, down, to the top or bottom, or bring an element (by ref) into view. Use it to reach content that loads as you scroll; read the page again afterwards."
}

// Impact is observe: scrolling changes where the page is looked at, not what
// it holds.
func (t *BrowserScroll) Impact(map[string]any) int { return toolapi.ImpactObserve }

func (t *BrowserScroll) RequiresTarget() bool { return false }

func (t *BrowserScroll) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			` + tabParam + `,
			"direction": {"type": "string", "enum": ["down", "up", "top", "bottom"], "default": "down"},
			"amount": {"type": "integer", "minimum": 1, "description": "Pixels to scroll up or down; default most of a screen."},
			` + refParam + `,
			` + browserParam + `
		},
		"additionalProperties": false
	}`)
}

type browserScrollData struct {
	Moved      bool   `json:"moved" desc:"the page moved"`
	Method     string `json:"method,omitempty" desc:"wheel when the window did not scroll and a mouse wheel was used on the page instead"`
	ScrollY    int    `json:"scroll_y" desc:"how far down the page is now, in CSS pixels"`
	PageHeight int    `json:"page_height" desc:"the page's full height"`
	AtBottom   bool   `json:"at_bottom,omitempty" desc:"the bottom of the page is on screen"`
}

func (t *BrowserScroll) OutputSchema() json.RawMessage {
	return toolapi.EnvelopeSchema(toolapi.PayloadSchemaOf(browserScrollData{}))
}

func (t *BrowserScroll) Execute(ctx context.Context, params map[string]any) (string, error) {
	return toolapi.StringResult(t.ExecuteTyped(ctx, params))
}

func (t *BrowserScroll) ExecuteTyped(ctx context.Context, params map[string]any) (toolapi.ToolMessage, error) {
	args := map[string]any{"tab": tabArg(params)}
	if d, _ := params["direction"].(string); d != "" {
		args["direction"] = d
	}
	if n, ok := toolapi.ParamInt(params, "amount"); ok {
		args["amount"] = n
	}
	if r, _ := params["ref"].(string); r != "" {
		args["ref"] = r
	}
	var d browserScrollData
	if fail := inputCall(ctx, t.b, params, "page.scroll", args, 20*time.Second, &d); fail != nil {
		return *fail, nil
	}
	content := fmt.Sprintf("Scrolled to %d of %d pixels.", d.ScrollY, d.PageHeight)
	switch {
	case d.Method == "wheel":
		content = "The window did not scroll, so the mouse wheel was used on the middle of the page; read it again to see what moved."
	case !d.Moved:
		content = fmt.Sprintf("The page did not move; it is at %d of %d pixels.", d.ScrollY, d.PageHeight)
	}
	if d.AtBottom {
		content += " The bottom of the page is on screen."
	}
	return toolapi.ToolOK("status", content, d), nil
}

// ─── browser_wait ───────────────────────────────────────────────────────────

/*
 * BrowserWait waits for text or an element to appear on, or leave, a page.
 * desc: For pages that change without loading a new one: a search that fills
 *       in results, a spinner that clears, a message that appears after Save.
 */
type BrowserWait struct{ b Browser }

// NewBrowserWait returns the tool over a bridge.
func NewBrowserWait(b Browser) *BrowserWait { return &BrowserWait{b: b} }

func (t *BrowserWait) Name() string { return "browser_wait" }

func (t *BrowserWait) Description() string {
	return "Wait until text or an element (CSS selector) appears on a page in the person's own browser — or, with gone, until it disappears. For pages that update without loading a new page: search results, a spinner, a confirmation message."
}

func (t *BrowserWait) Impact(map[string]any) int { return toolapi.ImpactObserve }

func (t *BrowserWait) RequiresTarget() bool { return false }

func (t *BrowserWait) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			` + tabParam + `,
			"text": {"type": "string", "description": "Text to wait for, case-insensitive."},
			` + selectorParam + `,
			"gone": {"type": "boolean", "default": false, "description": "Wait for it to disappear instead."},
			"timeout_ms": {"type": "integer", "minimum": 250, "maximum": 60000, "default": 10000},
			` + browserParam + `
		},
		"additionalProperties": false
	}`)
}

type browserWaitData struct {
	Found    bool `json:"found" desc:"what was waited for happened"`
	WaitedMS int  `json:"waited_ms" desc:"how long it took, or how long was waited"`
}

func (t *BrowserWait) OutputSchema() json.RawMessage {
	return toolapi.EnvelopeSchema(toolapi.PayloadSchemaOf(browserWaitData{}))
}

func (t *BrowserWait) Execute(ctx context.Context, params map[string]any) (string, error) {
	return toolapi.StringResult(t.ExecuteTyped(ctx, params))
}

func (t *BrowserWait) ExecuteTyped(ctx context.Context, params map[string]any) (toolapi.ToolMessage, error) {
	text, _ := params["text"].(string)
	selector, _ := params["selector"].(string)
	if text == "" && selector == "" {
		return toolapi.ToolFail("status", "say what to wait for: text or a selector", nil), nil
	}
	gone, _ := params["gone"].(bool)
	timeout := 10000
	if v, ok := toolapi.ParamInt(params, "timeout_ms"); ok {
		timeout = v
	}
	args := map[string]any{"tab": tabArg(params), "text": text, "selector": selector, "gone": gone, "timeout_ms": timeout}
	browser, _ := params["browser"].(string)
	raw, err := t.b.Do(ctx, browserbridge.Call{
		Username: agent.UsernameFrom(ctx), Browser: browser,
		Op: "page.wait", Args: args, Idempotent: true,
		Timeout: time.Duration(timeout)*time.Millisecond + 10*time.Second,
	})
	if err != nil {
		return browserFailure("status", err), nil
	}
	var d browserWaitData
	if err := json.Unmarshal(raw, &d); err != nil {
		return toolapi.ToolFail("status", "the browser's answer could not be read: "+err.Error(), nil), nil
	}
	what := text
	if selector != "" {
		what = selector
	}
	state := "appeared"
	if gone {
		state = "disappeared"
	}
	if !d.Found {
		return toolapi.ToolEmptyWith("status", fmt.Sprintf("%q had not %s after %dms", what, state, d.WaitedMS), d), nil
	}
	return toolapi.ToolOK("status", fmt.Sprintf("%q %s after %dms.", what, state, d.WaitedMS), d), nil
}

var (
	_ toolapi.Tool = (*BrowserClick)(nil)
	_ toolapi.Tool = (*BrowserType)(nil)
	_ toolapi.Tool = (*BrowserKey)(nil)
	_ toolapi.Tool = (*BrowserSelect)(nil)
	_ toolapi.Tool = (*BrowserScroll)(nil)
	_ toolapi.Tool = (*BrowserWait)(nil)
)
