package agent

import "testing"

// What a value looks like when it is put inside a string.
//
// A run asking about a directory read a path of
// map[name:kaiju-prompts size:4096 type:dir] and failed on "no such file or
// directory". That is Go's debug syntax for a map, written by fmt.Sprint, and
// it reads as though the memory was corrupted rather than as though an object
// was put where a filename goes. A million written the same way is 1e+06, which
// is a different number to every tool that receives it.
func TestAValuePutInsideAStringIsWrittenAsJSON(t *testing.T) {
	for _, c := range []struct {
		what string
		val  any
		want string
	}{
		{"the directory entry that reached file_read as a path",
			map[string]any{"name": "kaiju-prompts", "size": 4096.0, "type": "dir"},
			`{"name":"kaiju-prompts","size":4096,"type":"dir"}`},
		{"a list", []any{"a", "b"}, `["a","b"]`},
		{"a number big enough for Go to write in exponent form", float64(1000000), "1000000"},
		{"a number that was never in danger", float64(4096), "4096"},
		{"a fraction", 0.1, "0.1"},
		{"a truth", true, "true"},
		{"nothing", nil, "null"},
		// Unquoted: a path is /tmp/x, not "/tmp/x".
		{"text, which is already text", "already a string", "already a string"},
	} {
		if got := injectedString(c.val); got != c.want {
			t.Errorf("%s: got %q, want %q", c.what, got, c.want)
		}
	}
}

// End to end through the substitution, in the shape that failed: an object
// resolved out of a dependency and dropped into the middle of a path.
func TestAnObjectEmbeddedInAPathArrivesAsSomethingReadable(t *testing.T) {
	g, dep := graphWithDep(t, `{"entries":[{"name":"kaiju-prompts","size":4096,"type":"dir"}]}`, StateResolved)
	n := &Node{Params: map[string]any{"path": "/tmp/${node." + dep + ".entries.0}"}}
	n.ID = g.AddNode(n)

	if err := substituteTemplates(n, g, nil); err != nil {
		t.Fatalf("substituteTemplates: %v", err)
	}
	want := `/tmp/{"name":"kaiju-prompts","size":4096,"type":"dir"}`
	if got := n.Params["path"]; got != want {
		t.Errorf("path = %v, want %v", got, want)
	}
}

// A placeholder that is the whole value still keeps its type — this changes
// only what happens inside a larger string.
func TestAWholeValuePlaceholderStillKeepsItsType(t *testing.T) {
	g, dep := graphWithDep(t, `{"entries":[{"name":"kaiju-prompts"}]}`, StateResolved)
	n := &Node{Params: map[string]any{"entry": "${node." + dep + ".entries.0}"}}
	n.ID = g.AddNode(n)

	if err := substituteTemplates(n, g, nil); err != nil {
		t.Fatalf("substituteTemplates: %v", err)
	}
	entry, ok := n.Params["entry"].(map[string]any)
	if !ok {
		t.Fatalf("entry = %#v, want the object itself, not a rendering of it", n.Params["entry"])
	}
	if entry["name"] != "kaiju-prompts" {
		t.Errorf("entry.name = %v", entry["name"])
	}
}
