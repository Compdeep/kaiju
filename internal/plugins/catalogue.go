package plugins

import (
	"embed"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
)

// The catalogue of installable plugins, embedded so every binary carries it and
// there is no file to go missing at run time. It lives beside this code rather
// than under plugins/ because go:embed cannot reach a parent directory, and a
// second copy there would be free to drift from this one.
//
//go:embed catalogue.json
var catalogueFS embed.FS

// Kind says how a plugin reaches the binary, and therefore what installing it
// requires.
const (
	// KindGo is compiled in behind the build tag plugin_<name>.
	KindGo = "go"
	// KindPython is a folder under plugins/ served by the shared Python host.
	// Every one of them needs the single tag plugin_remote and no tag of its own.
	KindPython = "python"
)

// Entry is one installable plugin as the catalogue declares it. This is what
// COULD be built; Compiled reports what actually is.
type Entry struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Tier        string `json:"tier"`
	Description string `json:"description"`

	// Dir and Port describe a KindPython plugin: the folder the host loads it
	// from, and the port that host listens on.
	Dir  string `json:"dir,omitempty"`
	Port int    `json:"port,omitempty"`

	// Source and Commit describe a community plugin's origin, pinned. Nothing
	// fetches them yet; a community entry is added by an operator editing this
	// file, never by the agent, because compiling a plugin links its code into
	// kaiju's own binary with none of a tool's gating.
	Source string `json:"source,omitempty"`
	Commit string `json:"commit,omitempty"`
}

// Tag is the build tag that puts this plugin in a binary. Every Python plugin
// shares plugin_remote, because they are all reached through the one bridge, so
// asking for three of them asks for one tag.
func (e Entry) Tag() string {
	if e.Kind == KindPython {
		return "plugin_remote"
	}
	return "plugin_" + e.Name
}

var catalogue struct {
	Plugins []Entry `json:"plugins"`
}

func init() {
	b, err := catalogueFS.ReadFile("catalogue.json")
	if err != nil {
		// Unreachable: the file is embedded at compile time, so a failure here
		// means the binary was built without it, which the build would have
		// refused.
		panic("plugins: catalogue.json missing from the binary: " + err.Error())
	}
	if err := json.Unmarshal(b, &catalogue); err != nil {
		panic("plugins: catalogue.json is not valid JSON: " + err.Error())
	}
}

// Installable returns every plugin the catalogue declares, sorted by name. It
// answers "what can I install", and says nothing about what this binary has.
func Installable() []Entry {
	out := append([]Entry(nil), catalogue.Plugins...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Lookup returns the catalogue entry for a name.
func Lookup(name string) (Entry, bool) {
	for _, e := range catalogue.Plugins {
		if e.Name == name {
			return e, true
		}
	}
	return Entry{}, false
}

// Tags resolves plugin names to the set of build tags a binary needs to carry
// them, sorted and de-duplicated. A name the catalogue does not list is an error
// naming it: the alternative is building something the operator did not ask for,
// or silently dropping what they did.
//
// Resolution is deliberately separate from building. The caller can resolve, see
// what it would take, and decide — and a tool can refuse an unknown name without
// ever starting a compiler.
func Tags(names []string) ([]string, error) {
	seen := map[string]bool{}
	var tags []string
	var unknown []string
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		e, ok := Lookup(n)
		if !ok {
			unknown = append(unknown, n)
			continue
		}
		if t := e.Tag(); !seen[t] {
			seen[t] = true
			tags = append(tags, t)
		}
	}
	if len(unknown) > 0 {
		return nil, fmt.Errorf("the plugin catalogue does not list %s — `plugin_list` reports what it does list",
			strings.Join(unknown, ", "))
	}
	sort.Strings(tags)
	return tags, nil
}

// Missing returns the names in want that this binary was not built with, in the
// order asked. An empty result is the invariant holding: everything wanted is
// compiled in, so there is nothing to converge.
func Missing(want []string) []string {
	have := map[string]bool{}
	for _, n := range Compiled() {
		have[n] = true
	}
	// A Python plugin is carried by the bridge, which registers under the single
	// name "remote". It is the bridge that is or is not compiled in, so that is
	// what we check for one.
	bridge := have["remote"]
	var missing []string
	for _, n := range want {
		if n = strings.TrimSpace(n); n == "" {
			continue
		}
		if have[n] {
			continue
		}
		if e, ok := Lookup(n); ok && e.Kind == KindPython && bridge {
			continue
		}
		missing = append(missing, n)
	}
	return missing
}

// ActivationNames maps catalogue names to the names the registry knows plugins by.
//
// They are not the same thing, and conflating them cost a working install. A Go
// plugin registers under its own name. Every Python plugin is carried by the
// bridge, which registers under "remote" — so asking Activate for "illustrator"
// asks for a plugin that was never registered, it reports it missing, and the
// tools the bridge did load are never added.
//
// The result is sorted and de-duplicated, so three Python plugins ask for the
// bridge once.
func ActivationNames(want []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range want {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		name := n
		if e, ok := Lookup(n); ok && e.Kind == KindPython {
			name = "remote"
		}
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// legacyBridgeName is what config files written before the catalogue existed put
// in `plugins` to mean "connect to the plugin host and take whatever it serves".
const legacyBridgeName = "remote"

var legacyWarned sync.Once

// Normalise turns a wanted set into catalogue names.
//
// "remote" named the bridge, not a capability. In the new model you name the
// plugins — illustrator, webreader — and the bridge that carries them is implied.
// An existing config saying "remote" would otherwise fail to resolve, which took
// down the whole converge step and left its python plugins unstarted, so it is
// read as "every python plugin in the catalogue": the behaviour it used to have,
// since the host served all of them and the bridge took what it advertised.
//
// Called once at the entry points, so Tags, Missing, ActivationNames and the host
// supervisor all see the same set.
func Normalise(want []string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(n string) {
		if n != "" && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	for _, n := range want {
		n = strings.TrimSpace(n)
		if n != legacyBridgeName {
			add(n)
			continue
		}
		var names []string
		for _, e := range Installable() {
			if e.Kind == KindPython {
				add(e.Name)
				names = append(names, e.Name)
			}
		}
		legacyWarned.Do(func() {
			log.Printf("[plugins] config names %q, which was the bridge rather than a plugin; reading it as %s. Name the plugins you want instead.",
				legacyBridgeName, strings.Join(names, ", "))
		})
	}
	return out
}
