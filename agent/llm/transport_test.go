package llm

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// Supplying a transport changes where the request goes and nothing else.
//
// This exists because the client sends through one http.Client and three
// methods use it, so a transport that reached only Complete would look correct
// until the first streaming call. What is asserted is that the request the
// transport receives is an ordinary HTTP request the standard library built,
// which is the whole reason for replacing the transport rather than the client.

// recorder answers every request with a fixed completion and remembers the last
// request it was given.
type recorder struct {
	got  *http.Request
	body string
}

func (r *recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	r.got = req
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		r.body = string(b)
	}
	return &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"choices":[{"message":{"role":"assistant","content":"answered"}}]}`)),
		Request: req,
	}, nil
}

func TestASuppliedTransportCarriesTheRequest(t *testing.T) {
	rec := &recorder{}
	c := NewClient("https://example.invalid/v1", "sekrit", "a-model").Transport(rec)

	resp, err := c.Complete(context.Background(), &ChatRequest{
		Messages: []Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if rec.got == nil {
		t.Fatal("the transport was never called, so the client did not use it")
	}
	if len(resp.Choices) == 0 || resp.Choices[0].Message.Content != "answered" {
		t.Errorf("the reply came back as %+v", resp.Choices)
	}

	// An ordinary HTTP request, which is what lets a caller put anything that
	// speaks HTTP underneath it.
	if rec.got.Method != http.MethodPost {
		t.Errorf("method %q", rec.got.Method)
	}
	if !strings.Contains(rec.got.URL.String(), "example.invalid") {
		t.Errorf("the request went to %q", rec.got.URL)
	}
	if !strings.Contains(rec.body, "hello") {
		t.Errorf("the body the transport saw was %q", rec.body)
	}
}

// The credential is set by the client, so whatever sits underneath does not have
// to know how this provider authenticates.
func TestTheTransportSeesTheAuthorisationTheClientSet(t *testing.T) {
	rec := &recorder{}
	c := NewClient("https://example.invalid/v1", "sekrit", "a-model").Transport(rec)
	if _, err := c.Complete(context.Background(), &ChatRequest{
		Messages: []Message{{Role: "user", Content: "hello"}},
	}); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if got := rec.got.Header.Get("Authorization"); got == "" {
		t.Error("no credential reached the transport")
	}
}

// Nil leaves the client exactly as it was, which is what every existing caller
// passes without knowing it.
func TestNilTransportLeavesTheClientAlone(t *testing.T) {
	c := NewClient("https://example.invalid/v1", "k", "m")
	before := c.http.Transport
	if got := c.Transport(nil); got != c {
		t.Error("Transport did not return the client, so it cannot be chained")
	}
	if c.http.Transport != before {
		t.Error("passing nil replaced the transport")
	}
}

// Streaming goes through the same transport. This is the one that would have
// been missed: Complete and CompleteStream build their requests separately.
func TestStreamingGoesThroughTheSameTransport(t *testing.T) {
	rec := &recorder{}
	c := NewClient("https://example.invalid/v1", "k", "m").Transport(rec)

	// The recorder answers with a non-streaming body; what is under test is that
	// the call reached the transport at all, not how the reply is parsed.
	_, _ = c.CompleteStream(context.Background(), &ChatRequest{
		Messages: []Message{{Role: "user", Content: "hello"}},
	}, func(chunk, kind string) {})

	if rec.got == nil {
		t.Fatal("a streaming call did not go through the supplied transport")
	}
}
