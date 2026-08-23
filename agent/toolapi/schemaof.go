package toolapi

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"
)

// Deriving a tool's declared payload from the type it returns.
//
// When a planner wires one step's output into another step's parameter, it reads
// the first tool's declared output to learn what fields exist. A tool that
// declares only the envelope — type, status, content, detail — tells it nothing
// about the tool's own result, so no field can be named and the whole text has to
// be quoted back instead.
//
// Writing those declarations by hand puts the same field list in two places: once
// in the struct the tool fills in, once in a JSON string beside it. The two agree
// on the day they are written. Bash is the example that prompted this: it fills a
// bashData with exit_code, stdout, stderr and command, and declared none of them.
//
// So the declaration is derived from the struct. One definition, read twice, and a
// field renamed in the struct is renamed in the declaration by the same edit.
//
// Descriptions cannot be read from a comment by reflection, so a field that needs
// one carries a `desc:"..."` tag. Everything else comes from the field's type and
// its json tag.

// maxSchemaDepth stops a type that contains itself. A payload nested ten deep is
// past the point where naming a field of it helps anyone, and a self-referential
// one would otherwise not terminate.
const maxSchemaDepth = 10

/*
 * PayloadSchemaOf renders a struct as the JSON schema of a tool's data payload.
 *
 * The result is the object to hand to EnvelopeSchema, so a tool declares its
 * payload as EnvelopeSchema(PayloadSchemaOf(myData{})).
 *
 * param: v - a struct, or a pointer to one. Its exported fields become properties.
 * return: the schema as JSON. For anything that is not a struct, an empty object,
 *         which declares a payload with no named fields rather than a broken
 *         schema.
 */
func PayloadSchemaOf(v any) string {
	t := reflect.TypeOf(v)
	for t != nil && t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t == nil || t.Kind() != reflect.Struct {
		return `{"type":"object"}`
	}
	return schemaOfType(t, 0)
}

/*
 * PayloadSchemaOfWithNote renders a struct and adds a description to the payload.
 *
 * A tool whose fields depend on which action was asked for needs to say so, since
 * the properties alone read as one fixed set that is always present.
 *
 * param: v - as PayloadSchemaOf.
 * param: note - the payload's description.
 * return: the schema as JSON.
 */
func PayloadSchemaOfWithNote(v any, note string) string {
	schema := PayloadSchemaOf(v)
	if note == "" {
		return schema
	}
	// Inserted after the opening brace, which every rendering here starts with.
	return `{"description":` + quote(note) + `,` + strings.TrimPrefix(schema, "{")
}

// schemaOfType renders one type. depth counts nesting, not recursion into fields
// of the same struct.
func schemaOfType(t reflect.Type, depth int) string {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if depth > maxSchemaDepth {
		return `{}`
	}

	// Two types that are structs but are not field bags.
	switch t {
	case reflect.TypeOf(time.Time{}):
		return `{"type":"string","format":"date-time"}`
	case reflect.TypeOf(json.RawMessage{}):
		return `{}` // arbitrary JSON, by definition undeclared
	}

	switch t.Kind() {
	case reflect.String:
		return `{"type":"string"}`
	case reflect.Bool:
		return `{"type":"boolean"}`
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return `{"type":"integer"}`
	case reflect.Float32, reflect.Float64:
		return `{"type":"number"}`
	case reflect.Slice, reflect.Array:
		// []byte is JSON's base64 string, not an array.
		if t.Elem().Kind() == reflect.Uint8 && t.Kind() == reflect.Slice {
			return `{"type":"string","contentEncoding":"base64"}`
		}
		return `{"type":"array","items":` + schemaOfType(t.Elem(), depth+1) + `}`
	case reflect.Map:
		// Keys are not known ahead of time, so the value type is all there is to
		// declare. A planner can read one out by name if it learned the name
		// somewhere else, which is the most a map allows.
		return `{"type":"object","additionalProperties":` + schemaOfType(t.Elem(), depth+1) + `}`
	case reflect.Interface:
		return `{}`
	case reflect.Struct:
		return schemaOfStruct(t, depth)
	default:
		// Channels, functions and the like never survive json.Marshal, so a tool
		// returning one has a different problem than its declaration.
		return `{}`
	}
}

// schemaOfStruct renders a struct's exported fields as properties.
func schemaOfStruct(t reflect.Type, depth int) string {
	props := map[string]string{}
	collectFields(t, depth, props)
	if len(props) == 0 {
		return `{"type":"object"}`
	}

	// Sorted, so the same struct renders to the same bytes every time. Map order
	// would otherwise make the declaration differ between runs of one binary,
	// which shows up as a spurious difference wherever schemas are compared.
	names := make([]string, 0, len(props))
	for name := range props {
		names = append(names, name)
	}
	sort.Strings(names)

	var b strings.Builder
	b.WriteString(`{"type":"object","properties":{`)
	for i, name := range names {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(quote(name) + ":" + props[name])
	}
	b.WriteString(`}}`)
	return b.String()
}

