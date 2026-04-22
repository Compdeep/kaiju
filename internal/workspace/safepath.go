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
var AllowedZones = []string{"project", "media", "canvas", "blueprints"}

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
// insufficient — see the cmd/kaiju/main.go incident 2026-04-18.
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
