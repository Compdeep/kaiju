package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A line of wttr.in's v2 forecast as it arrives: colour codes around the text.
const colouredLine = "\x1b[38;5;226m  \\   /  \x1b[0m Sunny \x1b[38;5;214m+18\x1b[0m(\x1b[38;5;220m17\x1b[0m) °C ┌─────┐\x1b[2mLocation: \x1b[0m東京都"

func TestPlainTextRemovesTerminalCodes(t *testing.T) {
	got := plainText(colouredLine)
	if strings.Contains(got, "\x1b") {
		t.Fatalf("escape codes left in %q", got)
	}
	want := "  \\   /   Sunny +18(17) °C ┌─────┐Location: 東京都"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	// A title and a link, both ended by BEL or by ESC \.
	if got := plainText("\x1b]0;title\x07a\x1b]8;;https://x.test\x1b\\link\x1b]8;;\x1b\\b"); got != "alinkb" {
		t.Errorf("OSC sequences: got %q", got)
	}
	if got := plainText("no codes here"); got != "no codes here" {
		t.Errorf("plain text changed: %q", got)
	}
}

// A command that colours its output reaches stdout, stderr and the saved file
// without the codes.
func TestBashOutputHasNoTerminalCodes(t *testing.T) {
	ws := t.TempDir()
	src := filepath.Join(ws, "coloured.txt")
	if err := os.WriteFile(src, []byte(colouredLine+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := NewBash("sh", ws)
	msg, err := b.ExecuteTyped(context.Background(), map[string]any{"command": "cat " + src + "; cat " + src + " >&2"})
	if err != nil {
		t.Fatal(err)
	}
	p := bashPayload(t, msg)
	stdout, _ := p["stdout"].(string)
	stderr, _ := p["stderr"].(string)
	rel, _ := p["output_path"].(string)
	for name, text := range map[string]string{"stdout": stdout, "stderr": stderr, "content": msg.Content} {
		if strings.Contains(text, "\x1b") {
			t.Errorf("%s still carries escape codes: %q", name, text)
		}
		if !strings.Contains(text, "Sunny +18(17) °C") {
			t.Errorf("%s lost the text: %q", name, text)
		}
	}
	kept, err := os.ReadFile(filepath.Join(ws, rel))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(kept), "\x1b") {
		t.Errorf("the file at output_path still carries escape codes")
	}
}