// collectFields walks one struct's fields into props, following embedded structs
// so their fields appear where JSON puts them: alongside the outer struct's own.
func collectFields(t reflect.Type, depth int, props map[string]string) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" && !f.Anonymous {
			continue // unexported, so json.Marshal skips it too
		}
		// An embedded field whose type is unexported is still promoted by
		// json.Marshal, which takes its exported fields. An embedded field of
		// unexported non-struct type is not, and falls out below.
		if f.PkgPath != "" && f.Anonymous {
			ft := f.Type
			for ft.Kind() == reflect.Ptr {
				ft = ft.Elem()
			}
			if ft.Kind() != reflect.Struct {
				continue
			}
		}

		name, omit := jsonFieldName(f)
		if omit {
			continue
		}

		// An embedded struct with no json name of its own has its fields promoted.
		if f.Anonymous && name == "" {
			ft := f.Type
			for ft.Kind() == reflect.Ptr {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				collectFields(ft, depth, props)
				continue
			}
		}
		if name == "" {
			name = f.Name
		}

		schema := schemaOfType(f.Type, depth+1)
		if desc := f.Tag.Get("desc"); desc != "" {
			schema = `{"description":` + quote(desc) + `,` + strings.TrimPrefix(schema, "{")
			// A description on an empty schema leaves a trailing brace pair to
			// clean up: {"description":"x",} is not JSON.
			schema = strings.Replace(schema, `,}`, `}`, 1)
		}
		props[name] = schema
	}
}

// jsonFieldName reports the name json.Marshal will use, and whether the field is
// skipped entirely.
func jsonFieldName(f reflect.StructField) (name string, omit bool) {
	tag := f.Tag.Get("json")
	if tag == "-" {
		return "", true
	}
	if tag == "" {
		if f.Anonymous {
			return "", false // promote, decided by the caller
		}
		return f.Name, false
	}
	parts := strings.Split(tag, ",")
	if parts[0] == "" {
		if f.Anonymous {
			return "", false
		}
		return f.Name, false
	}
	return parts[0], false
}

// quote renders a string as JSON, so a description containing a quote or a
// newline does not produce a schema that will not parse.
func quote(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}

/*
 * DeclaredPayloadFields lists the field names a schema declares for the payload.
 *
 * Used by the check that a tool's returned payload carries no field its
 * declaration does not name.
 *
 * param: schema - a tool's OutputSchema.
 * return: the field names, and false if the schema declares no payload object.
 */
func DeclaredPayloadFields(schema json.RawMessage) ([]string, bool) {
	// PayloadSchema decides what the payload's declaration is, including the case
	// this used to miss: a schema written by hand with the fields at the top level
	// and no envelope around them. That form is read as the payload everywhere else
	// in the engine, so a tool using it looked to this function like a tool
	// declaring nothing — which is what it reported, wrongly, about tools that
	// declare their fields perfectly well.
	payload := PayloadSchema(schema)
	if payload == nil {
		return nil, false
	}

	// Whether the payload was found inside an envelope or is the whole schema.
	//
	// This matters for the four framing names below. In a schema written flat, a
	// "content" property is the envelope's and belongs to no tool. In a payload
	// found under data, a property called content or status is the tool's own —
	// web_fetch's payload carries the page's text and the HTTP status line, and
	// stripping those made a tool that declares them correctly read as one that
	// returns undeclared fields.
	var root map[string]json.RawMessage
	wrapped := false
	if json.Unmarshal(schema, &root) == nil {
		if _, marked := root["x-envelope"]; marked {
			wrapped = true
		} else if props, ok := root["properties"]; ok {
			var p map[string]json.RawMessage
			if json.Unmarshal(props, &p) == nil {
				_, wrapped = p["data"]
			}
		}
	}

	var declared struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Items      struct {
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"items"`
	}
	if err := json.Unmarshal(payload, &declared); err != nil {
		return nil, false
	}
	props := declared.Properties
	if len(props) == 0 {
		props = declared.Items.Properties
	}
	// The envelope's own four are not the tool's, and a hand-written flat schema
	// often lists content beside its own fields. Inside a data object they are the
	// tool's, so nothing is removed there.
	if !wrapped {
		for _, framing := range []string{"type", "status", "content", "detail"} {
			delete(props, framing)
		}
	}
	if len(props) == 0 {
		return nil, false
	}
	out := make([]string, 0, len(props))
	for name := range props {
		out = append(out, name)
	}
	sort.Strings(out)
	return out, true
}

// schemaMustParse is for the tests here and in packages declaring payloads: a
// schema that does not parse declares nothing, and does so silently.
func schemaMustParse(schema string) error {
	var v any
	if err := json.Unmarshal([]byte(schema), &v); err != nil {
		return fmt.Errorf("schema does not parse: %w", err)
	}
	return nil
}
