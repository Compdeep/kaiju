// Package ui carries the configuration an application supplies to put its own
// identity on kaiju's web interface and to decide which of its parts exist.
//
// The interface itself is served from elsewhere; this package holds only what
// is handed to it, so an application can construct the configuration without
// pulling in an HTTP server, and so the same values can be read both by the Go
// side deciding which routes to register and by the page deciding what to draw.
// One answer, read twice, rather than a Go flag and a browser flag that can
// disagree.
package ui

import (
	"fmt"
	"regexp"
	"strings"
)

/*
 * Config is everything an application says about the interface.
 * desc: The zero value is deliberate on each of its three parts, and they do
 *       not all default the same way. Brand and Theme empty mean kaiju's own
 *       name and kaiju's own colours: an application that says nothing gets the
 *       interface unchanged. Sections empty means every optional part is OFF —
 *       an application that says nothing gets a chat and no administration.
 *
 *       They differ because forgetting costs differently. A forgotten brand
 *       shows kaiju's name where another product's should be, which is visible
 *       the first time anyone looks. A forgotten section mounts kaiju's user and
 *       scope administration inside a product that never asked for it, which is
 *       visible to whoever finds it. So the one that is embarrassing defaults to
 *       on and the one that is dangerous defaults to off.
 */
type Config struct {
	Brand    Brand    `json:"brand,omitempty"`
	Theme    Theme    `json:"theme,omitempty"`
	Sections Sections `json:"sections,omitempty"`
}

/*
 * Brand is the name and mark the interface wears.
 * desc: Empty fields keep kaiju's own, so a partial brand is legal — a name
 *       without a logo shows the new name beside the old mark.
 */
type Brand struct {
	// Name replaces "kaiju" in the document title and wherever the interface
	// names itself, including the word beside an assistant reply.
	Name string `json:"name,omitempty"`
	// LogoURL is fetched by the page and shown in place of kaiju's mark. A path
	// the same server answers, or an absolute URL.
	LogoURL string `json:"logo_url,omitempty"`
	// Attribution shows a small "powered by Kaiju" line. Off unless asked for,
	// because kaiju's own interface does not carry one today and the zero value
	// has to leave that as it is.
	Attribution bool `json:"attribution,omitempty"`
}

/*
 * Theme overrides the interface's colour tokens.
 * desc: The interface already defines its whole palette as CSS custom
 *       properties, once for the light mode and once for the dark, so a theme
 *       is a set of values for names that already exist rather than a
 *       stylesheet. Light and Dark are separate because a single map would
 *       apply one accent colour to both modes, which is a brand in one of them
 *       and unreadable in the other.
 */
type Theme struct {
	// Light overrides tokens in the light mode: "--accent" to "#2F6FED".
	Light map[string]string `json:"light,omitempty"`
	// Dark overrides the same tokens in the dark mode.
	Dark map[string]string `json:"dark,omitempty"`
	// Default is the mode a visitor who has never chosen one sees: "light",
	// "dark", or empty for the interface's own behaviour. A visitor's own
	// choice, once made, outranks this.
	Default string `json:"default,omitempty"`
}

/*
 * Sections says which optional parts of the interface exist.
 * desc: Every field is off in the zero value; see Config. A section that is off
 *       is off on both sides — the page does not draw it and the routes behind
 *       it are never registered, because a page that hides a button in front of
 *       a live endpoint has hidden nothing.
 */
type Sections struct {
	// Users is the administration surface: users, groups, scopes, and the
	// assignment of intent ranks to tools. Off leaves the interface a chat.
	Users bool `json:"users,omitempty"`
	// Workspace is the side panel that browses the agent's working directory
	// and shows the files in it. Off leaves the chat with no file browser,
	// media viewer or code preview.
	Workspace bool `json:"workspace,omitempty"`
}

// AllSections is every section on: kaiju's own interface, whole.
//
// Named rather than written out at the call site so that a section added later
// reaches kaiju's own daemon without anyone remembering to go and add it there.
func AllSections() Sections {
	return Sections{
		Users:     true,
		Workspace: true,
	}
}

// A CSS custom property name: two dashes and then letters, digits and dashes.
var tokenName = regexp.MustCompile(`^--[a-z0-9-]+$`)

// What a token value may not contain.
//
// The values are written into a stylesheet the page installs, so a value
// carrying a semicolon or a closing brace would end the declaration it sits in
// and start something else — a rule of the supplier's choosing, applying to any
// element they name. The characters below are the ones that let that happen.
const forbiddenInValue = ";{}<>\\\"'"

/*
 * Validate reports what in this configuration cannot be used.
 * desc: Call it before serving a configuration to a page. It checks the theme
 *       tokens, which are the only part that becomes code: a name that is not a
 *       custom property would land in the stylesheet as an unknown declaration,
 *       and a value carrying a semicolon or a brace could close the rule it is
 *       in and open another.
 *
 *       Brand and Sections are not checked. A name and a URL are text the page
 *       escapes, and a bool cannot be malformed.
 * return: an error naming the first token that fails, or nil.
 */
func (c Config) Validate() error {
	for _, m := range []struct {
		mode   string
		tokens map[string]string
	}{
		{"light", c.Theme.Light},
		{"dark", c.Theme.Dark},
	} {
		for name, value := range m.tokens {
			if !tokenName.MatchString(name) {
				return fmt.Errorf("ui: %s theme: %q is not a CSS custom property name (expected --something)", m.mode, name)
			}
			if strings.ContainsAny(value, forbiddenInValue) {
				return fmt.Errorf("ui: %s theme: value for %s contains one of %q, which would end the declaration it is written into", m.mode, name, forbiddenInValue)
			}
			if strings.TrimSpace(value) == "" {
				return fmt.Errorf("ui: %s theme: value for %s is empty", m.mode, name)
			}
		}
	}
	switch c.Theme.Default {
	case "", "light", "dark":
	default:
		return fmt.Errorf("ui: theme default is %q, expected \"light\", \"dark\" or empty", c.Theme.Default)
	}
	return nil
}
