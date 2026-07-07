package prompt

import (
	"os"
	"path/filepath"
	"testing"
)

// TestEmbeddedDefaultsPopulated verifies package init filled every required
// section from the embedded prompts.md.
func TestEmbeddedDefaultsPopulated(t *testing.T) {
	for _, name := range sectionOrder {
		if *targets[name] == "" {
			t.Errorf("section %q empty after init", name)
		}
	}
}

// TestLoadNoOverride: Load with a dir that has no prompts.md keeps embedded
// defaults and succeeds.
func TestLoadNoOverride(t *testing.T) {
	before := Soul
	if err := Load(t.TempDir()); err != nil {
		t.Fatalf("Load with no override: %v", err)
	}
	if Soul != before {
		t.Fatalf("Soul changed despite no override")
	}
}

// TestLoadPartialOverride overlays only SOUL and leaves the rest intact.
func TestLoadPartialOverride(t *testing.T) {
	dir := t.TempDir()
	custom := "CUSTOM SOUL BODY"
	if err := os.WriteFile(filepath.Join(dir, "prompts.md"), []byte("=== SOUL ===\n"+custom+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	holmesBefore := Holmes
	if err := Load(dir); err != nil {
		t.Fatalf("Load partial override: %v", err)
	}
	if Soul != custom {
		t.Fatalf("SOUL not overridden: got %q", Soul)
	}
	if Holmes != holmesBefore {
		t.Fatalf("HOLMES changed by a SOUL-only override")
	}
	// Restore embedded defaults for other tests.
	reinit()
}

// TestLoadMalformedOverrideFailsClosed: a non-empty override file with no
// recognizable delimiters is an error, not a silent fallback.
func TestLoadMalformedOverrideFailsClosed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "prompts.md"), []byte("just some text, no delimiters\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Load(dir); err == nil {
		t.Fatalf("expected error on malformed override, got nil")
	}
	reinit()
}

// TestLoadEmptyOverrideSectionFailsClosed: an override section that is present
// but empty is an error.
func TestLoadEmptyOverrideSectionFailsClosed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "prompts.md"), []byte("=== SOUL ===\n\n=== ROUTE ===\nok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Load(dir); err == nil {
		t.Fatalf("expected error on empty override section, got nil")
	}
	reinit()
}

// reinit restores the exported vars from the embedded defaults so tests that
// mutate them don't bleed into each other.
func reinit() {
	sections, _ := parseSections(embeddedPrompts)
	for _, name := range sectionOrder {
		*targets[name] = sections[name]
	}
}
