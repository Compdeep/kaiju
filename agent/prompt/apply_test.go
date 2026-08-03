package prompt

import (
	"strings"
	"testing"
)

// restore puts the package-level sections back after a test mutates them.
// They are global by design, so a test that skipped this would corrupt the next.
func restore(t *testing.T) {
	t.Helper()
	sections, err := parseSections(embeddedPrompts)
	if err != nil {
		t.Fatalf("re-parse embedded: %v", err)
	}
	t.Cleanup(func() {
		for name, body := range sections {
			if dst, ok := targets[name]; ok {
				*dst = body
			}
		}
	})
}

func TestApplyOverridesOnlyNamedSections(t *testing.T) {
	restore(t)
	before := Reflector

	if err := Apply("test/prompts.md", []byte("=== SOUL ===\nyou are a security analyst\n")); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !strings.Contains(Soul, "security analyst") {
		t.Errorf("SOUL not overridden: %q", Soul)
	}
	if Reflector != before {
		t.Error("an unnamed section was modified")
	}
}

func TestApplyThenLoadPrecedence(t *testing.T) {
	restore(t)
	// The application applies its own, then the operator's file wins on the
	// sections it names.
	if err := Apply("app", []byte("=== SOUL ===\nfrom the application\n=== CHAT ===\napp chat\n")); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := applyOverride("operator", []byte("=== SOUL ===\nfrom the operator\n")); err != nil {
		t.Fatalf("override: %v", err)
	}
	if !strings.Contains(Soul, "operator") {
		t.Errorf("operator should win for SOUL: %q", Soul)
	}
	if !strings.Contains(Chat, "app chat") {
		t.Errorf("application section not retained where the operator was silent: %q", Chat)
	}
}

func TestApplyIsFailClosed(t *testing.T) {
	restore(t)
	if err := Apply("bad", []byte("this file has no section delimiters at all")); err == nil {
		t.Error("malformed content must be rejected, not silently ignored")
	}
	if err := Apply("empty-section", []byte("=== SOUL ===\n   \n")); err == nil {
		t.Error("an empty section must be rejected")
	}
}

func TestApplyEmptyIsNoOp(t *testing.T) {
	restore(t)
	before := Soul
	if err := Apply("none", nil); err != nil {
		t.Fatalf("nil data should be a no-op: %v", err)
	}
	if Soul != before {
		t.Error("nil data changed a section")
	}
}

// An application naming a section kaiju does not have should be told, not
// silently ignored — otherwise a typo looks like a working override.
func TestUnknownSectionIsNotFatalButIsVisible(t *testing.T) {
	restore(t)
	if err := Apply("app", []byte("=== NOT_A_SECTION ===\nx\n=== SOUL ===\nreal\n")); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !strings.Contains(Soul, "real") {
		t.Error("the valid section alongside an unknown one was dropped")
	}
}
