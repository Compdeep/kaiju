package skillmd

import (
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// PlannerHeadings are the section headings the executive extracts from a skill
// body to build the planner's Skill Guidance block. A skill whose body uses
// none of them still reaches the planner by name and description, but
// contributes no guidance: the extraction matches nothing and the body is
// dropped. The drop is otherwise invisible — one matching skill is enough for
// the Skill Guidance section to render and look healthy — so LoadDir warns at
// boot instead of leaving it to be noticed by its absence from a prompt.
var PlannerHeadings = []string{"## Planning Guidance", "## RULES"}

// hasPlannerGuidance reports whether a body carries a section the executive
// will extract. Mirrors Text.ExtractSection's plain substring match, so the
// two agree on what counts as present.
func hasPlannerGuidance(body string) bool {
	for _, h := range PlannerHeadings {
		if strings.Contains(body, h) {
			return true
		}
	}
	return false
}

// DefaultDirs returns the standard skill search directories in precedence order.
// Later directories override earlier ones (same name = last wins).
//
// Precedence (low → high):
//  1. <dataDir>/skills/bundled     shipped with the binary, replaced on upgrade
//  2. <dataDir>/skills             user-installed, never touched by an upgrade
//  3. <workspace>/skills           workspace-specific overrides (highest)
//
// The first two are separate directories rather than one because an upgrade has
// to be able to replace the cards it ships without deciding what to do about
// the ones somebody wrote. Seeding shipped cards into the user directory makes
// every upgrade choose between overwriting an edit and leaving a stale card.
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
func LoadDir(dir string, reg *toolapi.Registry) ([]*SkillMD, error) {
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

		if !hasPlannerGuidance(body) {
			log.Printf("[skillmd] %s: body has none of %v — it will reach the planner as name+description only, with no guidance (%s)",
				fm.Name, PlannerHeadings, skillPath)
		}

		s := NewSkillMD(fm, body, filepath.Join(dir, entry.Name()), skillPath, modTime, reg)
		loaded = append(loaded, s)
	}

	return loaded, nil
}

// LoadFromDirs loads from multiple directories in precedence order.
// Later directories override earlier ones (same name = last wins).
func LoadFromDirs(dirs []string, reg *toolapi.Registry) ([]*SkillMD, error) {
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
				// First wins — home dir skills take priority over bundled/repo.
				// Users edit their home copy; the bundled copy is the recovery default.
				order = append(order, s.Name())
				byName[s.Name()] = s
			}
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
