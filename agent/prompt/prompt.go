// Package prompt is the single home for the agent's system prompts. The
// canonical text lives in prompts.md (embedded into the binary), split into
// named sections delimited by lines matching EXACTLY `=== NAME ===`. Each
// section is exposed as an exported package var so call sites reference
// prompt.Holmes, prompt.Aggregator, etc. instead of scattered consts.
//
// Section bodies are markdown and routinely contain `##`/`###` headers — that
// is why the section delimiter is `===`, never `#`.
//
// Load overlays an optional dataDir/prompts.md so operators can override any
// subset of sections without rebuilding. Resolution is fail-closed: if the
// override is malformed or leaves any required section empty, Load returns an
// error and the caller aborts boot.
package prompt

import (
	"bufio"
	_ "embed"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

//go:embed prompts.md
var embeddedPrompts string

// Exported prompt sections. Populated at package init from the embedded
// prompts.md so they are never empty even before Load runs, and optionally
// overridden by Load from dataDir/prompts.md.
var (
	Soul string
	// SoulTerse is the persona for a stage that must produce a schema rather
	// than prose. Empty when a host's prompts file omits the section, in which
	// case the full persona is used.
	SoulTerse     string
	Route         string
	Preflight     string
	Executive     string
	Aggregator    string
	Holmes        string
	Microplanner  string
	Observer      string
	Reflector     string
	Interjection  string
	Classifier    string
	Curator       string
	Chat          string
	Vision        string
	React         string
	CoverageGen   string
	CoverageHook  string
	GroundingGen  string
	GroundingHook string
)

// sectionOrder is the canonical list of required section names, in a stable
// order for validation and logging.
var sectionOrder = []string{
	"SOUL",
	"SOUL_TERSE",
	"ROUTE",
	"PREFLIGHT",
	"EXECUTIVE",
	"AGGREGATOR",
	"HOLMES",
	"MICROPLANNER",
	"OBSERVER",
	"REFLECTOR",
	"INTERJECTION",
	"CLASSIFIER",
	"CURATOR",
	"CHAT",
	"VISION",
	"REACT",
	"COVERAGE_GEN",
	"COVERAGE_HOOK",
	"GROUNDING_GEN",
	"GROUNDING_HOOK",
}

// targets maps each required section name to the package var it fills.
var targets = map[string]*string{
	"SOUL":           &Soul,
	"SOUL_TERSE":     &SoulTerse,
	"ROUTE":          &Route,
	"PREFLIGHT":      &Preflight,
	"EXECUTIVE":      &Executive,
	"AGGREGATOR":     &Aggregator,
	"HOLMES":         &Holmes,
	"MICROPLANNER":   &Microplanner,
	"OBSERVER":       &Observer,
	"REFLECTOR":      &Reflector,
	"INTERJECTION":   &Interjection,
	"CLASSIFIER":     &Classifier,
	"CURATOR":        &Curator,
	"CHAT":           &Chat,
	"VISION":         &Vision,
	"REACT":          &React,
	"COVERAGE_GEN":   &CoverageGen,
	"COVERAGE_HOOK":  &CoverageHook,
	"GROUNDING_GEN":  &GroundingGen,
	"GROUNDING_HOOK": &GroundingHook,
}

func init() {
	sections, err := parseSections(embeddedPrompts)
	if err != nil {
		panic("prompt: embedded prompts.md unparseable: " + err.Error())
	}
	for _, name := range sectionOrder {
		body, ok := sections[name]
		if !ok || body == "" {
			// The embedded file is authored in-repo — a missing/empty
			// required section is a build-time bug, not a runtime condition.
			panic(fmt.Sprintf("prompt: embedded prompts.md missing required section %q", name))
		}
		*targets[name] = body
	}
}

// Load overlays dataDir/prompts.md (if present) onto the embedded defaults and
// validates the result. Fail-closed: a malformed override, an empty override
// section, or any required section left empty returns a non-nil error. A
// missing override file is fine — the embedded defaults stand. Runs once at
// boot; performs no per-query IO.
/*
 * Apply overlays prompt sections supplied in-process by the embedding
 * application, before any operator override from disk.
 * desc: Load reads a file from dataDir, which suits an operator tweaking a
 *       running install. An application embedding kaiju needs something else:
 *       its prompts are part of the product and belong in its binary, not in a
 *       directory a deployment might not have, or might lose. Apply is that
 *       path — the caller passes its own embedded prompts.md and gets the same
 *       per-section semantics and the same fail-closed validation.
 *
 *       Precedence ends up: kaiju defaults, then the application, then the
 *       operator. Each layer overrides only the sections it names.
 *
 *       Call once at startup, before anything reads a prompt. The section
 *       variables are package-level and read on every LLM call, so applying
 *       them while work is in flight is a data race.
 * param: source - a label for logs and errors (e.g. "enbarr/prompts.md").
 * param: data - the prompts.md content, `=== NAME ===` delimited.
 * return: an error if the content is malformed or names an empty section.
 */
func Apply(source string, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	return applyOverride(source, data)
}

func Load(dataDir string) error {
	if dataDir != "" {
		path := filepath.Join(dataDir, "prompts.md")
		data, err := os.ReadFile(path)
		switch {
		case err == nil:
			if aerr := applyOverride(path, data); aerr != nil {
				return aerr
			}
		case os.IsNotExist(err):
			// No override — embedded defaults stand.
		default:
			return fmt.Errorf("prompt: reading override %s: %w", path, err)
		}
	}

	// Final validation: every required section must be present and non-empty.
	for _, name := range sectionOrder {
		if strings.TrimSpace(*targets[name]) == "" {
			return fmt.Errorf("prompt: required section %q is empty after load", name)
		}
	}
	return nil
}

// applyOverride parses an override file and overlays its sections. Partial
// override is allowed (e.g. override only SOUL). Any parse failure, a file
// with zero recognizable sections, or an empty override section is an error.
func applyOverride(path string, data []byte) error {
	overrides, err := parseSections(string(data))
	if err != nil {
		return fmt.Errorf("prompt: parsing override %s: %w", path, err)
	}
	if len(overrides) == 0 {
		return fmt.Errorf("prompt: override %s parsed to zero sections (malformed — expected `=== NAME ===` delimiters)", path)
	}
	for name, body := range overrides {
		dst, known := targets[name]
		if !known {
			// Not a built-in. An application may have registered it as one of
			// its own; only if it did not is the name genuinely unknown.
			if strings.TrimSpace(body) == "" {
				return fmt.Errorf("prompt: section %q in %s is empty", name, path)
			}
			if setCustom(name, body) {
				log.Printf("[prompt] overriding %s from %s (application section)", name, path)
				continue
			}
			log.Printf("[prompt] override %s: ignoring unknown section %q", path, name)
			continue
		}
		if strings.TrimSpace(body) == "" {
			return fmt.Errorf("prompt: override section %q in %s is empty", name, path)
		}
		*dst = body
		log.Printf("[prompt] overriding %s from %s", name, path)
	}
	return nil
}

// parseSections splits the content into named sections. A section starts at a
// line matching EXACTLY `=== NAME ===` and its body runs until the next such
// delimiter or EOF. Bodies are TrimSpace'd.
func parseSections(content string) (map[string]string, error) {
	sections := make(map[string]string)
	var curName string
	var curBody strings.Builder

	flush := func() {
		if curName != "" {
			sections[curName] = strings.TrimSpace(curBody.String())
		}
	}

	scanner := bufio.NewScanner(strings.NewReader(content))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if name, ok := parseDelimiter(line); ok {
			flush()
			curName = name
			curBody.Reset()
			continue
		}
		if curName != "" {
			curBody.WriteString(line)
			curBody.WriteByte('\n')
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	flush()
	return sections, nil
}

// parseDelimiter reports whether line is EXACTLY a `=== NAME ===` section
// delimiter and returns NAME. It deliberately does not treat markdown `#`
// headers or `===` underlines as delimiters.
func parseDelimiter(line string) (string, bool) {
	const pre, suf = "=== ", " ==="
	if len(line) < len(pre)+len(suf) {
		return "", false
	}
	if !strings.HasPrefix(line, pre) || !strings.HasSuffix(line, suf) {
		return "", false
	}
	name := line[len(pre) : len(line)-len(suf)]
	if name == "" || strings.Contains(name, "===") {
		return "", false
	}
	return name, true
}
