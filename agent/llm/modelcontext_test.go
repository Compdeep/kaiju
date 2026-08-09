package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// modelsServer answers GET /v1/models with the given JSON body, and records the
// path it was asked for.
func modelsServer(t *testing.T, body string) (*httptest.Server, *string) {
	t.Helper()
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	return srv, &path
}

func TestModelContext_VLLMReportsMaxModelLen(t *testing.T) {
	srv, path := modelsServer(t, `{"object":"list","data":[
		{"id":"qwen3-32b","object":"model","max_model_len":40960}]}`)
	defer srv.Close()

	got, err := NewClient(srv.URL, "", "qwen3-32b").ModelContext(context.Background(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 40960 {
		t.Fatalf("window = %d, want 40960", got)
	}
	if *path != "/v1/models" {
		t.Fatalf("asked %q, want /v1/models", *path)
	}
}

// OpenRouter's own API spells it context_length.
func TestModelContext_AcceptsContextLength(t *testing.T) {
	srv, _ := modelsServer(t, `{"data":[{"id":"a/b","context_length":131072}]}`)
	defer srv.Close()

	got, err := NewClient(srv.URL, "", "a/b").ModelContext(context.Background(), "")
	if err != nil || got != 131072 {
		t.Fatalf("got (%d, %v), want (131072, nil)", got, err)
	}
}

// The common case: the endpoint answers, and says nothing about size. Zero and
// no error — not a fault, and the caller proceeds without a window.
func TestModelContext_SilentEndpointIsZeroNotAnError(t *testing.T) {
	srv, _ := modelsServer(t, `{"data":[{"id":"gpt-4.1","object":"model","owned_by":"openai"}]}`)
	defer srv.Close()

	got, err := NewClient(srv.URL, "", "gpt-4.1").ModelContext(context.Background(), "")
	if err != nil {
		t.Fatalf("a listing with no size field is not an error: %v", err)
	}
	if got != 0 {
		t.Fatalf("window = %d, want 0", got)
	}
}

// A deployment often serves one model under a name that does not match what was
// configured — a filesystem path, or a shortened tag.
func TestModelContext_SingleEntryAnswersRegardlessOfName(t *testing.T) {
	srv, _ := modelsServer(t, `{"data":[{"id":"/models/Qwen3-32B-AWQ","max_model_len":32768}]}`)
	defer srv.Close()

	got, err := NewClient(srv.URL, "", "qwen3-32b").ModelContext(context.Background(), "")
	if err != nil || got != 32768 {
		t.Fatalf("got (%d, %v), want (32768, nil)", got, err)
	}
}

func TestModelContext_UnknownModelAmongManyIsReported(t *testing.T) {
	srv, _ := modelsServer(t, `{"data":[
		{"id":"a","max_model_len":1000},{"id":"b","max_model_len":2000}]}`)
	defer srv.Close()

	got, err := NewClient(srv.URL, "", "c").ModelContext(context.Background(), "")
	if got != 0 {
		t.Fatalf("window = %d, want 0", got)
	}
	if err == nil || !strings.Contains(err.Error(), "not among") {
		t.Fatalf("err = %v, want it to say the model was not offered", err)
	}
}

// A named model wins over the single-entry shortcut only when it matches; an
// explicit name still finds its own entry in a multi-model listing.
func TestModelContext_ExplicitNameSelectsItsEntry(t *testing.T) {
	srv, _ := modelsServer(t, `{"data":[
		{"id":"a","max_model_len":1000},{"id":"b","max_model_len":2000}]}`)
	defer srv.Close()

	got, err := NewClient(srv.URL, "", "a").ModelContext(context.Background(), "b")
	if err != nil || got != 2000 {
		t.Fatalf("got (%d, %v), want (2000, nil) — the argument overrides the client's model", got, err)
	}
}

func TestModelContext_EndpointFailureIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	got, err := NewClient(srv.URL, "", "x").ModelContext(context.Background(), "")
	if got != 0 || err == nil {
		t.Fatalf("got (%d, %v), want (0, an error)", got, err)
	}
}

// An endpoint already ending in /v1 must not get a second one.
func TestModelContext_EndpointWithV1SuffixIsNotDoubled(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = w.Write([]byte(`{"data":[{"id":"m","max_model_len":8}]}`))
	}))
	defer srv.Close()

	if _, err := NewClient(srv.URL+"/v1", "", "m").ModelContext(context.Background(), ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if path != "/v1/models" {
		t.Fatalf("asked %q, want /v1/models", path)
	}
}

func TestModelContext_NilClient(t *testing.T) {
	var c *Client
	if got, err := c.ModelContext(context.Background(), "m"); got != 0 || err == nil {
		t.Fatalf("got (%d, %v), want (0, an error)", got, err)
	}
}
