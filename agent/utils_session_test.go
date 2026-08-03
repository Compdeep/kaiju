package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── worklogPath ──────────────────────────────────────────────────────────────

func TestWorklogPath_LegacyWhenEmpty(t *testing.T) {
	got := worklogPath("/ws", "")
	want := filepath.Join("/ws", ".worklog")
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func TestWorklogPath_SessionScoped(t *testing.T) {
	got := worklogPath("/ws", "abc123")
	want := filepath.Join("/ws", "sessions", "abc123", "worklog")
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func TestWorklogPath_DifferentSessions_DifferentPaths(t *testing.T) {
	a := worklogPath("/ws", "session-a")
	b := worklogPath("/ws", "session-b")
	if a == b {
		t.Error("different sessions should have different paths")
	}
}

// ── blueprintsDir ────────────────────────────────────────────────────────────

func TestBlueprintsDir_LegacyWhenEmpty(t *testing.T) {
	got := blueprintsDir("/ws", "")
	want := filepath.Join("/ws", "blueprints")
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func TestBlueprintsDir_SessionScoped(t *testing.T) {
	got := blueprintsDir("/ws", "abc123")
	want := filepath.Join("/ws", "blueprints", "abc123")
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

// ── appendWorklog / readWorklog round-trip ──────────────────────────────────

func TestWorklog_RoundTrip_SessionScoped(t *testing.T) {
	dir := t.TempDir()
	sid := "test-session"

	appendWorklog(dir, sid, "tag1", "OK", "first event")
	appendWorklog(dir, sid, "tag2", "FAILED", "broken thing")
	appendWorklog(dir, sid, "tag3", "OK", "third event")

	wl := readWorklog(dir, sid, 10)
	if !strings.Contains(wl, "first event") {
		t.Errorf("expected first event: %q", wl)
	}
	if !strings.Contains(wl, "broken thing") {
		t.Errorf("expected broken thing: %q", wl)
	}
	if !strings.Contains(wl, "third event") {
		t.Errorf("expected third event: %q", wl)
	}
}

func TestWorklog_RoundTrip_Legacy(t *testing.T) {
	dir := t.TempDir()
	appendWorklog(dir, "", "tag", "OK", "legacy entry")
	wl := readWorklog(dir, "", 10)
	if !strings.Contains(wl, "legacy entry") {
		t.Errorf("expected legacy entry: %q", wl)
	}

	// Verify it ended up at the legacy path.
	legacyPath := filepath.Join(dir, ".worklog")
	if _, err := os.Stat(legacyPath); os.IsNotExist(err) {
		t.Error("legacy worklog file was not created")
	}
}

func TestWorklog_SessionsAreIsolated(t *testing.T) {
	dir := t.TempDir()
	appendWorklog(dir, "session-A", "tag", "OK", "from session A")
	appendWorklog(dir, "session-B", "tag", "OK", "from session B")

	a := readWorklog(dir, "session-A", 10)
	b := readWorklog(dir, "session-B", 10)

	if !strings.Contains(a, "from session A") {
		t.Errorf("session A worklog missing its entry: %q", a)
	}
	if strings.Contains(a, "from session B") {
		t.Errorf("session A leaked from session B: %q", a)
	}
	if !strings.Contains(b, "from session B") {
		t.Errorf("session B worklog missing its entry: %q", b)
	}
	if strings.Contains(b, "from session A") {
		t.Errorf("session B leaked from session A: %q", b)
	}
}

func TestWorklog_AutoCreatesSessionDir(t *testing.T) {
	dir := t.TempDir()
	sid := "fresh-session"
	// Directory does not exist yet — appendWorklog should create it.
	appendWorklog(dir, sid, "tag", "OK", "first event")

	expectedDir := filepath.Join(dir, "sessions", sid)
	if _, err := os.Stat(expectedDir); os.IsNotExist(err) {
		t.Errorf("expected session dir %q to be auto-created", expectedDir)
	}
}

func TestWorklog_MaxLines(t *testing.T) {
	dir := t.TempDir()
	sid := "test-session"
	for i := 0; i < 20; i++ {
		appendWorklog(dir, sid, "tag", "OK", "event")
	}

	wl := readWorklog(dir, sid, 5)
	lines := strings.Split(strings.TrimSpace(wl), "\n")
	if len(lines) != 5 {
		t.Errorf("expected 5 lines, got %d", len(lines))
	}
}

func TestWorklog_NonExistent_ReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	wl := readWorklog(dir, "no-such-session", 10)
	if wl != "" {
		t.Errorf("expected empty for missing session, got %q", wl)
	}
}

// ── resetWorklog ─────────────────────────────────────────────────────────────

func TestResetWorklog_TruncatesSessionFile(t *testing.T) {
	dir := t.TempDir()
	sid := "test-session"
	appendWorklog(dir, sid, "tag", "OK", "before reset")
	resetWorklog(dir, sid)

	wl := readWorklog(dir, sid, 10)
	if wl != "" {
		t.Errorf("expected empty after reset, got %q", wl)
	}
}

func TestResetWorklog_OnlyAffectsTargetSession(t *testing.T) {
	dir := t.TempDir()
	appendWorklog(dir, "session-A", "tag", "OK", "session A entry")
	appendWorklog(dir, "session-B", "tag", "OK", "session B entry")

	resetWorklog(dir, "session-A")

	a := readWorklog(dir, "session-A", 10)
	b := readWorklog(dir, "session-B", 10)
	if a != "" {
		t.Errorf("session A should be empty after reset: %q", a)
	}
	if !strings.Contains(b, "session B entry") {
		t.Errorf("session B should be untouched: %q", b)
	}
}

// ── Blueprint session-scoping ────────────────────────────────────────────────

func TestLatestBlueprintPath_SessionScoped(t *testing.T) {
	dir := t.TempDir()

	// Set up a blueprint in session-A
	dirA := filepath.Join(dir, "blueprints", "session-A")
	os.MkdirAll(dirA, 0755)
	pathA := filepath.Join(dirA, "main.blueprint.md")
	os.WriteFile(pathA, []byte("session A plan"), 0644)

	// Set up a blueprint in session-B
	dirB := filepath.Join(dir, "blueprints", "session-B")
	os.MkdirAll(dirB, 0755)
	pathB := filepath.Join(dirB, "main.blueprint.md")
	os.WriteFile(pathB, []byte("session B plan"), 0644)

	gotA := latestBlueprintPath(dir, "session-A")
	if gotA != pathA {
		t.Errorf("session A: expected %q, got %q", pathA, gotA)
	}
	gotB := latestBlueprintPath(dir, "session-B")
	if gotB != pathB {
		t.Errorf("session B: expected %q, got %q", pathB, gotB)
	}
}

func TestLoadLatestBlueprint_SessionIsolation(t *testing.T) {
	dir := t.TempDir()

	dirA := filepath.Join(dir, "blueprints", "session-A")
	os.MkdirAll(dirA, 0755)
	os.WriteFile(filepath.Join(dirA, "main.blueprint.md"), []byte("session A content"), 0644)

	dirB := filepath.Join(dir, "blueprints", "session-B")
	os.MkdirAll(dirB, 0755)
	os.WriteFile(filepath.Join(dirB, "main.blueprint.md"), []byte("session B content"), 0644)

	a := loadLatestBlueprint(dir, "session-A")
	b := loadLatestBlueprint(dir, "session-B")

	if a != "session A content" {
		t.Errorf("session A: expected isolated content, got %q", a)
	}
	if b != "session B content" {
		t.Errorf("session B: expected isolated content, got %q", b)
	}
}

func TestLoadLatestBlueprint_LegacyPath(t *testing.T) {
	dir := t.TempDir()
	bpDir := filepath.Join(dir, "blueprints")
	os.MkdirAll(bpDir, 0755)
	os.WriteFile(filepath.Join(bpDir, "legacy.blueprint.md"), []byte("legacy content"), 0644)

	got := loadLatestBlueprint(dir, "")
	if got != "legacy content" {
		t.Errorf("expected legacy content, got %q", got)
	}
}

func TestLoadLatestBlueprint_MissingDir_ReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	got := loadLatestBlueprint(dir, "no-such-session")
	if got != "" {
		t.Errorf("expected empty for missing dir, got %q", got)
	}
}

func TestLatestBlueprintPath_PrefersMain_OverDebug(t *testing.T) {
	dir := t.TempDir()
	bpDir := filepath.Join(dir, "blueprints", "test-session")
	os.MkdirAll(bpDir, 0755)
	// _ prefix marks debugger/auto-generated blueprints.
	os.WriteFile(filepath.Join(bpDir, "_debug_fix_123.blueprint.md"), []byte("debug content"), 0644)
	os.WriteFile(filepath.Join(bpDir, "main.blueprint.md"), []byte("main content"), 0644)

	got := latestBlueprintPath(dir, "test-session")
	if !strings.HasSuffix(got, "main.blueprint.md") {
		t.Errorf("expected main blueprint to be picked over debug, got %q", got)
	}
}

func TestScanExistingBlueprints_SessionScoped(t *testing.T) {
	dir := t.TempDir()
	dirA := filepath.Join(dir, "blueprints", "session-A")
	os.MkdirAll(dirA, 0755)
	os.WriteFile(filepath.Join(dirA, "one.blueprint.md"), []byte("content A1"), 0644)
	os.WriteFile(filepath.Join(dirA, "two.blueprint.md"), []byte("content A2"), 0644)

	dirB := filepath.Join(dir, "blueprints", "session-B")
	os.MkdirAll(dirB, 0755)
	os.WriteFile(filepath.Join(dirB, "other.blueprint.md"), []byte("content B"), 0644)

	a := scanExistingBlueprints(dir, "session-A")
	if !strings.Contains(a, "content A1") || !strings.Contains(a, "content A2") {
		t.Errorf("session A should see both A blueprints: %q", a)
	}
	if strings.Contains(a, "content B") {
		t.Errorf("session A leaked from session B: %q", a)
	}
}

func TestScanExistingBlueprints_Empty(t *testing.T) {
	dir := t.TempDir()
	got := scanExistingBlueprints(dir, "missing")
	if got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}
