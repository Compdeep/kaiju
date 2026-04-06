package skillmd

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	"github.com/user/kaiju/internal/agent/tools" // for Registry type
)

var placeholderRe = regexp.MustCompile(`\{\{(\w+)\}\}`)

// SkillMD represents a SKILL.md-based skill that provides planning guidance.
// It only implements tools.Tool (and thus goes into the registry) when
// CommandDispatch is set — in that case it forwards execution to the target tool.
// Skills without CommandDispatch provide planning guidance only and should NOT
// be registered in the tool registry.
type SkillMD struct {
	fm       Frontmatter
	body     string
	params   json.RawMessage
	baseDir  string
	filePath string
	modTime  time.Time
	registry *tools.Registry
}

// NewSkillMD creates a SkillMD from parsed frontmatter and body.
func NewSkillMD(fm *Frontmatter, body, baseDir, filePath string, modTime time.Time, reg *tools.Registry) *SkillMD {
	s := &SkillMD{
		fm:       *fm,
		body:     body,
		baseDir:  baseDir,
		filePath: filePath,
		modTime:  modTime,
		registry: reg,
	}
	s.params = s.buildParams()
	return s
}

func (s *SkillMD) Name() string        { return s.fm.Name }
func (s *SkillMD) Description() string  { return s.fm.Description }

func (s *SkillMD) Parameters() json.RawMessage { return s.params }

// Body returns the raw markdown body for section extraction (e.g. Planning Guidance).
func (s *SkillMD) Body() string { return s.body }

// Source implements tools.ToolMeta.
func (s *SkillMD) Source() string { return "skillmd" }

// IsUserInvocable implements tools.ToolMeta.
func (s *SkillMD) IsUserInvocable() bool {
	if s.fm.UserInvocable == nil {
		return true // default
	}
	return *s.fm.UserInvocable
}

// FilePath returns the full path to the SKILL.md file.
func (s *SkillMD) FilePath() string { return s.filePath }

// ModTime returns the file modification time.
func (s *SkillMD) ModTime() time.Time { return s.modTime }

// HasCommandDispatch returns true if this skill wraps a compiled tool.
// Skills with CommandDispatch are executable tools; skills without it
// provide planning guidance only.
func (s *SkillMD) HasCommandDispatch() bool {
	return s.fm.CommandDispatch != ""
}

// Impact returns the IBE impact tier. For command-dispatch skills, delegates
// to the target tool. For guidance-only skills, returns 0 (lowest tier).
func (s *SkillMD) Impact(params map[string]any) int {
	if s.fm.CommandDispatch != "" && s.registry != nil {
		if target, ok := s.registry.Get(s.fm.CommandDispatch); ok {
			return target.Impact(params)
		}
	}
	return 0
}

// Execute dispatches to the compiled tool if CommandDispatch is set.
// For guidance-only skills (no CommandDispatch), returns an error —
// skills are not directly executable, they provide planning guidance.
func (s *SkillMD) Execute(ctx context.Context, params map[string]any) (string, error) {
	// Command-dispatch: forward to compiled tool
	if s.fm.CommandDispatch != "" {
		if s.registry == nil {
			return "", fmt.Errorf("command-dispatch %q: no registry", s.fm.CommandDispatch)
		}
		target, ok := s.registry.Get(s.fm.CommandDispatch)
		if !ok {
			return "", fmt.Errorf("command-dispatch target %q not found in registry", s.fm.CommandDispatch)
		}
		return target.Execute(ctx, params)
	}

	// Guidance-only skill — not directly executable
	return "", fmt.Errorf("skill %q is not directly executable (provides planning guidance only)", s.fm.Name)
}

// buildParams auto-extracts JSON Schema from {{placeholder}} patterns in body.
func (s *SkillMD) buildParams() json.RawMessage {
	matches := placeholderRe.FindAllStringSubmatch(s.body, -1)
	if len(matches) == 0 {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}

	// Deduplicate
	seen := make(map[string]bool)
	var unique []string
	for _, m := range matches {
		if !seen[m[1]] {
			seen[m[1]] = true
			unique = append(unique, m[1])
		}
	}

	props := make(map[string]any, len(unique))
	required := make([]string, 0, len(unique))
	for _, name := range unique {
		props[name] = map[string]string{"type": "string"}
		required = append(required, name)
	}

	schema := map[string]any{
		"type":       "object",
		"properties": props,
		"required":   required,
	}
	data, _ := json.Marshal(schema)
	return data
}
