package prompt

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Application-defined sections.
//
// The sections above are the ones this engine itself reads. An application
// embedding kaiju has prompts of its own — stages this engine knows nothing
// about — and they belong in the same prompts.md as everything else, not
// scattered through Go consts. Without this, applying such a section logs
// "ignoring unknown section" and silently drops it.
//
// An application registers its names at startup, supplies the text through
// Apply or Load exactly as for built-in sections, and reads them back with
// Section.
//
// Deliberately kept as a name/text map rather than generated variables: this
// package cannot know an application's section names at compile time, and the
// alternative — an application editing this package to add its own — is the
// coupling the whole arrangement exists to avoid.

var (
	customMu sync.RWMutex
	custom   = map[string]string{}
)

/*
 * Register declares application-defined section names.
 * desc: After registering, Apply and Load will accept these names instead of
 *       ignoring them, and Section will return whatever text was supplied.
 *       Registering the same name twice is harmless. Call at startup, before
 *       Apply.
 * param: names - section names, conventionally UPPER_SNAKE like the built-ins.
 * return: an error if a name is empty or collides with a built-in section,
 *         since either would make the resulting prompts.md ambiguous.
 */
func Register(names ...string) error {
	customMu.Lock()
	defer customMu.Unlock()

	// Checked in full before anything is written. It used to register as it went and
	// return on the first bad name, so Register("A", "", "C") left A registered and
	// C not — a caller that reported the error and stopped still had half of what it
	// asked for in place, and Registered() then listed something that matched neither
	// the request nor nothing.
	clean := make([]string, 0, len(names))
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			return fmt.Errorf("prompt: cannot register an empty section name")
		}
		if _, builtin := targets[n]; builtin {
			return fmt.Errorf("prompt: %q is a built-in section; supply it through Apply rather than registering it", n)
		}
		clean = append(clean, n)
	}
	for _, n := range clean {
		if _, exists := custom[n]; !exists {
			custom[n] = ""
		}
	}
	return nil
}

/*
 * Section returns the text of an application-defined section.
 * desc: Returns "" when the name was never registered, or was registered but
 *       no prompts.md supplied it — so a caller can fall back to its own
 *       default rather than fail. Built-in sections are read through their
 *       package variables (prompt.Soul and so on), not through this.
 * param: name - the registered section name.
 * return: the section text, or "" if unset.
 */
func Section(name string) string {
	customMu.RLock()
	defer customMu.RUnlock()
	return custom[name]
}

/*
 * Registered lists the application-defined section names, sorted.
 * desc: For diagnostics and startup logging — an operator checking why a
 *       section did not take effect wants to see what was actually declared.
 * return: the registered names.
 */
func Registered() []string {
	customMu.RLock()
	defer customMu.RUnlock()
	out := make([]string, 0, len(custom))
	for n := range custom {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// isRegistered reports whether a name is one an application declared, without
// writing anything. The check half of setCustom, needed because an override file is
// now validated in full before any of it is applied.
func isRegistered(name string) bool {
	customMu.Lock()
	defer customMu.Unlock()
	_, ok := custom[name]
	return ok
}

// setCustom stores a section's text. Reports whether the name was registered;
// callers treat an unregistered name as unknown, exactly as before.
func setCustom(name, body string) bool {
	customMu.Lock()
	defer customMu.Unlock()
	if _, ok := custom[name]; !ok {
		return false
	}
	custom[name] = body
	return true
}
