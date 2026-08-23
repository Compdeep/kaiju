package skillmd

import (
	"bytes"
	"fmt"

	"gopkg.in/yaml.v3"
)

// Frontmatter is the parsed YAML header of a SKILL.md file.
type Frontmatter struct {
	Name                   string         `yaml:"name"`
	Description            string         `yaml:"description"`
	UserInvocable          *bool          `yaml:"user-invocable"`
	DisableModelInvocation bool           `yaml:"disable-model-invocation"`
	CommandDispatch        string         `yaml:"command-dispatch"`
	Metadata               *SkillMetadata `yaml:"metadata,omitempty"`
}

// SkillMetadata holds platform gating constraints.
type SkillMetadata struct {
	OS       []string       `yaml:"os"`
	Requires *SkillRequires `yaml:"requires"`
}

// SkillRequires lists external dependencies.
type SkillRequires struct {
	Bins []string `yaml:"bins"`
	Env  []string `yaml:"env"`
}

// Parse splits raw SKILL.md bytes into Frontmatter + body string.
// Returns error if frontmatter delimiters (---) are missing or name is empty.
func Parse(data []byte) (*Frontmatter, string, error) {
	// Frontmatter must start with "---\n"
	data = bytes.TrimLeft(data, "\xef\xbb\xbf") // strip BOM
	if !bytes.HasPrefix(bytes.TrimSpace(data), []byte("---")) {
		return nil, "", fmt.Errorf("missing frontmatter delimiter (---)")
	}

	// Find the opening and closing ---
	trimmed := bytes.TrimSpace(data)
	rest := trimmed[3:] // skip first ---
	rest = bytes.TrimLeft(rest, " \t")
	if len(rest) > 0 && rest[0] == '\n' {
		rest = rest[1:]
	} else if len(rest) > 1 && rest[0] == '\r' && rest[1] == '\n' {
		rest = rest[2:]
	}

	idx := bytes.Index(rest, []byte("\n---"))
	if idx < 0 {
		return nil, "", fmt.Errorf("missing closing frontmatter delimiter (---)")
	}

	yamlBlock := rest[:idx]
	body := rest[idx+4:] // skip "\n---"
	// Trim leading newline from body
	body = bytes.TrimLeft(body, "\r\n")

	var fm Frontmatter
	if err := yaml.Unmarshal(yamlBlock, &fm); err != nil {
		return nil, "", fmt.Errorf("parse frontmatter YAML: %w", err)
	}

	if fm.Name == "" {
		return nil, "", fmt.Errorf("frontmatter missing required field: name")
	}

	return &fm, string(body), nil
}
