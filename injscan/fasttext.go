package injscan

import "strings"

// wordModel is the fastText model ft5b_v2 collapsed to one number per word and per word-pair bucket.
// fastText scores a window as (planted output - normal output) . (average of the window's input rows);
// that is a straight line, so each row reduces exactly to its own number and the window score is the
// average of those numbers (train/collapse7.py checks this against fastText itself).
type wordModel struct {
	ids     map[string]int32 // word -> row
	scalars []float32        // one per word row, then one per bucket row
	nwords  int
	bucket  uint64
	ngrams  int
}

// fnv32 is fastText's word hash: FNV-1a over the UTF-8 bytes, each byte taken as a signed char.
func fnv32(s string) uint32 {
	h := uint32(2166136261)
	for i := 0; i < len(s); i++ {
		h ^= uint32(int32(int8(s[i])))
		h *= 16777619
	}
	return h
}

// windowScore scores one window that has already been through clean and tok.
func (m *wordModel) windowScore(w string) float64 {
	toks := append(strings.Fields(w), "</s>")
	var sum float64
	n := 0
	hashes := make([]uint64, 0, len(toks))
	for _, t := range toks {
		if id, ok := m.ids[t]; ok {
			sum += float64(m.scalars[id])
			n++
		}
		// fastText keeps word hashes as int32; widening to 64 bits extends the sign.
		hashes = append(hashes, uint64(int64(int32(fnv32(t)))))
	}
	for i := range hashes {
		h := hashes[i]
		for j := i + 1; j < len(hashes) && j < i+m.ngrams; j++ {
			h = h*116049371 + hashes[j]
			sum += float64(m.scalars[m.nwords+int(h%m.bucket)])
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

// score is the highest window score of the decoded text.
func (m *wordModel) score(decoded string) float64 {
	best := -1e9
	for _, w := range windows(decoded) {
		if s := m.windowScore(tok(clean(w))); s > best {
			best = s
		}
	}
	return best
}
