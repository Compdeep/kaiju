package prompt

import (
	"strings"
	"testing"
)

func clearCustom(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		customMu.Lock()
		custom = map[string]string{}
		customMu.Unlock()
	})
}

func TestApplicationSectionRoundTrip(t *testing.T) {
	clearCustom(t)
	if err := Register("VERDICT", "DEDUP"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := Apply("app", []byte("=== VERDICT ===\nweigh the evidence\n=== DEDUP ===\nsame issue?\n")); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := Section("VERDICT"); !strings.Contains(got, "weigh the evidence") {
		t.Errorf("VERDICT = %q", got)
	}
	if got := Section("DEDUP"); !strings.Contains(got, "same issue?") {
		t.Errorf("DEDUP = %q", got)
	}
}

// Unregistered names stay ignored — that is what stops a typo silently
// becoming a section.
func TestUnregisteredSectionStillIgnored(t *testing.T) {
	clearCustom(t)
	if err := Apply("app", []byte("=== NEVER_REGISTERED ===\nx\n")); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if Section("NEVER_REGISTERED") != "" {
		t.Error("an unregistered section was stored")
	}
}

func TestRegisterRejectsBuiltinCollision(t *testing.T) {
	clearCustom(t)
	if err := Register("SOUL"); err == nil {
		t.Error("registering a built-in name must be refused — it would make prompts.md ambiguous")
	}
	if err := Register(""); err == nil {
		t.Error("an empty name must be refused")
	}
}

func TestRegisterIsIdempotentAndPreservesText(t *testing.T) {
	clearCustom(t)
	_ = Register("VERDICT")
	_ = Apply("app", []byte("=== VERDICT ===\nfirst\n"))
	if err := Register("VERDICT"); err != nil {
		t.Fatalf("re-register: %v", err)
	}
	if Section("VERDICT") != "first" {
		t.Errorf("re-registering wiped the text: %q", Section("VERDICT"))
	}
}

func TestUnsuppliedSectionIsEmptyNotFatal(t *testing.T) {
	clearCustom(t)
	_ = Register("NEVER_SUPPLIED")
	if err := Apply("app", []byte("=== SOUL ===\nx\n")); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// Empty, so the application can fall back to its own default rather than
	// failing to start.
	if Section("NEVER_SUPPLIED") != "" {
		t.Error("expected empty")
	}
	if len(Registered()) != 1 || Registered()[0] != "NEVER_SUPPLIED" {
		t.Errorf("Registered() = %v", Registered())
	}
}

// An application section supplied empty is a mistake, and fail-closed applies
// to it exactly as to a built-in.
func TestEmptyApplicationSectionIsRejected(t *testing.T) {
	clearCustom(t)
	_ = Register("VERDICT")
	if err := Apply("app", []byte("=== VERDICT ===\n   \n")); err == nil {
		t.Error("an empty application section must be rejected")
	}
}
