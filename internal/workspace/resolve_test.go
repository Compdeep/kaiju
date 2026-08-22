package workspace

import (
	"path/filepath"
	"strings"
	"testing"
)

// Resolve treats the workspace as where work lands by default rather than the
// only place it may land. What it must not lose is the rule that motivated the
// zones: on 2026-04-18 a coder step overwrote cmd/kaiju/main.go, and the zones
// are what stops that.

func TestResolve_RelativePathsBehaveExactlyAsBefore(t *testing.T) {
	ws := "/srv/agent/workspace"
	for _, c := range []struct {
		path      string
		wantAllow bool
		why       string
	}{
		{"project/app.py", true, "inside an allowed zone"},
		{"media/out.png", true, "inside an allowed zone"},
		{"cmd/main.go", false, "not in an allowed zone — this is the 2026-04-18 case"},
		{"app.py", false, "the workspace root is not a zone"},
		{"project/../cmd/main.go", false, "an escape that lands outside the zones"},
		{"../outside.txt", false, "an escape out of the workspace entirely"},
	} {
		got, err := Resolve(ws, c.path)
		if c.wantAllow && err != nil {
			t.Errorf("Resolve(%q) refused (%s): %v", c.path, c.why, err)
		}
		if !c.wantAllow && err == nil {
			t.Errorf("Resolve(%q) allowed and must not (%s): %q", c.path, c.why, got)
		}
	}
}

// The change: a path with a root, outside the workspace, is a location the
// caller named. It is permitted here and graded by the gate, which can see the
// intent and clearance a path rule cannot.
func TestResolve_AllowsANamedLocationOutsideTheWorkspace(t *testing.T) {
	ws := "/srv/agent/workspace"
	for _, path := range []string{
		"/etc/apache2/sites-available/example.conf",
		"/srv/app/config.yml",
		"/var/log/app/app.log",
	} {
		got, err := Resolve(ws, path)
		if err != nil {
			t.Errorf("Resolve(%q) must be permitted, the gate decides: %v", path, err)
		}
		if got != path {
			t.Errorf("Resolve(%q) = %q, a named location is taken as given", path, got)
		}
	}
}

// THE ONE THAT MATTERS. Permitting rooted paths must not become a way around the
// zones: the same file refused as cmd/main.go must stay refused when spelled out
// in full, or the protection is decorative.
func TestResolve_ARootedPathInsideTheWorkspaceStillObeysTheZones(t *testing.T) {
	ws := "/srv/agent/workspace"

	refused := filepath.Join(ws, "cmd", "kaiju", "main.go")
	if got, err := Resolve(ws, refused); err == nil {
		t.Fatalf("writing the agent's own tree must stay refused however the path is spelled, got %q", got)
	}

	// And a rooted path that lands in a zone is fine, since a relative one would be.
	allowed := filepath.Join(ws, "project", "app.py")
	got, err := Resolve(ws, allowed)
	if err != nil {
		t.Fatalf("a rooted path inside a zone is the same write as the relative one: %v", err)
	}
	if got != allowed {
		t.Fatalf("got %q, want %q", got, allowed)
	}
}

// A path that merely starts with the same characters as the workspace is not
// inside it, and must not pick up the zone rules by accident.
func TestResolve_ASiblingDirectoryIsNotInsideTheWorkspace(t *testing.T) {
	ws := "/srv/agent/workspace"
	sibling := "/srv/agent/workspace-backup/notes.txt"
	got, err := Resolve(ws, sibling)
	if err != nil {
		t.Fatalf("a sibling path is outside the workspace: %v", err)
	}
	if got != sibling {
		t.Fatalf("got %q, want %q", got, sibling)
	}
}

// SafeJoin is untouched, so anything still calling it keeps the old refusal.
func TestSafeJoin_StillRefusesRootedPaths(t *testing.T) {
	if _, err := SafeJoin("/srv/agent/workspace", "/etc/passwd"); err == nil {
		t.Fatal("SafeJoin's own behaviour must not have moved")
	} else if !strings.Contains(err.Error(), "absolute paths are not allowed") {
		t.Fatalf("unexpected refusal: %v", err)
	}
}
