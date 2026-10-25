package injscan

import (
	"math"
	"strings"
	"sync"
)

// tagger scores every word of a text for being part of planted text. Each word's vector is the mean of the
// fixed English vectors of its pieces (crawl-300d-2M, cut to 100 numbers, one byte each); a small layer
// reads [previous, own, next, own*next, previous*own, has-a-vector] and gives the word a score. A text
// scores its best run of three words. It copies train/tagger7.py.
//
// The layer is linear before its ReLU, so its input splits into parts that depend on one word (that word
// as previous, own or next word) or on one pair of neighbours (the two products). Each part is worked out
// once per distinct word or pair and cached: log lines repeat the same words, and ScoreLines scores every
// line several times.
type tagger struct {
	ids    map[string]int32
	vec    []int8    // rows of dim
	scale  []float32 // one per row
	dim    int
	hidden int

	// first layer with the feature scaling folded in, split by feature group; each dim x hidden, row-major
	wPrev, wOwn, wNext, wOwnNext, wPrevOwn []float32
	wKnown, bias                           []float32 // hidden each
	w2                                     []float32
	b2                                     float32

	mu    sync.Mutex
	words map[string]*wordPart
	pairs map[string]*pairPart
}

type wordPart struct {
	vec             []float32
	known           bool
	prev, own, next []float32 // the word's contribution as previous, own and next word (hidden each)
}

type pairPart struct {
	ownNext, prevOwn []float32 // (a*b) projected for a's own*next and for b's previous*own
}

const cacheLimit = 400000 // entries per cache before it is emptied

// fold turns the trained layer (mu, sd, w1 of features x hidden, b1) into the split, pre-scaled form.
func (t *tagger) fold(mu, sd, w1, b1 []float32) {
	D, H := t.dim, t.hidden
	t.bias = make([]float32, H)
	copy(t.bias, b1)
	groups := make([][]float32, 5)
	for g := range groups {
		groups[g] = make([]float32, D*H)
	}
	t.wKnown = make([]float32, H)
	for f := 0; f < 5*D+1; f++ {
		row := w1[f*H : (f+1)*H]
		inv := 1 / float64(sd[f])
		for j := range row {
			w := float32(float64(row[j]) * inv)
			t.bias[j] -= float32(float64(mu[f]) * inv * float64(row[j]))
			if f == 5*D {
				t.wKnown[j] = w
			} else {
				groups[f/D][(f%D)*H+j] = w
			}
		}
	}
	t.wPrev, t.wOwn, t.wNext, t.wOwnNext, t.wPrevOwn = groups[0], groups[1], groups[2], groups[3], groups[4]
	t.words = map[string]*wordPart{}
	t.pairs = map[string]*pairPart{}
}

// project returns v . w for a dim x hidden matrix w.
func (t *tagger) project(v []float32, w []float32) []float32 {
	out := make([]float32, t.hidden)
	for k, x := range v {
		if x == 0 {
			continue
		}
		row := w[k*t.hidden : (k+1)*t.hidden]
		for j := range out {
			out[j] += x * row[j]
		}
	}
	return out
}

// wordVector is the mean vector of the word's pieces that have one; ok is false when none has.
func (t *tagger) wordVector(word string) ([]float32, bool) {
	out := make([]float32, t.dim)
	n := 0
	for _, p := range strings.Split(tok(word), " ") {
		id, ok := t.ids[p]
		if !ok {
			id, ok = t.ids[strings.ToLower(p)]
		}
		if !ok {
			continue
		}
		row, s := t.vec[int(id)*t.dim:int(id+1)*t.dim], t.scale[id]
		for i, q := range row {
			out[i] += float32(q) * s
		}
		n++
	}
	if n == 0 {
		return out, false
	}
	for i := range out {
		out[i] /= float32(n)
	}
	return out, true
}

// word returns the cached parts of one word. Callers hold t.mu.
func (t *tagger) word(w string) *wordPart {
	if p, ok := t.words[w]; ok {
		return p
	}
	v, known := t.wordVector(w)
	p := &wordPart{vec: v, known: known, prev: t.project(v, t.wPrev), own: t.project(v, t.wOwn), next: t.project(v, t.wNext)}
	if len(t.words) >= cacheLimit {
		t.words = map[string]*wordPart{}
	}
	t.words[w] = p
	return p
}

// pair returns the cached product parts of two neighbouring words. Callers hold t.mu.
func (t *tagger) pair(a, b string, pa, pb *wordPart) *pairPart {
	key := a + "\x00" + b
	if p, ok := t.pairs[key]; ok {
		return p
	}
	e := make([]float32, t.dim)
	for i := range e {
		e[i] = pa.vec[i] * pb.vec[i]
	}
	p := &pairPart{ownNext: t.project(e, t.wOwnNext), prevOwn: t.project(e, t.wPrevOwn)}
	if len(t.pairs) >= cacheLimit {
		t.pairs = map[string]*pairPart{}
	}
	t.pairs[key] = p
	return p
}

// wordScores returns the decoded text's words (cut to 64 characters) and one score per word.
func (t *tagger) wordScores(decoded string) ([]string, []float64) {
	words := fields(decoded)
	for i, w := range words {
		words[i] = cut(w)
	}
	toks := words
	if len(toks) == 0 { // Python scores one empty token
		toks = []string{""}
	}
	t.mu.Lock()
	parts := make([]*wordPart, len(toks))
	for i, w := range toks {
		parts[i] = t.word(w)
	}
	pairs := make([]*pairPart, len(toks)) // pairs[i] joins toks[i] and toks[i+1]
	for i := 0; i+1 < len(toks); i++ {
		pairs[i] = t.pair(toks[i], toks[i+1], parts[i], parts[i+1])
	}
	t.mu.Unlock()

	h := make([]float32, t.hidden)
	scores := make([]float64, len(toks))
	for i, p := range parts {
		copy(h, t.bias)
		add(h, p.own)
		if p.known {
			add(h, t.wKnown)
		}
		if i > 0 {
			add(h, parts[i-1].prev)
			add(h, pairs[i-1].prevOwn)
		}
		if i+1 < len(parts) {
			add(h, parts[i+1].next)
			add(h, pairs[i].ownNext)
		}
		s := float64(t.b2)
		for j, x := range h {
			if x > 0 {
				s += float64(x) * float64(t.w2[j])
			}
		}
		scores[i] = s
	}
	return words, scores
}

func add(dst, src []float32) {
	for j := range dst {
		dst[j] += src[j]
	}
}

// best is the highest mean of three consecutive word scores (the highest word score below three words).
func best(scores []float64) float64 {
	m := math.Inf(-1)
	if len(scores) < 3 {
		for _, s := range scores {
			m = math.Max(m, s)
		}
		return m
	}
	for i := 0; i+3 <= len(scores); i++ {
		m = math.Max(m, (scores[i]+scores[i+1]+scores[i+2])/3)
	}
	return m
}
