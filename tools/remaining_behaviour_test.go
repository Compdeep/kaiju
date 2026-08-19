package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// The six tools nothing executed.
//
// Every tool in this package and the application's was run by hand and its result
// compared against its declaration, and these six were the ones no test ever called:
// archive, clipboard, env_list, file_list, git and process_list. What follows is
// their behaviour, not their declaration — the contract tests already cover what they
// say about themselves.

// ── archive ─────────────────────────────────────────────────────────

// What goes into an archive comes out of it, and each step says how many.
func TestArchiveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"one.txt", "two.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	archive := filepath.Join(dir, "bundle.zip")
	tool := NewArchive()

	msg, err := tool.ExecuteTyped(context.Background(), map[string]any{
		"action": "create", "archive_path": archive, "format": "zip",
		"files": []any{filepath.Join(dir, "one.txt"), filepath.Join(dir, "two.txt")},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	var created archiveData
	if err := json.Unmarshal(msg.Data, &created); err != nil {
		t.Fatalf("create payload: %v", err)
	}
	if created.Action != "create" || created.Count != 2 {
		t.Errorf("create = %+v, want two files", created)
	}

	msg, err = tool.ExecuteTyped(context.Background(), map[string]any{
		"action": "list", "archive_path": archive, "format": "zip"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var listed archiveData
	if err := json.Unmarshal(msg.Data, &listed); err != nil {
		t.Fatalf("list payload: %v", err)
	}
	if listed.Count != 2 || len(listed.Entries) != 2 {
		t.Errorf("list = %+v, want the two names inside", listed)
	}

	dest := filepath.Join(dir, "out")
	msg, err = tool.ExecuteTyped(context.Background(), map[string]any{
		"action": "extract", "archive_path": archive, "dest": dest, "format": "zip"})
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	var extracted archiveData
	if err := json.Unmarshal(msg.Data, &extracted); err != nil {
		t.Fatalf("extract payload: %v", err)
	}
	if extracted.Count != 2 || extracted.Dest != dest {
		t.Errorf("extract = %+v, want two files into %s", extracted, dest)
	}
	// The files are really there, which is the point of the count.
	found := 0
	filepath.Walk(dest, func(_ string, info os.FileInfo, _ error) error {
		if info != nil && !info.IsDir() {
			found++
		}
		return nil
	})
	if found != 2 {
		t.Errorf("%d files under %s, want 2", found, dest)
	}
}

// An archive that is not there is named in the failure.
func TestArchiveOnSomethingThatIsNotAnArchive(t *testing.T) {
	_, err := NewArchive().ExecuteTyped(context.Background(), map[string]any{
		"action": "list", "archive_path": filepath.Join(t.TempDir(), "absent.zip"), "format": "zip"})
	if err == nil {
		t.Fatal("listing an archive that does not exist was accepted")
	}
	if !strings.Contains(err.Error(), "absent.zip") {
		t.Errorf("the error does not name the archive: %v", err)
	}
}

// ── clipboard ───────────────────────────────────────────────────────

// A machine with no clipboard program says so, and does not end the run.
//
// Both directions raised a Go error, which ends the step — over a machine that
// simply has no graphical session, which is every server this runs on.
func TestClipboardWithoutAClipboardProgram(t *testing.T) {
	if _, err := exec.LookPath("xclip"); err == nil {
		t.Skip("this machine has a clipboard program, so the absent case cannot be tested here")
	}
	if _, err := exec.LookPath("xsel"); err == nil {
		t.Skip("this machine has a clipboard program")
	}

	for _, params := range []map[string]any{
		{"action": "read"},
		{"action": "write", "content": "something"},
	} {
		msg, err := NewClipboard().ExecuteTyped(context.Background(), params)
		if err != nil {
			t.Errorf("%v returned a Go error rather than a result: %v", params, err)
			continue
		}
		if msg.Status != toolapi.StatusError {
			t.Errorf("%v = %q, want error", params, msg.Status)
		}
		if !strings.Contains(msg.Detail, "no clipboard") {
			t.Errorf("the detail does not say what is missing: %q", msg.Detail)
		}
	}
}

// An action this tool does not have is refused by name.
func TestClipboardRefusesAnUnknownAction(t *testing.T) {
	_, err := NewClipboard().ExecuteTyped(context.Background(), map[string]any{"action": "paste"})
	if err == nil {
		t.Fatal("an unknown action was accepted")
	}
	if !strings.Contains(err.Error(), "read") || !strings.Contains(err.Error(), "write") {
		t.Errorf("the refusal does not say which actions exist: %v", err)
	}
}

// ── env_list ────────────────────────────────────────────────────────

// A sensitive-looking name is masked, and the masking is counted.
func TestEnvListMasksWhatLooksSensitive(t *testing.T) {
	t.Setenv("SHAPES_PROBE_TOKEN", "the-secret-value")
	t.Setenv("SHAPES_PROBE_PLAIN", "an-ordinary-value")

	msg, err := NewEnvList().ExecuteTyped(context.Background(),
		map[string]any{"filter": "SHAPES_PROBE"})
	if err != nil {
		t.Fatalf("%v", err)
	}
	if msg.Status != toolapi.StatusOK {
		t.Fatalf("status = %q (%q)", msg.Status, msg.Detail)
	}
	if strings.Contains(msg.Content, "the-secret-value") {
		t.Error("a value under a name containing TOKEN was shown in the text")
	}
	if !strings.Contains(msg.Content, "an-ordinary-value") {
		t.Error("an ordinary value was withheld")
	}

	var payload envListData
	if err := json.Unmarshal(msg.Data, &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if payload.Masked != 1 {
		t.Errorf("masked = %d, want the one sensitive name", payload.Masked)
	}
	if payload.Variables["SHAPES_PROBE_TOKEN"] == "the-secret-value" {
		t.Error("the payload carries the value the text masked")
	}
	if payload.Count != 2 {
		t.Errorf("count = %d, want both variables", payload.Count)
	}
}

// Asking with show_sensitive gets the value, since the caller asked for it.
func TestEnvListShowsSensitiveWhenAsked(t *testing.T) {
	t.Setenv("SHAPES_PROBE_TOKEN", "the-secret-value")
	msg, err := NewEnvList().ExecuteTyped(context.Background(),
		map[string]any{"filter": "SHAPES_PROBE_TOKEN", "show_sensitive": true})
	if err != nil {
		t.Fatalf("%v", err)
	}
	if !strings.Contains(msg.Content, "the-secret-value") {
		t.Errorf("show_sensitive did not show it: %q", msg.Content)
	}
}

// A filter matching nothing is an empty result naming the filter.
func TestEnvListWithNoMatch(t *testing.T) {
	msg, err := NewEnvList().ExecuteTyped(context.Background(),
		map[string]any{"filter": "no-variable-is-called-this"})
	if err != nil {
		t.Fatalf("%v", err)
	}
	if msg.Status != toolapi.StatusEmpty {
		t.Fatalf("status = %q, want empty", msg.Status)
	}
}

// ── file_list ───────────────────────────────────────────────────────

// A directory's contents come back typed and sized.
func TestFileListReportsTypesAndSizes(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("12345"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}

	msg, err := NewFileList("").ExecuteTyped(context.Background(), map[string]any{"path": dir})
	if err != nil {
		t.Fatalf("%v", err)
	}
	var payload struct {
		Entries []struct {
			Name string `json:"name"`
			Type string `json:"type"`
			Size int64  `json:"size"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(msg.Data, &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if len(payload.Entries) != 2 {
		t.Fatalf("%d entries, want the file and the directory", len(payload.Entries))
	}
	byName := map[string]struct {
		Type string
		Size int64
	}{}
	for _, e := range payload.Entries {
		byName[e.Name] = struct {
			Type string
			Size int64
		}{e.Type, e.Size}
	}
	if byName["a.txt"].Type != "file" || byName["a.txt"].Size != 5 {
		t.Errorf("a.txt = %+v, want a file of five bytes", byName["a.txt"])
	}
	if byName["sub"].Type != "dir" {
		t.Errorf("sub = %+v, want a directory", byName["sub"])
	}
}

// A directory that is not there names itself in the failure.
func TestFileListOnAMissingDirectory(t *testing.T) {
	_, err := NewFileList("").ExecuteTyped(context.Background(),
		map[string]any{"path": filepath.Join(t.TempDir(), "absent")})
	if err == nil {
		t.Fatal("listing a directory that does not exist was accepted")
	}
	if !strings.Contains(err.Error(), "absent") {
		t.Errorf("the error does not name the path: %v", err)
	}
}

// ── git ─────────────────────────────────────────────────────────────

// Status in a repository reports the repository, and counts its own output.
func TestGitStatusInARepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git on this machine")
	}
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init"}, {"config", "user.email", "t@example.com"}, {"config", "user.name", "t"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git %v: %v: %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	msg, err := NewGit().ExecuteTyped(context.Background(),
		map[string]any{"action": "status", "path": dir})
	if err != nil {
		t.Fatalf("%v", err)
	}
	if msg.Status != toolapi.StatusOK {
		t.Fatalf("status = %q (%q)", msg.Status, msg.Detail)
	}
	if !strings.Contains(msg.Content, "new.txt") {
		t.Errorf("the untracked file is not in the report: %q", msg.Content)
	}
	var payload gitData
	if err := json.Unmarshal(msg.Data, &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if payload.Action != "status" {
		t.Errorf("action = %q", payload.Action)
	}
	if payload.Lines != len(strings.Split(strings.TrimRight(msg.Content, "\n"), "\n")) {
		t.Errorf("lines = %d, and the report has %d",
			payload.Lines, len(strings.Split(strings.TrimRight(msg.Content, "\n"), "\n")))
	}
}

// Outside a repository, git's own complaint survives rather than the step ending.
func TestGitOutsideARepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git on this machine")
	}
	msg, err := NewGit().ExecuteTyped(context.Background(),
		map[string]any{"action": "status", "path": t.TempDir()})
	if err != nil {
		t.Fatalf("returned a Go error rather than a result: %v", err)
	}
	if msg.Status == toolapi.StatusOK {
		t.Fatalf("a directory that is not a repository reported ok: %q", msg.Content)
	}
	if !strings.Contains(strings.ToLower(msg.Detail+string(msg.Data)), "repository") {
		t.Errorf("git's own reason did not survive: detail=%q data=%s", msg.Detail, msg.Data)
	}
}

// ── process_list ────────────────────────────────────────────────────

// The listing carries this test's own process, as a row with a pid.
func TestProcessListFindsThisProcess(t *testing.T) {
	msg, err := NewProcessList().ExecuteTyped(context.Background(), map[string]any{"limit": 200})
	if err != nil {
		t.Fatalf("%v", err)
	}
	if msg.Status != toolapi.StatusOK {
		t.Fatalf("status = %q (%q)", msg.Status, msg.Detail)
	}
	var payload processListData
	if err := json.Unmarshal(msg.Data, &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if payload.Count == 0 {
		t.Fatal("no processes at all, on a machine running this test")
	}
	self := os.Getpid()
	for _, p := range payload.Processes {
		if p.PID == self {
			if p.Command == "" {
				t.Error("this process is listed with no command line")
			}
			return
		}
	}
	// The rows are parsed per platform, so an unparsed platform is not a failure —
	// but then there should be no rows at all rather than rows without this one.
	if len(payload.Processes) > 0 {
		t.Errorf("%d rows parsed and none is this process (pid %d)", len(payload.Processes), self)
	}
}

// A filter that matches nothing says so rather than returning the whole table.
func TestProcessListFilterExcludes(t *testing.T) {
	msg, err := NewProcessList().ExecuteTyped(context.Background(),
		map[string]any{"filter": "no-process-is-called-this-8f7d6s", "limit": 50})
	if err != nil {
		t.Fatalf("%v", err)
	}
	var payload processListData
	if err := json.Unmarshal(msg.Data, &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	// The filter is a substring test over the whole command line, so a match is
	// possible only if something really carries that string.
	for _, p := range payload.Processes {
		if !strings.Contains(strings.ToLower(p.Command), "no-process-is-called-this-8f7d6s") {
			t.Errorf("a row that does not match the filter was kept: %s", p.Command)
			break
		}
	}
	if payload.Filter != "no-process-is-called-this-8f7d6s" {
		t.Errorf("the payload does not say what was filtered on: %q", payload.Filter)
	}
}
