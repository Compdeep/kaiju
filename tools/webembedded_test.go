package tools

import (
	"strings"
	"testing"
)

// A page whose markup is a shell. Measured on two real ones: 20KB and 47KB of
// HTML holding 48 characters of text between them, both returning empty while
// carrying their own state in a script tag the whole time.
func TestEmbeddedJSON_ReadsAPageThatRendersItself(t *testing.T) {
	html := `<html><body><div id="__next"></div>
	  <script id="__NEXT_DATA__" type="application/json">
	    {"props":{"txs":[{"sig":"5xY","slot":123},{"sig":"9aB","slot":124}]}}
	  </script></body></html>`

	got := embeddedJSON(html, 32)
	if !strings.Contains(got, `"sig":"5xY"`) {
		t.Errorf("the page's own data was not recovered: %q", got)
	}
}

// An empty shell is not data. Solscan ships __NEXT_DATA__ holding 190 bytes of
// build metadata and fetches its transactions afterwards over the network —
// returning that would claim to have read a page that has not loaded.
func TestEmbeddedJSON_AnEmptyShellIsNotContent(t *testing.T) {
	html := `<script id="__NEXT_DATA__" type="application/json">{"buildId":"x","isFallback":false}</script>`
	if got := embeddedJSON(html, 512); got != "" {
		t.Errorf("a build-metadata shell was returned as content: %q", got)
	}
}

// Largest first: every cap between here and a model cuts from the end, so the
// block worth reading has to come before the boilerplate.
func TestEmbeddedJSON_TheSubstantialBlockLeads(t *testing.T) {
	big := `{"rows":[` + strings.Repeat(`{"a":"bbbbbbbbbb"},`, 60) + `{"a":"end"}]}`
	html := `<script type="application/ld+json">{"@type":"WebSite","name":"a site here"}</script>
	         <script type="application/json">` + big + `</script>`
	got := embeddedJSON(html, 32)
	if !strings.HasPrefix(got, `{"rows"`) {
		t.Errorf("the small descriptive block led:\n%.120s", got)
	}
}

// Only what parses. A script tag holding something that merely looks like JSON
// is not data, and a fragment downstream is worse than nothing.
func TestEmbeddedJSON_RejectsWhatDoesNotParse(t *testing.T) {
	html := `<script type="application/json">{"a": 1, "b": </script>`
	if got := embeddedJSON(html, 8); got != "" {
		t.Errorf("a truncated block was returned: %q", got)
	}
}

// A page with nothing embedded returns nothing, which leaves the caller exactly
// where it was.
func TestEmbeddedJSON_OrdinaryPageIsUntouched(t *testing.T) {
	html := `<html><body><article><p>Real readable prose.</p></article>
	         <script>var x = 1;</script></body></html>`
	if got := embeddedJSON(html, 32); got != "" {
		t.Errorf("an ordinary page produced %q", got)
	}
}

// Nuxt writes its state as an assignment rather than a JSON script tag.
func TestEmbeddedJSON_ReadsNuxtState(t *testing.T) {
	html := `<script>window.__NUXT__={"data":{"items":[1,2,3],"note":"a longer value here to pass the floor"}};</script>`
	if got := embeddedJSON(html, 32); !strings.Contains(got, `"items"`) {
		t.Errorf("nuxt state was not read: %q", got)
	}
}
