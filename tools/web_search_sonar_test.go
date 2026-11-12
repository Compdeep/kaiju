package tools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// Search through Perplexity Sonar on OpenRouter, the default when the config
// has an OpenRouter key: Startpage and DuckDuckGo answer a server with bot
// checks, so scraping them found nothing for most queries.

// fakeWeb answers every host from one test server: OpenRouter's chat
// completions, DuckDuckGo's html search, and HEAD probes of result URLs.
type fakeWeb struct {
	srv       *httptest.Server
	mu        sync.Mutex
	sonarBody map[string]any
	sonarAuth string
	sonarCode int
	ddgCalls  int
}

func newFakeWeb(t *testing.T) *fakeWeb {
	f := &fakeWeb{sonarCode: http.StatusOK}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.Method == http.MethodHead:
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/api/v1/chat/completions":
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &f.sonarBody)
			f.sonarAuth = r.Header.Get("Authorization")
			if f.sonarCode != http.StatusOK {
				w.WriteHeader(f.sonarCode)
				return
			}
			cite := func(u, title string) map[string]any {
				return map[string]any{"type": "url_citation", "url_citation": map[string]any{"url": u, "title": title}}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{
				"role": "assistant", "content": "answer [1][2]",
				"annotations": []any{
					cite("https://ssd-api.jpl.nasa.gov/doc/horizons.html", "Horizons API"),
					cite("https://ssd-api.jpl.nasa.gov/doc/horizons.html", "Horizons API"),
					cite("https://ssd.jpl.nasa.gov/horizons/", "Horizons System"),
					cite("https://ssd.jpl.nasa.gov/planets/orbits.html", "Planetary orbits"),
				},
			}}}})
		case r.URL.Path == "/html/":
			f.ddgCalls++
			_, _ = io.WriteString(w, `<a class="result__a" href="https://ddg.example.com/page">DDG Result</a>`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// client sends every request, whatever its host, to the fake server.
func (f *fakeWeb) client() *http.Client {
	target, _ := url.Parse(f.srv.URL)
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r = r.Clone(r.Context())
		r.URL.Scheme, r.URL.Host = target.Scheme, target.Host
		return http.DefaultTransport.RoundTrip(r)
	})}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func sonarSearch(f *fakeWeb, provider, key string) *WebSearch {
	return NewWebSearchWithConfig(SearchConfig{
		Provider: provider, APIKey: key, Endpoint: "https://openrouter.ai/api/v1",
		DelaySec: 0.001, HTTPClient: f.client(),
	})
}

func resultURLs(t *testing.T, w *WebSearch, params map[string]any) []string {
	t.Helper()
	msg, err := w.ExecuteTyped(context.Background(), params)
	if err != nil {
		t.Fatal(err)
	}
	var data struct {
		Results []searchResult `json:"results"`
	}
	b, _ := json.Marshal(msg.Data)
	_ = json.Unmarshal(b, &data)
	var out []string
	for _, r := range data.Results {
		out = append(out, r.URL)
	}
	return out
}

func TestSonarSourcesBecomeResults(t *testing.T) {
	f := newFakeWeb(t)
	w := sonarSearch(f, "", "sk-or-test")
	got := resultURLs(t, w, map[string]any{"query": "JPL Horizons API", "max_results": 2.0, "recency_days": 5.0})
	want := []string{"https://ssd-api.jpl.nasa.gov/doc/horizons.html", "https://ssd.jpl.nasa.gov/horizons/"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("results = %v, want %v (deduplicated, cut to max_results)", got, want)
	}
	if f.sonarBody["model"] != sonarModel {
		t.Errorf("model = %v, want %s", f.sonarBody["model"], sonarModel)
	}
	if f.sonarAuth != "Bearer sk-or-test" {
		t.Errorf("Authorization = %q", f.sonarAuth)
	}
	if f.sonarBody["search_recency_filter"] != "week" {
		t.Errorf("search_recency_filter = %v, want week for recency_days 5", f.sonarBody["search_recency_filter"])
	}
	if f.ddgCalls != 0 {
		t.Errorf("DuckDuckGo was called %d times on a Sonar success", f.ddgCalls)
	}
}

func TestSonarFailureFallsBackToDuckDuckGo(t *testing.T) {
	f := newFakeWeb(t)
	f.sonarCode = http.StatusBadGateway
	w := sonarSearch(f, "sonar", "sk-or-test")
	got := resultURLs(t, w, map[string]any{"query": "anything"})
	if len(got) != 1 || got[0] != "https://ddg.example.com/page" {
		t.Fatalf("results = %v, want the DuckDuckGo result", got)
	}
}

func TestSearchProviderFollowsTheKey(t *testing.T) {
	for _, c := range []struct{ provider, key, want string }{
		{"", "sk-or-x", "sonar"},
		{"", "", "ddg"},
		{"startpage+ddg", "sk-or-x", "sonar"}, // retired value
		{"startpage", "", "ddg"},              // retired value
		{"sonar", "", "ddg"},                  // no key, nothing to call
		{"ddg", "sk-or-x", "ddg"},             // asked for by name
	} {
		w := NewWebSearchWithConfig(SearchConfig{Provider: c.provider, APIKey: c.key})
		if w.provider != c.want {
			t.Errorf("provider %q with key %v: got %q, want %q", c.provider, c.key != "", w.provider, c.want)
		}
	}
}

func TestEmptyScrapedSearchSaysAKeyIsNeeded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `<html>anomaly</html>`)
	}))
	defer srv.Close()
	target, _ := url.Parse(srv.URL)
	cl := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r = r.Clone(r.Context())
		r.URL.Scheme, r.URL.Host = target.Scheme, target.Host
		return http.DefaultTransport.RoundTrip(r)
	})}
	w := NewWebSearchWithConfig(SearchConfig{DelaySec: 0.001, HTTPClient: cl})
	msg, err := w.ExecuteTyped(context.Background(), map[string]any{"query": "x"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg.Detail, "OpenRouter key") {
		t.Errorf("the empty result should say an OpenRouter key gives reliable search, got %q", msg.Detail)
	}
}
