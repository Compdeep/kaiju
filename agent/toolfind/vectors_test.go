package toolfind

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// fakeEmbedder records what it was asked to embed and answers deterministically,
// so batching and re-use can be checked without a network.
type fakeEmbedder struct {
	mu     sync.Mutex
	calls  [][]string
	fail   bool
	vecFor func(string) []float64
}

func (f *fakeEmbedder) Model() string { return "fake-embed-1" }

func (f *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float64, error) {
	f.mu.Lock()
	f.calls = append(f.calls, append([]string(nil), texts...))
	fail := f.fail
	f.mu.Unlock()
	if fail {
		return nil, fmt.Errorf("embedding is down")
	}
	out := make([][]float64, len(texts))
	for i, t := range texts {
		if f.vecFor != nil {
			out[i] = f.vecFor(t)
			continue
		}
		out[i] = []float64{float64(len(t)), 1}
	}
	return out, nil
}

func (f *fakeEmbedder) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeEmbedder) embedded() map[string]bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]bool{}
	for _, c := range f.calls {
		for _, t := range c {
			out[t] = true
		}
	}
	return out
}

func docsOfSize(n int) map[string]string {
	out := make(map[string]string, n)
	for i := 0; i < n; i++ {
		out[fmt.Sprintf("tool_%03d", i)] = fmt.Sprintf("document number %d", i)
	}
	return out
}

// A registry larger than one request has to arrive as several. Providers cap
// both the number of inputs and the body size, and a single request for a
// thousand tools is rejected outright — which would leave every tool ranking on
// words with nothing saying why.
func TestEmbedMissing_SplitsIntoBatches(t *testing.T) {
	docs := docsOfSize(embedBatch*2 + 5)
	f := &fakeEmbedder{}

	got := embedMissing(context.Background(), f, docs, nil)

	if len(got) != len(docs) {
		t.Errorf("embedded %d of %d tools", len(got), len(docs))
	}
	if f.callCount() != 3 {
		t.Errorf("want 3 requests for %d docs at a batch of %d, got %d",
			len(docs), embedBatch, f.callCount())
	}
	for _, c := range f.calls {
		if len(c) > embedBatch {
			t.Errorf("a request carried %d documents, over the batch of %d", len(c), embedBatch)
		}
	}
}

// Only what is missing is paid for. This is the whole reason a vector is stored
// against a hash of its document.
func TestEmbedMissing_SkipsWhatIsAlreadyHeld(t *testing.T) {
	docs := docsOfSize(4)
	have := map[string][]float32{"tool_000": {1, 2}, "tool_001": {3, 4}}
	f := &fakeEmbedder{}

	got := embedMissing(context.Background(), f, docs, have)

	if len(got) != 2 {
		t.Errorf("embedded %d tools, want the 2 that were missing", len(got))
	}
	sent := f.embedded()
	for _, held := range []string{"document number 0", "document number 1"} {
		if sent[held] {
			t.Errorf("re-embedded a document already held: %q", held)
		}
	}
}

// A batch that fails costs its tools their vectors and nothing else. Ranking on
// words is worse than ranking on both; failing the run is worse than either.
func TestEmbedMissing_AFailedBatchDoesNotStopTheRest(t *testing.T) {
	f := &fakeEmbedder{fail: true}
	got := embedMissing(context.Background(), f, docsOfSize(3), nil)
	if len(got) != 0 {
		t.Errorf("want no vectors from a failing endpoint, got %d", len(got))
	}
}

// Nothing missing means nothing sent — a boot with a warm store makes no
// embedding requests at all.
func TestEmbedMissing_QuietWhenEverythingIsHeld(t *testing.T) {
	docs := docsOfSize(3)
	have := map[string][]float32{}
	for n := range docs {
		have[n] = []float32{1}
	}
	f := &fakeEmbedder{}
	if got := embedMissing(context.Background(), f, docs, have); got != nil {
		t.Errorf("want no work, got %d vectors", len(got))
	}
	if f.callCount() != 0 {
		t.Errorf("made %d requests with nothing to embed", f.callCount())
	}
}

