package toolfind

import (
	"context"
	"encoding/gob"
	"log"
	"math"
	"os"
	"path/filepath"
)

// Holding what the embedding model said about each tool, and not asking twice.
//
// Embedding a registry is the one expensive thing this package does: a thousand
// tools is a thousand documents through a paid endpoint, and a boot that pays
// for it again is a boot that is slow and costs money for an answer nothing
// changed. So a vector is stored against a hash of the document it came from,
// and a tool is re-embedded when its description or its schema moves and not
// otherwise.

// embedBatch is how many documents go in one request. Providers cap both the
// number of inputs and the size of a request body, and a registry is embedded
// rarely enough that a conservative batch costs nothing.
const embedBatch = 96

// vectorFile is the name of the store inside the directory handed to Open.
const vectorFile = "toolvectors.gob"

// vector is one tool's embedding, and the document it was taken from.
type vector struct {
	Name   string
	Hash   string
	Values []float32
}

// vectorFileFormat is what is written to disk. The model is recorded because
// vectors from two models cannot be compared: changing the model has to
// invalidate everything, and silently mixing them would rank by nothing.
type vectorFileFormat struct {
	Model   string
	Vectors []vector
}

// loadVectors reads the store, keeping only the vectors still matching a
// document. A missing, unreadable or stale file is not an error — it means
// everything has to be embedded again, which is what a first run does anyway.
func loadVectors(dir, model string, want map[string]string) map[string][]float32 {
	if dir == "" {
		return nil
	}
	f, err := os.Open(filepath.Join(dir, vectorFile))
	if err != nil {
		return nil
	}
	defer f.Close()

	var stored vectorFileFormat
	if err := gob.NewDecoder(f).Decode(&stored); err != nil {
		log.Printf("[toolfind] vector store unreadable, re-embedding: %v", err)
		return nil
	}
	if stored.Model != model {
		log.Printf("[toolfind] vector store was built with %q, now %q — re-embedding",
			stored.Model, model)
		return nil
	}
	out := make(map[string][]float32, len(stored.Vectors))
	for _, v := range stored.Vectors {
		if want[v.Name] != v.Hash {
			continue
		}
		// Idempotent for a vector already stored at unit length, and what
		// converts one written by an older build that stored raw magnitudes.
		if normalize(v.Values) {
			out[v.Name] = v.Values
		}
	}
	return out
}

// saveVectors writes the store. A failure is logged and dropped: an index that
// cannot persist still ranks, it just pays again next boot.
func saveVectors(dir, model string, hashes map[string]string, vecs map[string][]float32) {
	if dir == "" || len(vecs) == 0 {
		return
	}
	out := vectorFileFormat{Model: model, Vectors: make([]vector, 0, len(vecs))}
	for name, values := range vecs {
		out.Vectors = append(out.Vectors, vector{Name: name, Hash: hashes[name], Values: values})
	}
	path := filepath.Join(dir, vectorFile)
	f, err := os.Create(path)
	if err != nil {
		log.Printf("[toolfind] cannot write %s: %v", path, err)
		return
	}
	defer f.Close()
	if err := gob.NewEncoder(f).Encode(out); err != nil {
		log.Printf("[toolfind] cannot encode %s: %v", path, err)
	}
}

// embedMissing embeds the documents that have no current vector, in batches.
//
// A batch that fails is logged and skipped rather than failing the whole run:
// the tools in it rank on words alone until the next attempt, which is worse
// than having them and far better than having nothing.
func embedMissing(ctx context.Context, client embedder, docs map[string]string,
	have map[string][]float32) map[string][]float32 {

	var todo []string
	for name := range docs {
		if _, ok := have[name]; !ok {
			todo = append(todo, name)
		}
	}
	if len(todo) == 0 {
		return nil
	}
	sortStrings(todo) // a stable order, so a partial failure is reproducible

	got := make(map[string][]float32, len(todo))
	for start := 0; start < len(todo); start += embedBatch {
		end := start + embedBatch
		if end > len(todo) {
			end = len(todo)
		}
		names := todo[start:end]
		texts := make([]string, len(names))
		for i, n := range names {
			texts[i] = docs[n]
		}
		vecs, err := client.Embed(ctx, texts)
		if err != nil {
			log.Printf("[toolfind] embedding %d tools failed, they rank on words only: %v",
				len(names), err)
			continue
		}
		for i, n := range names {
			if i >= len(vecs) {
				continue
			}
			v := toFloat32(vecs[i])
			if !normalize(v) {
				log.Printf("[toolfind] %s embedded to a zero vector, ranking it on words", n)
				continue
			}
			got[n] = v
		}
	}
	log.Printf("[toolfind] embedded %d of %d tools", len(got), len(todo))
	return got
}

func toFloat32(in []float64) []float32 {
	out := make([]float32, len(in))
	for i, v := range in {
		out[i] = float32(v)
	}
	return out
}

// normalize scales v to unit length, in place, and reports whether it could.
//
// Every vector is normalized once — when it arrives from the endpoint, and
// again when it is read back from the store, which costs nothing for one that
// already is. Similarity is then a dot product and nothing else: no magnitudes
// to recompute, no square roots, two thirds of the arithmetic gone from a loop
// that runs once per tool per search.
//
// It is done here rather than left to the comparison because a tool's vector
// does not change between searches and a query's changes once. Recomputing
// their lengths on every comparison is work whose answer was already known —
// which is affordable on a workstation and is not on a small ARM board, where
// this loop is the one piece of real arithmetic in the package.
func normalize(v []float32) bool {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		return false
	}
	inv := float32(1 / math.Sqrt(sum))
	for i := range v {
		v[i] *= inv
	}
	return true
}

// similarity compares two normalized vectors. Zero when they cannot be
// compared at all.
//
// Four running sums rather than one, and this is the whole reason the function
// looks like it does. A single accumulator makes every addition wait for the
// one before it, and the loop then runs at the latency of a floating-point add
// rather than its throughput — measured over a thousand vectors of 1536
// dimensions, a single-accumulator dot product was slower than the cosine it
// replaced, which recomputed both magnitudes on every call and did three times
// the arithmetic. It was faster because its three sums were independent and
// the processor could overlap them.
//
// Four independent chains cover the latency on both an x86 core and an ARM
// one. The sums are combined at the end; float32 throughout, because over unit
// vectors the error is orders of magnitude below the gap between two tools'
// scores.
func similarity(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var s0, s1, s2, s3 float32
	i := 0
	for ; i+4 <= len(a); i += 4 {
		s0 += a[i] * b[i]
		s1 += a[i+1] * b[i+1]
		s2 += a[i+2] * b[i+2]
		s3 += a[i+3] * b[i+3]
	}
	for ; i < len(a); i++ {
		s0 += a[i] * b[i]
	}
	return float64((s0 + s1) + (s2 + s3))
}
