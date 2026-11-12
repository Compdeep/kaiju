package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// The tool's surface, and the parsers behind it.
//
// A search provider's HTML is not a contract: it changes without notice, and
// when it does the parser returns nothing rather than failing, so a run simply
// finds no sources and nobody is told why. These pin the shapes each parser was
// written against, which is what turns that silence into a failing test.

// ── the tool's surface ────────────────────────────────────────────────────

func TestWebSearchImplementsTheToolInterfaces(t *testing.T) {
	w := NewWebSearch()
	var _ toolapi.Tool = w
	var _ toolapi.Outputter = w
}

func TestWebSearchDefaults(t *testing.T) {
	w := NewWebSearchWithConfig(SearchConfig{})
	if w.provider != "ddg" {
		t.Errorf("default provider without a key = %q, want ddg", w.provider)
	}
	if w.delay != 200*time.Millisecond {
		t.Errorf("default delay = %v, want 200ms", w.delay)
	}
}

func TestWebSearchConfigOverrides(t *testing.T) {
	w := NewWebSearchWithConfig(SearchConfig{Provider: "ddg", DelaySec: 1.5})
	if w.provider != "ddg" {
		t.Errorf("provider = %q, want ddg", w.provider)
	}
	if w.delay != 1500*time.Millisecond {
		t.Errorf("delay = %v, want 1.5s", w.delay)
	}
}

// A supplied client is the one used, which is what lets an embedding
// application prove its own chain from a search result offline.
func TestWebSearchUsesASuppliedClient(t *testing.T) {
	mine := &http.Client{Timeout: 3 * time.Second}
	w := NewWebSearchWithConfig(SearchConfig{HTTPClient: mine})
	if w.client != mine {
		t.Error("the supplied client was not used, so the tool can only be " +
			"exercised against the live web")
	}
	if def := NewWebSearch(); def.client == nil {
		t.Error("no client at all when none was supplied")
	}
}

func TestWebSearchRejectsAnEmptyQuery(t *testing.T) {
	w := NewWebSearch()
	_, err := w.Execute(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("expected an error on an empty query")
	}
	if !strings.Contains(err.Error(), "query is required") {
		t.Errorf("the error should say the query is missing, got: %v", err)
	}
}

// The payload schema names the three fields a plan writes templates against,
// and marks the url as a handle the run can still follow.
func TestWebSearchOutputSchemaDescribesAResult(t *testing.T) {
	var envelope map[string]any
	if err := json.Unmarshal(NewWebSearch().OutputSchema(), &envelope); err != nil {
		t.Fatalf("OutputSchema is not valid JSON: %v", err)
	}
	payload := toolapi.PayloadSchema(NewWebSearch().OutputSchema())
	if payload == nil {
		t.Fatal("the schema declares an envelope with no payload")
	}
	var data map[string]any
	if err := json.Unmarshal(payload, &data); err != nil {
		t.Fatalf("the payload schema is not valid JSON: %v", err)
	}
	props, _ := data["properties"].(map[string]any)
	results, _ := props["results"].(map[string]any)
	items, _ := results["items"].(map[string]any)
	itemProps, ok := items["properties"].(map[string]any)
	if !ok {
		t.Fatal("results.items.properties is missing, so no template can name a field")
	}
	for _, k := range []string{"title", "url", "snippet"} {
		if _, ok := itemProps[k]; !ok {
			t.Errorf("results.items.properties has no %q, which breaks "+
				"${step.N.results.0.%s}", k, k)
		}
	}
	url, _ := itemProps["url"].(map[string]any)
	if _, marked := url["x-reference"]; !marked {
		t.Error("the url is not marked as a handle, so a url this tool " +
			"surfaced and nothing read is not reported to the answering stage")
	}
}

func TestWebSearchRequiresAQueryParameter(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal(NewWebSearch().Parameters(), &schema); err != nil {
		t.Fatalf("Parameters is not valid JSON: %v", err)
	}
	required, _ := schema["required"].([]any)
	for _, r := range required {
		if s, _ := r.(string); s == "query" {
			return
		}
	}
	t.Error("query is not in required")
}

// ── parseDDGResults ───────────────────────────────────────────────────────

func TestParseDDGResultsExtractsResultLinks(t *testing.T) {
	html := `
	<div>
		<a class="result__a" href="https://valid.example.com/page">Example Title</a>
		<a class="result__snippet" href="#">A snippet text describing it.</a>
	</div>`
	results := parseDDGResults(html, 5)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d: %#v", len(results), results)
	}
	if !strings.HasPrefix(results[0].URL, "http") {
		t.Errorf("the URL did not resolve to http: %q", results[0].URL)
	}
}

// DDG returns links in redirect form: //duckduckgo.com/l/?uddg=<encoded URL>.
// Left unresolved, every result URL points at DDG rather than at the source.
func TestParseDDGResultsResolvesTheRedirect(t *testing.T) {
	html := `<a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Freal.example.com%2Fpath">Title</a>`
	results := parseDDGResults(html, 5)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].URL != "https://real.example.com/path" {
		t.Errorf("URL = %q, want the target the redirect carries", results[0].URL)
	}
}

func TestResolveDDGURLPassesThroughAnHTTPLink(t *testing.T) {
	if got := resolveDDGURL("https://example.com/page"); got != "https://example.com/page" {
		t.Errorf("got %q", got)
	}
}

func TestResolveDDGURLAddsHTTPSToAProtocolRelativeLink(t *testing.T) {
	if got := resolveDDGURL("//example.com/page"); got != "https://example.com/page" {
		t.Errorf("got %q", got)
	}
}

func TestResolveDDGURLRejectsARelativeLink(t *testing.T) {
	if got := resolveDDGURL("/some/path"); got != "" {
		t.Errorf("expected empty for a non-absolute link, got %q", got)
	}
}

// ── stripTags ────────────────────────────────────────────

func TestStripTagsRemovesTagsAndKeepsText(t *testing.T) {
	if got := stripTags("<b>hello</b> <i>world</i>"); got != "hello world" {
		t.Errorf("got %q", got)
	}
}

func TestStripTagsDecodesAmp(t *testing.T) {
	if got := stripTags("a &amp; b"); got != "a & b" {
		t.Errorf("got %q", got)
	}
}
