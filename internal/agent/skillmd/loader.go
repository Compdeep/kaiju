package skillmd

import (
	"log"
	"os"
	"path/filepath"
	"runtime"

	"github.com/user/kaiju/internal/agent/tools"
)

// DefaultDirs returns the standard skill search directories in precedence order.
// Later directories override earlier ones (same name = last wins).
//
// Precedence (low → high):
//   1. <dataDir>/skills/bundled     shipped with Kaiju install
//   2. <dataDir>/skills             user-installed from ClawHub
//   3. <workspace>/skills           workspace-specific overrides (highest)
func DefaultDirs(dataDir, workspace string) []string {
	dirs := []string{
		filepath.Join(dataDir, "skills", "bundled"),
		filepath.Join(dataDir, "skills"),
	}
	if workspace != "" {
		dirs = append(dirs, filepath.Join(workspace, "skills"))
	}
	return dirs
}

// LoadDir loads all SKILL.md files from a single directory.
// Layout: dir/<skill-name>/SKILL.md (each skill in its own subdirectory)
func LoadDir(dir string, reg *tools.Registry) ([]*SkillMD, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var loaded []*SkillMD
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		skillPath := filepath.Join(dir, entry.Name(), "SKILL.md")
		data, err := os.ReadFile(skillPath)
		if err != nil {
			continue // no SKILL.md in this subdir
		}

		fm, body, err := Parse(data)
		if err != nil {
			log.Printf("[skillmd] parse %s: %v", skillPath, err)
			continue
		}

		// Platform gating
		if err := CheckGating(fm.Metadata); err != nil {
			log.Printf("[skillmd] skip %s: %v", fm.Name, err)
			continue
		}

		info, _ := os.Stat(skillPath)
		modTime := info.ModTime()

		s := NewSkillMD(fm, body, filepath.Join(dir, entry.Name()), skillPath, modTime, reg)
		loaded = append(loaded, s)
	}

	return loaded, nil
}

// LoadFromDirs loads from multiple directories in precedence order.
// Later directories override earlier ones (same name = last wins).
func LoadFromDirs(dirs []string, reg *tools.Registry) ([]*SkillMD, error) {
	byName := make(map[string]*SkillMD)
	var order []string

	for _, dir := range dirs {
		skills, err := LoadDir(dir, reg)
		if err != nil {
			log.Printf("[skillmd] load dir %s: %v", dir, err)
			continue
		}
		for _, s := range skills {
			if _, exists := byName[s.Name()]; !exists {
				order = append(order, s.Name())
			}
			byName[s.Name()] = s // last wins
		}
	}

	result := make([]*SkillMD, 0, len(byName))
	for _, name := range order {
		result = append(result, byName[name])
	}
	for name, s := range byName {
		found := false
		for _, n := range order {
			if n == name {
				found = true
				break
			}
		}
		if !found {
			result = append(result, s)
		}
	}

	return result, nil
}

// userHomeDir returns the user's home directory, accounting for platform differences.
func userHomeDir() string {
	if runtime.GOOS == "windows" {
		return os.Getenv("USERPROFILE")
	}
	home, _ := os.UserHomeDir()
	return home
}
