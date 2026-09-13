package uploads

import (
	"encoding/json"
	"github.com/Compdeep/kaiju/agent"
	"os"
	"path/filepath"
	"testing"
)

// A .meta.json whose file is gone is not an attachment.
//
// The cleanup for a write that failed part way removes the file alone, so the
// sidecar outlives the bytes it describes. This listing is what the chip strip
// is restored from, so an orphaned sidecar put the attachment back on every
// session load and its path into every query after it — and the only stage that
// found out was file_read, once per run.
func TestListSkipsMetadataWithNoFile(t *testing.T) {
	ws := t.TempDir()
	const sid = "s1"
	dir := filepath.Join(ws, "uploads", sid)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	write := func(name string, withFile bool) {
		meta := Meta{Filename: name, Type: "text/html", Size: 10, Lines: 2}
		raw, err := json.Marshal(meta)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name+".meta.json"), raw, 0o644); err != nil {
			t.Fatal(err)
		}
		if withFile {
			if err := os.WriteFile(filepath.Join(dir, name), []byte("<html></html>"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	write("present.html", true)
	write("orphaned.html", false) // the sidecar survived, the upload did not

	ag, err := agent.New(agent.Config{
		PathConfig: agent.PathConfig{Workspace: ws, MetadataDir: t.TempDir(), DataDir: t.TempDir()},
	})
	if err != nil {
		t.Fatalf("agent: %v", err)
	}
	p := New(ag, nil)
	got, err := p.List(sid)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		names := make([]string, len(got))
		for i, r := range got {
			names[i] = r.Filename
		}
		t.Fatalf("List returned %d attachments %v, want only the one whose file exists", len(got), names)
	}
	if got[0].Filename != "present.html" {
		t.Errorf("listed %q, want present.html", got[0].Filename)
	}
}