// With vectors present the ranking uses them: a tool whose description shares no
// word with the objective still reaches the top when its vector is close.
func TestRank_UsesVectorsWhenWordsDoNotMatch(t *testing.T) {
	reg := toolapi.NewRegistry()
	tools := []*fakeTool{
		{name: "alpha", desc: "Kappa lambda mu."},
		{name: "beta", desc: "Nu xi omicron."},
	}
	for _, tl := range tools {
		if err := reg.RegisterWithSource(tl, tl.name); err != nil {
			t.Fatal(err)
		}
	}
	// "beta" is made the near neighbour of any objective; nothing shares a word.
	f := &fakeEmbedder{vecFor: func(text string) []float64 {
		if contains(text, "beta") {
			return []float64{1, 0}
		}
		if contains(text, "alpha") {
			return []float64{0, 1}
		}
		return []float64{0.99, 0.14} // the objective, close to beta
	}}

	ix := &index{dir: t.TempDir(), reg: reg, embed: f, model: f.Model()}
	ix.sync(context.Background())
	ix.fillVectors() // synchronously, so the test does not race the background pass

	got := ix.Rank(context.Background(), "sigma tau upsilon")
	if got[0] != "beta" {
		t.Errorf("the vector ranking was not used: %v", got)
	}
}

// A second boot over an unchanged registry embeds nothing. Without this a
// thousand-tool deployment pays for a thousand embeddings at every restart.
func TestOpen_SecondBootReusesTheStoredVectors(t *testing.T) {
	dir := t.TempDir()
	reg := registryOf(t, sample...)

	first := &fakeEmbedder{}
	ix1 := &index{dir: dir, reg: reg, embed: first, model: first.Model()}
	ix1.sync(context.Background())
	ix1.fillVectors()
	if first.callCount() == 0 {
		t.Fatal("the first boot embedded nothing")
	}

	second := &fakeEmbedder{}
	ix2 := &index{dir: dir, reg: reg, embed: second, model: second.Model()}
	ix2.sync(context.Background())
	ix2.fillVectors()
	if second.callCount() != 0 {
		t.Errorf("the second boot re-embedded %d batches over an unchanged registry", second.callCount())
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

// The rule the ranking rests on: a tool that has a vector is ranked by it, and
// a tool that does not is ranked by words. Both produce a term of the same
// shape, so neither group is pushed below the other merely for being in it —
// which is what a half-embedded registry needs while the first pass runs.
func TestRank_EachToolRankedByTheSearchThatSeesIt(t *testing.T) {
	reg := toolapi.NewRegistry()
	for _, tl := range []*fakeTool{
		{name: "alpha_vectored", desc: "Kappa lambda mu."},          // no shared words, will hold a vector
		{name: "beta_worded", desc: "sigma tau upsilon paperwork."}, // shares words, will hold none
		{name: "gamma_neither", desc: "Nu xi omicron."},
	} {
		if err := reg.RegisterWithSource(tl, tl.name); err != nil {
			t.Fatal(err)
		}
	}
	f := &fakeEmbedder{vecFor: func(text string) []float64 {
		if contains(text, "alpha_vectored") {
			return []float64{1, 0}
		}
		return []float64{0.99, 0.14} // the objective, close to alpha
	}}

	ix := &index{dir: t.TempDir(), reg: reg, embed: f, model: f.Model()}
	ix.sync(context.Background())
	// Only alpha gets a vector; the other two are left for words to rank.
	ix.mu.Lock()
	ix.vectors = map[string][]float32{"alpha_vectored": {1, 0}}
	ix.mu.Unlock()

	got := ix.Rank(context.Background(), "sigma tau upsilon paperwork")
	if len(got) != 3 {
		t.Fatalf("want 3 tools, got %v", got)
	}
	// alpha is close to the objective's vector; beta is the best word match.
	// Both are ranked first by their own search, so both are ahead of gamma,
	// which neither search has an opinion about.
	if got[2] != "gamma_neither" {
		t.Errorf("a tool no search ranked was not last: %v", got)
	}
	if !containsAny(got[:2], "alpha_vectored") || !containsAny(got[:2], "beta_worded") {
		t.Errorf("want the vector match and the word match both ahead of the rest: %v", got)
	}
}

// When the objective itself cannot be embedded there is no semantic ranking at
// all, and every tool has to fall back to words — including the ones holding
// vectors, which would otherwise rank on nothing.
func TestRank_FallsBackToWordsWhenTheObjectiveCannotBeEmbedded(t *testing.T) {
	reg := registryOf(t, sample...)
	f := &fakeEmbedder{}
	ix := &index{dir: t.TempDir(), reg: reg, embed: f, model: f.Model()}
	ix.sync(context.Background())
	ix.fillVectors() // every tool now holds a vector

	f.mu.Lock()
	f.fail = true // the objective's own embedding now fails
	f.mu.Unlock()

	got := ix.Rank(context.Background(), "refund money to a customer")
	if got[0] != "stripe_refund_charge" {
		t.Errorf("a failed objective embedding lost the word ranking too: %v", got[:3])
	}
}

func containsAny(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}
