package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// What every tool's declared schemas have to be true of.
//
// A schema is a promise made to a model: these are my arguments, this is what I
// return. Nothing checks a promise by writing it down, and until this file
// existed nothing checked these at all — which is how fifteen tools came to
// describe themselves to the planner as the envelope they are wrapped in
// rather than as themselves.
//
// Three properties, checked over every tool rather than over a chosen example:
//
//   - the schemas parse, and are objects with properties
//   - the output schema describes the tool, not the envelope around it
//   - a tool that declares no output schema is listed as such, so the gap is
//     counted rather than discovered
//
// The inventory below is checked against the source, so a tool added without
// being listed fails rather than being skipped in silence.

// schemaFixtures is every tool in this package, built with whatever its
// constructor needs. Zero values are used where a constructor takes
// dependencies: nothing here is executed, only asked what it declares.
func schemaFixtures() map[string]toolapi.Tool {
	return map[string]toolapi.Tool{
		"archive":        &Archive{},
		"bash":           &Bash{},
		"clipboard":      &Clipboard{},
		"disk_usage":     &DiskUsage{},
		"env_list":       &EnvList{},
		"file_list":      &FileList{},
		"file_read":      &FileRead{},
		"file_write":     &FileWrite{},
		"git":            &Git{},
		"memory_recall":  &MemoryRecall{},
		"message_search": &MessageSearch{},
		"memory_search":  &MemorySearch{},
		"memory_store":   &MemoryStore{},
		"net_info":       &NetInfo{},
		"office_extract": &OfficeExtract{},
		"panel_push":     &PanelPush{},
		"plugin_list":    &PluginList{},
		"process_kill":   &ProcessKill{},
		"process_list":   &ProcessList{},
		"service":        &Service{},
		"sysinfo":        &Sysinfo{},
		"web_fetch":      &WebFetch{},
		"web_research":   &WebResearch{},
		"web_search":     &WebSearch{},
	}
}

// looksLikeTheEnvelope reports whether a schema is describing the wrapper
// rather than a tool.
//
// Not "does it mention status": web_fetch's payload has a status of its own,
// the HTTP status line, and a tool is entitled to any field name it likes. The
// envelope is recognised by carrying all four of its framing keys at once,
// which no tool payload here does.
func looksLikeTheEnvelope(props map[string]json.RawMessage) bool {
	for _, key := range []string{"type", "status", "content", "detail"} {
		if _, found := props[key]; !found {
			return false
		}
	}
	return true
}

// The output schema has to describe the tool. This is the check that was
// missing: the fields moved one level down into the envelope and two of the
// three readers kept listing the level above, so the planner was told every
// tool returns the same five keys.
func TestOutputSchemasDescribeTheToolNotTheEnvelope(t *testing.T) {
	for name, tool := range schemaFixtures() {
		t.Run(name, func(t *testing.T) {
			schema := toolapi.GetOutputSchema(tool)
			if schema == nil {
				t.Skipf("%s declares no output schema", name)
			}
			if !json.Valid(schema) {
				t.Fatalf("output schema is not valid JSON:\n%s", schema)
			}

			payload := toolapi.PayloadSchema(schema)
			if payload == nil {
				return // carries only text; nothing to describe, which is honest
			}
			var s struct {
				Properties map[string]json.RawMessage `json:"properties"`
			}
			if err := json.Unmarshal(payload, &s); err != nil {
				t.Fatalf("payload schema is not an object: %v\n%s", err, payload)
			}
			if len(s.Properties) == 0 {
				return // an unshaped payload, which the planner is simply not told about
			}
			if looksLikeTheEnvelope(s.Properties) {
				t.Errorf("this is the envelope, not the tool — a reader that stopped at the "+
					"wrapper would show the planner these five keys for every tool alike:\n%s", payload)
			}
		})
	}
}

// Parameters are the other half of the promise, and they go to the model as a
// function schema, so a malformed one is a call the model cannot make.
func TestParameterSchemasAreWellFormed(t *testing.T) {
	for name, tool := range schemaFixtures() {
		t.Run(name, func(t *testing.T) {
			params := tool.Parameters()
			if !json.Valid(params) {
				t.Fatalf("parameter schema is not valid JSON:\n%s", params)
			}
			var s struct {
				Type       string                     `json:"type"`
				Properties map[string]json.RawMessage `json:"properties"`
				Required   []string                   `json:"required"`
			}
			if err := json.Unmarshal(params, &s); err != nil {
				t.Fatalf("parameter schema is not an object: %v", err)
			}
			if s.Type != "object" {
				t.Errorf("parameter schema type = %q, want object", s.Type)
			}
			// A required argument that is not declared cannot be supplied: the
			// model is told to send something the schema does not describe.
			for _, req := range s.Required {
				if _, ok := s.Properties[req]; !ok {
					t.Errorf("%q is required but not declared in properties", req)
				}
			}
		})
	}
}

