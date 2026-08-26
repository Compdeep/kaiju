package toolfind

import "math"

// Ranking tools by the words they share with an objective.
//
// BM25 rather than a count of matches: a term that occurs in half the registry
// says almost nothing about which half a run wants, and one that occurs in two
// tools says nearly everything. Enterprise registries are full of the first
// kind — "record", "list", "id" — and a plain count ranks by how wordy a tool's
// description is.
//
// Written here rather than taken from the database because this package indexes
// a registry, not a table: a few hundred short documents held in memory, no
// schema to migrate, and no second copy of the corpus to keep in step.

const (
	bm25K1 = 1.2  // term-frequency saturation
	bm25B  = 0.75 // how much document length is normalised

	// maxDocumentShare is how much of the registry a term may appear in before
	// it is ignored.
	//
	// A term in nearly every document separates nothing, and BM25 scores it
	// near zero rather than at zero — which was enough to do real damage. Tools
	// in one registry shared a parameter described as "how many to return", so
	// the words "how" and "many" in an objective matched all thousand of them
	// at a score of about 0.0005 each. Every tool was then scored, every score
	// was the same, and the order among them fell through to the tie-break,
	// which is the order the registry lists names in — alphabetical. Tools in
	// systems beginning with "a" won every objective and tools beginning with
	// "w" lost every one.
	//
	// Half is deliberately generous. A term shared by half a registry cannot
	// pick a tool out of it, whatever its score says.
	maxDocumentShare = 0.5
)

// lexicon is the inverted index over tool documents.
type lexicon struct {
	postings map[string]map[string]int // term → tool name → count
	length   map[string]int            // tool name → term count
	avgLen   float64
}

// newLexicon indexes each tool's document under its name.
func newLexicon(docs map[string]string) *lexicon {
	lx := &lexicon{
		postings: make(map[string]map[string]int, len(docs)*8),
		length:   make(map[string]int, len(docs)),
	}
	total := 0
	for name, doc := range docs {
		terms := tokenize(doc)
		lx.length[name] = len(terms)
		total += len(terms)
		for _, t := range terms {
			p := lx.postings[t]
			if p == nil {
				p = make(map[string]int, 4)
				lx.postings[t] = p
			}
			p[name]++
		}
	}
	if len(docs) > 0 {
		lx.avgLen = float64(total) / float64(len(docs))
	}
	return lx
}

// score returns the BM25 score of every tool that shares a term with the
// objective. Tools sharing nothing are absent rather than zero, so a caller can
// tell "no lexical opinion" from "scored zero".
func (lx *lexicon) score(objective string) map[string]float64 {
	if lx == nil || len(lx.length) == 0 {
		return nil
	}
	n := float64(len(lx.length))
	out := make(map[string]float64)
	for _, term := range tokenize(objective) {
		posting := lx.postings[term]
		if len(posting) == 0 {
			continue
		}
		df := float64(len(posting))
		if df > n*maxDocumentShare {
			continue // says nothing about which tool — see maxDocumentShare
		}
		idf := math.Log(1 + (n-df+0.5)/(df+0.5))
		for name, freq := range posting {
			tf := float64(freq)
			norm := 1 - bm25B + bm25B*float64(lx.length[name])/lx.avgLen
			out[name] += idf * (tf * (bm25K1 + 1)) / (tf + bm25K1*norm)
		}
	}
	return out
}
