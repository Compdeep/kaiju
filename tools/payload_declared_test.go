package tools

import (
	"github.com/Compdeep/kaiju/agent"
	"sort"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// Every registered tool declares the fields its payload carries.
//
// A tool that declares only the envelope tells a planner that a result has a
// status and some text, and nothing about the result itself. So a step could not
// be wired into the next one by naming a field, and the whole text was quoted
// forward instead — which is what ten of these did, while several of them were
// already returning the fields in question.
//
// The exemption list is the remaining work, and it is empty. A tool added without
// a declared payload fails here rather than joining a backlog nobody is counting.
func TestEveryToolDeclaresItsPayload(t *testing.T) {
	// A tool belongs here only if its result genuinely has no fields — one blob of
	// text and nothing to name in it. Add the reason beside the name.
	undeclared := map[string]string{}

	// The memory store is supplied, because Register leaves out the tools whose
	// dependency is absent. With only a workspace it registered 18 tools, so the
	// three memory tools were never checked — and they declare no payload. Plugins
	// and an LLM client are left out: PluginConfig is an interface with no test
	// double here, and web_research needs a live client.
	mem, err := agent.NewMemory(t.TempDir())
	if err != nil {
		t.Fatalf("memory: %v", err)
	}
	reg := toolapi.NewRegistry()
	names, err := Register(reg, Deps{
		Workspace: t.TempDir(),
		Memory:    mem,
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if len(names) == 0 {
		t.Fatal("nothing was registered, so this check does nothing")
	}

	var missing []string
	for _, name := range names {
		tool, ok := reg.Get(name)
		if !ok {
			t.Errorf("%s was registered and cannot be fetched back", name)
			continue
		}

		out, isOutputter := tool.(toolapi.Outputter)
		if !isOutputter {
			if _, exempt := undeclared[name]; !exempt {
				missing = append(missing, name+" (declares nothing at all)")
			}
			continue
		}

		fields, has := toolapi.DeclaredPayloadFields(out.OutputSchema())
		reason, exempt := undeclared[name]
		switch {
		case has && exempt:
			t.Errorf("%s declares %v and is on the exemption list saying %q — take it off",
				name, fields, reason)
		case !has && !exempt:
			missing = append(missing, name)
		}
	}

	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("these declare no payload fields, so a planner cannot name anything they return: %s",
			strings.Join(missing, ", "))
	}
}
