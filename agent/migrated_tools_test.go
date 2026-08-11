package agent

import (
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// The first tools to emit the envelope: web_search, web_fetch, sysinfo
// and list_processes.
//
// Until one did, every phase of this migration was inert. These check the
// output those tools now produce survives the trip into the graph — that the
// scheduler recognises it, the payload stays addressable at the paths plans
// already use, and absence renders as absence.
//
// They use the shapes the tools emit rather than calling them, because three of
// the four reach the network or the host. The tools' own tests cover that they
// emit these shapes.

// TestMigratedToolOutputBecomesATypedBody: what these tools return is
// recognised as an envelope and lands as a typed body.
func TestMigratedToolOutputBecomesATypedBody(t *testing.T) {
	cases := []struct {
		tool string
		raw  string
	}{
		{"web_search", toolapi.ToolOK("search", "", map[string]any{
			"query": "credential dumping", "results": []any{map[string]any{"url": "https://example.test/a"}}}).JSON()},
		{"web_search (nothing found)", toolapi.ToolEmpty("search", "no results for this query").JSON()},
		{"web_fetch", toolapi.ToolOK("page", "the page text", map[string]any{
			"status": "HTTP 200 200 OK", "content": "the page text", "format": "markdown"}).JSON()},
		{"web_fetch (404)", toolapi.ToolFail("page", "HTTP 404 404 Not Found", nil).JSON()},
		{"sysinfo", toolapi.ToolOK("kv", `{"hostname":"web-1"}`, map[string]any{
			"hostname": "web-1", "os": "linux", "cpus": 2}).JSON()},
		{"list_processes", toolapi.ToolOK("listing", "USER PID\nroot 1", map[string]any{"count": 1}).JSON()},
		{"list_processes (no match)", toolapi.ToolEmpty("listing", "no processes matching nginx").JSON()},
	}

	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			msg, ok := toolapi.ParseToolMessage(tc.raw)
			if !ok {
				t.Fatalf("the scheduler would not recognise this as an envelope: %s", tc.raw)
			}

			g := NewGraph()
			id := g.AddNode(&Node{Type: NodeTool, ToolName: "t"})
			g.SetBody(id, toolMessageBody{msg: msg})

			n := g.Get(id)
			if _, isTyped := n.Body.(toolMessageBody); !isTyped {
				t.Fatalf("node body is %T, want toolMessageBody", n.Body)
			}
			if n.Result == "" {
				t.Error("Result is empty — nothing would be persisted or shown")
			}
		})
	}
}

// TestMigratedToolPathsStillResolve: the template references a plan would
// already contain must keep working, since the payload kept its keys.
func TestMigratedToolPathsStillResolve(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		path string
		want any
	}{
		{"web_search result url", toolapi.ToolOK("search", "", map[string]any{
			"results": []any{map[string]any{"url": "https://example.test/a"}}}).JSON(),
			"results.0.url", "https://example.test/a"},
		{"web_fetch content", toolapi.ToolOK("page", "the page text", map[string]any{
			"content": "the page text", "format": "markdown"}).JSON(),
			"content", "the page text"},
		{"sysinfo hostname", toolapi.ToolOK("kv", "", map[string]any{"hostname": "web-1"}).JSON(),
			"hostname", "web-1"},
		{"list_processes count", toolapi.ToolOK("listing", "USER PID", map[string]any{"count": 3}).JSON(),
			"count", float64(3)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg, ok := toolapi.ParseToolMessage(tc.raw)
			if !ok {
				t.Fatalf("not an envelope: %s", tc.raw)
			}
			body := toolMessageBody{msg: msg}

			got, found := body.Field(tc.path)
			if !found {
				t.Fatalf("${node.<id>.%s} no longer resolves — plans referencing it would break", tc.path)
			}
			if got != tc.want {
				t.Errorf("%s = %v (%T), want %v (%T)", tc.path, got, got, tc.want, tc.want)
			}

			// And through a reference with no path, which is how the
			// dispatcher reaches the same value.
			g := NewGraph()
			id := g.AddNode(&Node{Type: NodeTool, ToolName: "t"})
			g.SetBody(id, body)
			n := &Node{ID: "reader", Params: map[string]any{"x": "${node." + id + "}"}}
			if err := substituteTemplates(n, g); err != nil {
				t.Fatalf("substituteTemplates: %v", err)
			}
			if _, isMap := n.Params["x"].(map[string]any); !isMap {
				t.Errorf("a reference with no path gave %T, want the payload map", n.Params["x"])
			}
		})
	}
}

// TestAbsenceReadsAsAbsence is the point of the whole migration: a tool that
// found nothing must not hand a later stage something that reads like output.
func TestAbsenceReadsAsAbsence(t *testing.T) {
	for _, tc := range []struct{ name, raw, want string }{
		{"search found nothing", toolapi.ToolEmpty("search", "no results for this query").JSON(),
			"(no search: no results for this query)"},
		{"page had no content", toolapi.ToolEmpty("page", "no readable content at that URL").JSON(),
			"(no page: no readable content at that URL)"},
		{"page failed", toolapi.ToolFail("page", "HTTP 404 404 Not Found", nil).JSON(),
			"(page failed: HTTP 404 404 Not Found)"},
		{"no matching processes", toolapi.ToolEmpty("listing", "no processes matching nginx").JSON(),
			"(no listing: no processes matching nginx)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg, _ := toolapi.ParseToolMessage(tc.raw)
			got := toolMessageBody{msg: msg}.Evidence()
			if got != tc.want {
				t.Errorf("Evidence() = %q, want %q", got, tc.want)
			}
			if got == "" {
				t.Error("evidence is empty — the reader cannot tell nothing was found from nothing being said")
			}
		})
	}
}
