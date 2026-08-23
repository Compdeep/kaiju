package workspace

import (
	"fmt"
	"path/filepath"
	"strings"
)

// AllowedZones lists workspace-relative subdirectories where agent-generated
// files may land. Anything outside these zones is rejected by SafeJoin to
// stop the agent from editing its own source tree (or any sibling project)
// when running in CLI mode where workspace = cwd.
var AllowedZones = []string{"project", "media", "canvas", "blueprints", "uploads"}

// SafeJoin resolves relPath against workspace and verifies it falls inside
// one of AllowedZones. Returns the cleaned absolute path or an error.
//
// Blocks:
//   - absolute paths (/etc/passwd)
//   - ../ escapes (project/../cmd/main.go → workspace/cmd/main.go, rejected)
//   - writes at workspace root (compute.py → rejected, must be project/compute.py)
//   - writes into anything not in AllowedZones (cmd/, internal/, .kaiju/, etc.)
//
// This is the last line of defense against the planner/coder writing to the
// agent's own infrastructure. Prompt-level rules alone have proven
// insufficient — a coder step overwrote cmd/kaiju/main.go on 2026-04-18.
func SafeJoin(workspace, relPath string) (string, error) {
	if filepath.IsAbs(relPath) {
		return "", fmt.Errorf("absolute paths are not allowed (got %q) — use a workspace-relative path under %v", relPath, AllowedZones)
	}
	if workspace == "" {
		return "", fmt.Errorf("workspace is not configured")
	}

	abs := filepath.Clean(filepath.Join(workspace, relPath))
	wsClean := filepath.Clean(workspace)
	prefix := wsClean + string(filepath.Separator)

	if abs != wsClean && !strings.HasPrefix(abs, prefix) {
		return "", fmt.Errorf("path escapes workspace: %q resolves outside %s", relPath, wsClean)
	}

	rel, err := filepath.Rel(wsClean, abs)
	if err != nil {
		return "", fmt.Errorf("compute relative path: %w", err)
	}
	rel = filepath.ToSlash(rel)

	for _, zone := range AllowedZones {
		if rel == zone || strings.HasPrefix(rel, zone+"/") {
			return abs, nil
		}
	}
	return "", fmt.Errorf("path not in allowed zones %v: %q lands at %q", AllowedZones, relPath, rel)
}

// Resolve is SafeJoin with the workspace treated as a default location rather
// than a boundary.
//
// A relative path is resolved exactly as SafeJoin resolves it: under the
// workspace, no escapes, inside an allowed zone. A path with a root is taken as
// given — but only once it is established to be outside the workspace. One that
// lands inside gets the zone rules too, so the protection that motivated them
// cannot be stepped around by writing the same file absolutely.
//
// Why the boundary moved: a request to change something that already exists
// elsewhere — a service configuration, a repository, a file the user named by
// its full path — was refused, and the refusal told the caller to write a
// workspace-relative path instead, which for that request produces a file that
// does nothing. Meanwhile bash could write the same location unexamined, so the
// rule bound the route that records what it touched and left the one that does
// not. A write outside the workspace is graded by the gate, which can see the
// intent and clearance a path rule cannot.
func Resolve(workspace, path string) (string, error) {
	if !filepath.IsAbs(path) {
		return SafeJoin(workspace, path)
	}
	abs := filepath.Clean(path)
	if workspace == "" {
		return abs, nil
	}
	wsClean := filepath.Clean(workspace)
	if abs == wsClean || strings.HasPrefix(abs, wsClean+string(filepath.Separator)) {
		// Inside the workspace: same rules as any other workspace path, so the
		// agent cannot reach its own source tree by spelling the path in full.
		rel, err := filepath.Rel(wsClean, abs)
		if err != nil {
			return "", fmt.Errorf("compute relative path: %w", err)
		}
		return SafeJoin(workspace, filepath.ToSlash(rel))
	}
	return abs, nil
}
