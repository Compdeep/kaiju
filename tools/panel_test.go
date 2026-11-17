package tools

import (
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// A file named by path reaches the panel as a path, with its name as the tab
// title, so the canvas can draw it by its type without the agent pasting it.
func TestPanelPush_APathIsShownWithoutContent(t *testing.T) {
	p := NewPanelPush()
	params := map[string]any{"plugin": "canvas", "path": "project/h4k/deck.pptx"}

	msg, err := p.ExecuteTyped(nil, params)
	if err != nil {
		t.Fatalf("ExecuteTyped: %v", err)
	}
	if msg.Status != toolapi.StatusOK {
		t.Fatalf("status = %q, want ok", msg.Status)
	}

	hint := p.DisplayHint(params, "")
	if hint == nil {
		t.Fatal("no display hint for a path")
	}
	if hint.Path != "project/h4k/deck.pptx" || hint.Content != "" || hint.Title != "deck.pptx" {
		t.Fatalf("hint = %+v", hint)
	}
}

func TestPanelPush_NeitherContentNorPathIsRefused(t *testing.T) {
	p := NewPanelPush()
	params := map[string]any{"plugin": "canvas"}
	if _, err := p.ExecuteTyped(nil, params); err == nil {
		t.Fatal("want an error with neither content nor path")
	}
	if p.DisplayHint(params, "") != nil {
		t.Fatal("want no display hint with neither content nor path")
	}
}