// Which tools promise nothing about their output. Not a failure — some tools
// return prose — but a number that should move on purpose rather than drift,
// because a tool with no output schema can never be chained by a planner.
func TestToolsWithoutAnOutputSchemaAreCounted(t *testing.T) {
	var missing []string
	for name, tool := range schemaFixtures() {
		if toolapi.GetOutputSchema(tool) == nil {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	t.Logf("tools declaring no output schema (%d): %v", len(missing), missing)
	if len(missing) > 0 {
		t.Logf("a planner cannot write a ${step.N.field} reference into any of these")
	}
}

// The silent case: a tool added to the package and left out of the inventory
// above, which would then never be checked. The source is the authority.
func TestEveryToolWithASchemaIsInTheInventory(t *testing.T) {
	declared := map[string]bool{}
	for name := range schemaFixtures() {
		declared[name] = true
	}

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	// The receiver type of every OutputSchema method in the package.
	re := regexp.MustCompile(`func \(\w+ \*(\w+)\) OutputSchema\(\)`)
	types := map[string]bool{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(src), -1) {
			types[m[1]] = true
		}
	}

	// Map the inventory's instances back to their type names.
	inventoried := map[string]bool{}
	for _, tool := range schemaFixtures() {
		typ := reflect.TypeOf(tool)
		for typ.Kind() == reflect.Ptr {
			typ = typ.Elem()
		}
		inventoried[typ.Name()] = true
	}

	var missing []string
	for typ := range types {
		if !inventoried[typ] {
			missing = append(missing, typ)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("these declare an output schema and are not in schemaFixtures, so nothing checks them: %v", missing)
	}
}

// A field declared in a schema must appear somewhere in the source that declares
// it.
//
// A schema that lies is worse than none: the planner writes a reference to a
// field the tool never sets, and the step fails at fire time on a path that
// looked validated. This catches the case that matters most — a field invented
// while writing the schema, which no run will ever produce.
//
// A schema derived with PayloadSchemaOf takes its fields from a struct's json
// tags, so the field names are in the file that declares the struct rather than
// in the file that declares the tool. Both are searched, and the struct is found
// by reading the type name out of the OutputSchema call, so the two stay bound to
// each other rather than the search widening to the whole package.
//
// The declarations themselves are cut out of the text before it is searched.
// Without that, a field's own declaration answers the search, so every schema
// written out by hand passes by containing itself — which this check did, proven by
// declaring a field no run produces and watching it pass.
//
// "content" is exempt: it is the envelope's, not the payload's, and never
// appears as a key in the tool's own source.
func TestNoSchemaDeclaresAFieldThatAppearsNowhere(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	sources := map[string]string{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		sources[f] = string(b)
	}

	re := regexp.MustCompile(`func \(\w+ \*(\w+)\) OutputSchema\(\)`)
	fileOf := map[string]string{}
	for f, src := range sources {
		for _, m := range re.FindAllStringSubmatch(src, -1) {
			fileOf[m[1]] = f
		}
	}

	for name, tool := range schemaFixtures() {
		schema := toolapi.GetOutputSchema(tool)
		if schema == nil {
			continue
		}
		typ := reflect.TypeOf(tool)
		for typ.Kind() == reflect.Ptr {
			typ = typ.Elem()
		}
		src, known := sources[fileOf[typ.Name()]]
		if !known {
			t.Errorf("%s declares a schema in a file this test cannot find", name)
			continue
		}
		where := fileOf[typ.Name()]

		// The derivation is read from the file as written, and the search happens
		// over a copy with the declarations cut out. Reading the stripped copy for
		// the derivation finds nothing, because the call being looked for is inside
		// the body that was cut.
		declaring := src
		src = withoutSchemaDeclarations(src)

		// A derived schema names its payload struct in the call. The struct may be
		// declared elsewhere in the package, so that file is searched as well.
		if payloadType, ok := derivedPayloadType(declaring, typ.Name()); ok {
			decl := regexp.MustCompile(`type ` + payloadType + ` struct`)
			found := false
			for f, other := range sources {
				if decl.MatchString(other) {
					src += "\n" + other
					where += " or " + f
					found = true
					break
				}
			}
			if !found {
				t.Errorf("%s derives its schema from %s and no file in this package declares that type",
					name, payloadType)
				continue
			}
		}

		payload := toolapi.PayloadSchema(schema)
		if payload == nil {
			continue
		}
		var declared struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(payload, &declared); err != nil {
			t.Errorf("%s: payload schema is not an object: %v", name, err)
			continue
		}
		for field := range declared.Properties {
			if field == "content" {
				continue
			}
			// A json tag may carry options after the name — `json:"path,omitempty"` —
			// so the name alone in quotes is not there to find. Both forms count.
			if !strings.Contains(src, `"`+field+`"`) && !strings.Contains(src, `"`+field+`,`) {
				t.Errorf("%s declares %q and nothing in %s ever sets it — "+
					"a planner told about this field would reference something no run produces",
					name, field, where)
			}
		}
	}
}

// derivedPayloadType reads the struct name out of a tool's OutputSchema.
//
// Matches PayloadSchemaOf(xData{}) and PayloadSchemaOfWithNote(xData{}, "..."),
// and only inside the named type's own OutputSchema method, so one file holding
// several tools does not attribute one tool's payload to another.
func derivedPayloadType(src, typeName string) (string, bool) {
	start := regexp.MustCompile(`func \(\w+ \*` + typeName + `\) OutputSchema\(\)`).FindStringIndex(src)
	if start == nil {
		return "", false
	}
	body := src[start[1]:]
	if end := strings.Index(body, "\n}"); end > 0 {
		body = body[:end]
	}
	m := regexp.MustCompile(`PayloadSchemaOf(?:WithNote)?\((\w+)\{\}`).FindStringSubmatch(body)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// withoutSchemaDeclarations returns a file's text with every OutputSchema body
// removed, so a search for a field name cannot be answered by the declaration that
// named it.
func withoutSchemaDeclarations(src string) string {
	re := regexp.MustCompile(`func \(\w+ \*\w+\) OutputSchema\(\)`)
	var b strings.Builder
	rest := src
	for {
		loc := re.FindStringIndex(rest)
		if loc == nil {
			b.WriteString(rest)
			return b.String()
		}
		b.WriteString(rest[:loc[0]])
		rest = rest[loc[1]:]
		if end := strings.Index(rest, "\n}"); end >= 0 {
			rest = rest[end+2:]
		} else {
			return b.String()
		}
	}
}
