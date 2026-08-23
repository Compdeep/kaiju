package llm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Compdeep/kaiju/tokens"
)

// What an embedding costs, and who hears about it.
//
// Embed posts to a real endpoint and is paid for. It counted nothing, so a run
// that ranks tools against a request — embedding that request and every tool
// description — spent money that never reached tokens.Snapshot, which is what
// both dashboards read. An operator watching cost saw every chat call and none
// of these.
//
// It still does not notify the CallObserver, and that is deliberate rather than
// the same omission: the observer takes a chat request and a chat response, and
// an embedding is neither.

// spent reads the running total the dashboards read.
func spent() int64 {
	_, total := tokens.Snapshot()
	return total
}

// embedServer answers one embeddings call with the given JSON body.
func embedServer(t *testing.T, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestEmbedCountsWhatItSpends(t *testing.T) {
	url := embedServer(t, `{"data":[{"index":0,"embedding":[0.1,0.2]}],"usage":{"prompt_tokens":37,"total_tokens":37}}`)

	before := spent()
	if _, err := NewClient(url, "k", "text-embedding-3-small").
		Embed(context.Background(), []string{"rank these tools"}); err != nil {
		t.Fatalf("Embed: %v", err)
	}

	if got := spent() - before; got != 37 {
		t.Errorf("the call reported 37 tokens and %d were counted", got)
	}
}

// An endpoint reporting no usage must not break the call, and must not invent a
// number. Some report nothing.
func TestEmbedWithNoReportedUsageStillReturnsVectors(t *testing.T) {
	url := embedServer(t, `{"data":[{"index":0,"embedding":[0.1,0.2]}]}`)

	before := spent()
	v, err := NewClient(url, "k", "text-embedding-3-small").
		Embed(context.Background(), []string{"x"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(v) != 1 || len(v[0]) != 2 {
		t.Errorf("vectors = %v, want one of length two", v)
	}
	if got := spent() - before; got != 0 {
		t.Errorf("%d tokens were counted for a call that reported none", got)
	}
}

// The observer is chat-shaped and an embedding is not, so Embed does not fire
// it — firing it would hand an application an empty conversation and an empty
// reply. Its comment says so; this holds the code to that.
func TestEmbedDoesNotNotifyTheCallObserver(t *testing.T) {
	url := embedServer(t, `{"data":[{"index":0,"embedding":[0.1]}],"usage":{"prompt_tokens":1,"total_tokens":1}}`)

	seen := 0
	SetCallObserver(func(context.Context, *ChatRequest, *ChatResponse, error) { seen++ })
	defer SetCallObserver(nil)

	if _, err := NewClient(url, "k", "m").Embed(context.Background(), []string{"x"}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if seen != 0 {
		t.Errorf("the observer was called %d times, with nothing to observe", seen)
	}
}
