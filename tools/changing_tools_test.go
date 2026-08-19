package tools

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// What archive and git leave out, and whether they say so.
//
// Both had a round-trip test. Neither had a test for the case where the tool cannot
// do all of what it was asked and reports a count as though it did.

// ── archive: what an extraction did not do ───────────────────────────────────

// writeZip builds a zip holding the named entries, and returns its path.
func writeZipOfEntries(t *testing.T, dir string, entries map[string]string) string {
	t.Helper()
	path := filepath.Join(dir, "in.zip")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create zip: %v", err)
	}
	zw := zip.NewWriter(f)
	for name, body := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("add %s: %v", name, err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("finish zip: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return path
}

// extractPayload runs an extraction and returns the message with its payload read.
type extractResult struct {
	Count    int  `json:"count"`
	Skipped  int  `json:"skipped"`
	Refused  int  `json:"refused"`
	Complete bool `json:"complete"`
}

func extract(t *testing.T, archivePath, dest string) (toolapi.ToolMessage, extractResult) {
	t.Helper()
	msg, err := NewArchive().ExecuteTyped(context.Background(), map[string]any{
		"action": "extract", "archive_path": archivePath, "dest": dest})
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	var data extractResult
	if len(msg.Data) > 0 {
		if err := json.Unmarshal(msg.Data, &data); err != nil {
			t.Fatalf("payload: %v", err)
		}
	}
	return msg, data
}

// An entry that points outside the destination is refused, counted, and named.
//
// It was passed over in silence and the remaining entries reported as a plain success,
// so an archive built to write outside the directory it was given looked the same as an
// ordinary one — and that is a fact about the archive worth reporting.
func TestArchiveExtractionReportsAnEntryPointingOutside(t *testing.T) {
	dir := t.TempDir()
	archivePath := writeZipOfEntries(t, dir, map[string]string{
		"ordinary.txt":    "inside",
		"../escaped.txt":  "outside",
		"sub/another.txt": "inside too",
	})
	dest := filepath.Join(dir, "out")

	msg, data := extract(t, archivePath, dest)
	if msg.Status != toolapi.StatusOK {
		t.Fatalf("extract = %q (%q)", msg.Status, msg.Detail)
	}
	if data.Refused != 1 {
		t.Errorf("refused = %d, want 1 — the entry pointing outside was passed over silently", data.Refused)
	}
	if data.Count != 2 {
		t.Errorf("count = %d, want 2", data.Count)
	}
	if data.Complete {
		t.Errorf("complete is true, but an entry the archive named was not written")
	}
	if !strings.Contains(msg.Content, "refused") {
		t.Errorf("the sentence does not mention the refusal: %q", msg.Content)
	}
	// And nothing was written outside.
	if _, err := os.Stat(filepath.Join(dir, "escaped.txt")); err == nil {
		t.Errorf("the entry was written outside the destination")
	}
}

// A sibling directory sharing the destination's leading characters is outside it.
//
// The test was a plain prefix comparison, so with a destination of .../out an entry
// resolving to .../outsider passed it and was written there — outside the directory
// the caller named, with nothing reported.
func TestArchiveExtractionRefusesASiblingOfTheDestination(t *testing.T) {
	dir := t.TempDir()
	archivePath := writeZipOfEntries(t, dir, map[string]string{
		"ordinary.txt":        "inside",
		"../outsider/got.txt": "outside",
	})
	dest := filepath.Join(dir, "out")

	_, data := extract(t, archivePath, dest)
	if data.Refused != 1 {
		t.Errorf("refused = %d, want 1 — a sibling directory passed the destination test", data.Refused)
	}
	if _, err := os.Stat(filepath.Join(dir, "outsider", "got.txt")); err == nil {
		t.Errorf("the entry was written to a sibling of the destination")
	}
}

// An entry that cannot be written is counted as not written.
//
// Every per-entry failure was passed over and the count returned as a success, so an
// extraction that wrote two of three files reported two — which reads exactly like an
// archive that held two.
func TestArchiveExtractionCountsWhatItCouldNotWrite(t *testing.T) {
	dir := t.TempDir()
	archivePath := writeZipOfEntries(t, dir, map[string]string{
		"fine.txt":     "written",
		"blocked":      "cannot be written",
		"alsofine.txt": "written too",
	})
	dest := filepath.Join(dir, "out")
	// A directory where the entry wants to be a file: creating the file fails.
	if err := os.MkdirAll(filepath.Join(dest, "blocked"), 0o755); err != nil {
		t.Fatalf("prepare: %v", err)
	}

	msg, data := extract(t, archivePath, dest)
	if data.Skipped != 1 {
		t.Errorf("skipped = %d, want 1 — the entry that could not be written was passed over", data.Skipped)
	}
	if data.Count != 2 {
		t.Errorf("count = %d, want 2", data.Count)
	}
	if data.Complete {
		t.Errorf("complete is true, but an entry was not written")
	}
	if !strings.Contains(msg.Content, "could not be written") {
		t.Errorf("the sentence does not say an entry was missed: %q", msg.Content)
	}
}

// An extraction where nothing arrived and entries were there to arrive is a failure.
func TestArchiveExtractionThatWroteNothingIsAFailure(t *testing.T) {
	dir := t.TempDir()
	archivePath := writeZipOfEntries(t, dir, map[string]string{"../only-escapes.txt": "outside"})
	dest := filepath.Join(dir, "out")

	msg, data := extract(t, archivePath, dest)
	if msg.Status != toolapi.StatusError {
		t.Errorf("an extraction that wrote nothing = %q, want error — zero files reads as an "+
			"empty archive rather than a failed extraction", msg.Status)
	}
	if data.Count != 0 || data.Refused != 1 {
		t.Errorf("count=%d refused=%d, want 0 and 1", data.Count, data.Refused)
	}
}

// A whole extraction says so.
func TestArchiveExtractionWithNothingMissedIsComplete(t *testing.T) {
	dir := t.TempDir()
	archivePath := writeZipOfEntries(t, dir, map[string]string{"a.txt": "one", "b/c.txt": "two"})
	dest := filepath.Join(dir, "out")

	msg, data := extract(t, archivePath, dest)
	if msg.Status != toolapi.StatusOK {
		t.Fatalf("extract = %q (%q)", msg.Status, msg.Detail)
	}
	if !data.Complete || data.Skipped != 0 || data.Refused != 0 {
		t.Errorf("complete=%v skipped=%d refused=%d, want true, 0, 0", data.Complete, data.Skipped, data.Refused)
	}
	if data.Count != 2 {
		t.Errorf("count = %d, want 2", data.Count)
	}
}

// An archive with nothing in it is an empty result, not a listing of length zero.
func TestArchiveListOfAnEmptyArchiveIsEmpty(t *testing.T) {
	dir := t.TempDir()
	archivePath := writeZipOfEntries(t, dir, nil)

	msg, err := NewArchive().ExecuteTyped(context.Background(), map[string]any{
		"action": "list", "archive_path": archivePath})
	if err != nil {
		t.Fatalf("%v", err)
	}
	if msg.Status != toolapi.StatusEmpty {
		t.Errorf("listing an empty archive = %q, want empty — it read \"0 entries:\" as a success", msg.Status)
	}
}

// Creating an archive still works, and the file it writes can be read back.
//
// The three closes that finish a zip and a tar.gz are checked now rather than
// deferred and discarded, which is the kind of change that breaks the ordinary path if
// it is done wrong.
func TestArchiveCreateStillProducesAReadableArchive(t *testing.T) {
	for _, format := range []string{"zip", "tar.gz"} {
		dir := t.TempDir()
		src := filepath.Join(dir, "src.txt")
		if err := os.WriteFile(src, []byte("contents"), 0o644); err != nil {
			t.Fatalf("%v", err)
		}
		archivePath := filepath.Join(dir, "out."+format)

		msg, err := NewArchive().ExecuteTyped(context.Background(), map[string]any{
			"action": "create", "archive_path": archivePath,
			"files": []any{src}, "format": format})
		if err != nil {
			t.Fatalf("%s create: %v", format, err)
		}
		if msg.Status != toolapi.StatusOK {
			t.Fatalf("%s create = %q (%q)", format, msg.Status, msg.Detail)
		}

		listed, err := NewArchive().ExecuteTyped(context.Background(), map[string]any{
			"action": "list", "archive_path": archivePath, "format": format})
		if err != nil {
			t.Fatalf("%s list: %v", format, err)
		}
		if listed.Status != toolapi.StatusOK {
			t.Errorf("%s: the archive just created does not list = %q (%q)", format, listed.Status, listed.Detail)
		}
		if !strings.Contains(listed.Content, "src.txt") {
			t.Errorf("%s: the listing does not name the file put in it: %q", format, listed.Content)
		}
	}
}

// ── git: output that was cut ─────────────────────────────────────────────────

// Output longer than this tool returns is marked as cut in the payload.
//
// The notice went into the text and nothing into the payload, so a step reading the
// payload saw a line count with no sign that the count was of a fragment.
func TestGitSaysWhenItsOutputWasCut(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git on this machine")
	}
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init").CombinedOutput(); err != nil {
		t.Skipf("git init: %v: %s", err, out)
	}
	// Enough untracked files that the short status runs past the limit.
	for i := 0; i < 400; i++ {
		name := fmt.Sprintf("a-file-with-a-reasonably-long-name-%03d.txt", i)
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("%v", err)
		}
	}

	msg, err := NewGit().ExecuteTyped(context.Background(), map[string]any{
		"action": "status", "path": dir})
	if err != nil {
		t.Fatalf("%v", err)
	}
	if msg.Status != toolapi.StatusOK {
		t.Fatalf("status = %q (%q)", msg.Status, msg.Detail)
	}
	var data struct {
		Truncated bool `json:"truncated"`
		Lines     int  `json:"lines"`
	}
	if err := json.Unmarshal(msg.Data, &data); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if !data.Truncated {
		t.Errorf("output of %d bytes came back with truncated=false, so a step reading the "+
			"payload cannot tell it has a fragment", len(msg.Content))
	}
	if data.Lines == 0 {
		t.Errorf("no line count on truncated output")
	}
}

// Output that fits is not marked as cut.
func TestGitDoesNotClaimShortOutputWasCut(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git on this machine")
	}
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init").CombinedOutput(); err != nil {
		t.Skipf("git init: %v: %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(dir, "one.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("%v", err)
	}

	msg, err := NewGit().ExecuteTyped(context.Background(), map[string]any{
		"action": "status", "path": dir})
	if err != nil {
		t.Fatalf("%v", err)
	}
	var data struct {
		Truncated bool `json:"truncated"`
	}
	json.Unmarshal(msg.Data, &data)
	if data.Truncated {
		t.Errorf("short output is marked as cut")
	}
}
