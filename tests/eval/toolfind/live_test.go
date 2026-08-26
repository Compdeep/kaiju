// Measuring tool ranking against a real embedding endpoint.
//
// The unit tests in agent/toolfind cover what can be decided without a network:
// batching, storage, stability, what happens when a piece is missing. None of
// them can say whether the ranking finds the right tool, and the free
// measurement beside this one only covers the word half.
//
// So this costs money and runs only when asked:
//
//	TOOLFIND_EMBED_KEY=sk-... go test ./tests/eval/toolfind/ -v
//
// Optional: TOOLFIND_EMBED_ENDPOINT (default https://api.openai.com/v1),
// TOOLFIND_EMBED_MODEL (default text-embedding-3-small).
package toolfind_live

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Compdeep/kaiju/agent/llm"
	"github.com/Compdeep/kaiju/agent/toolfind"
)

// liveFloorAt40 is measured, not chosen — see the assertion below.
const liveFloorAt40 = 0.70

func liveClient(t *testing.T) *llm.Client {
	t.Helper()
	key := os.Getenv("TOOLFIND_EMBED_KEY")
	if key == "" {
		t.Skip("set TOOLFIND_EMBED_KEY to run the paid ranking measurement")
	}
	endpoint := os.Getenv("TOOLFIND_EMBED_ENDPOINT")
	if endpoint == "" {
		endpoint = "https://api.openai.com/v1"
	}
	model := os.Getenv("TOOLFIND_EMBED_MODEL")
	if model == "" {
		model = "text-embedding-3-small"
	}
	return llm.NewClient(endpoint, key, model)
}

// The measurement the design rests on: with both halves of the search running,
// how often is the tool a person asked for near the top of a thousand?
//
// The word half alone reaches 0.30 at forty, measured in the unit suite. If the
// number here is not far above that, the embedding half is not earning what it
// costs and the design is wrong.
func TestLiveRecall_OnAThousandTools(t *testing.T) {
	client := liveClient(t)
	dir := t.TempDir()

	reg := EnterpriseRegistry(t, 1000)
	ix, err := toolfind.Open(dir, reg, client)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	// Open embeds in the background. Wait for it, rather than measuring a
	// half-built index and blaming the ranking.
	waitForVectors(t, ix, 8*time.Minute)

	at := []int{5, 10, 20, 40}
	got := Recall(t, ix, at, true)
	for _, k := range at {
		t.Logf("recall@%-2d = %.2f  (%d tools, words and vectors)", k, got[k], len(reg.List()))
	}

	// A planner's tool index carries roughly forty tools of this size, so
	// recall at forty is the number that decides whether the right tool was in
	// front of the model at all. Measured at 0.80; the floor allows for an
	// embedding model that scores a little differently, and still sits far
	// above what words reach on their own.
	if got[40] < liveFloorAt40 {
		t.Errorf("recall@40 is %.2f with embeddings on, floor is %.2f; words alone reach %.2f",
			got[40], liveFloorAt40, lexicalFloorAt40)
	}
}

// A second Open over the same directory must make no embedding requests. This
// is what stops a thousand-tool deployment paying for a thousand embeddings at
// every restart, and it can only be checked against a real endpoint by timing
// it: a warm store answers in milliseconds.
func TestLiveVectorStore_SecondOpenIsFree(t *testing.T) {
	client := liveClient(t)
	dir := t.TempDir()
	reg := EnterpriseRegistry(t, 200)

	ix, err := toolfind.Open(dir, reg, client)
	if err != nil {
		t.Fatal(err)
	}
	waitForVectors(t, ix, 4*time.Minute)

	start := time.Now()
	ix2, err := toolfind.Open(dir, reg, client)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatal(err)
	}
	_ = ix2.Rank(context.Background(), "refund the customer")
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Errorf("a warm store took %s to open and rank — it re-embedded", elapsed)
	}
}

// waitForVectors blocks until every tool has a vector.
//
// An earlier version watched for the ranking to stop moving, and passed
// instantly: word ranking is stable from the first call, so "not moving" meant
// "the expensive half has not started" as readily as "it has finished". The
// measurement it then produced was the word-only one, wearing the paid one's
// name.
func waitForVectors(t *testing.T, ix toolfind.Index, limit time.Duration) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		indexed, total := ix.Ready()
		if indexed >= total && total > 0 {
			t.Logf("index warm: %d/%d tools", indexed, total)
			return
		}
		time.Sleep(2 * time.Second)
	}
	indexed, total := ix.Ready()
	t.Fatalf("only %d of %d tools embedded within %s", indexed, total, limit)
}
