// Package injscan scores text for planted instructions: text an attacker places in logs, events, command
// output or files to steer an AI agent that reads them. The model is built in (Default); see README.md.
package injscan

import "strings"

// Kind says what a text is; each kind has its own cut-offs because ordinary documentation is full of
// instructions and would otherwise drown the cut-offs for logs.
type Kind string

const (
	Log  Kind = "log" // text a machine writes: logs, events, command output
	File Kind = "file"
)

// Level is how far a text's score goes past the cut-offs for its kind.
type Level string

const (
	None   Level = ""       // below the loose cut-off
	Loose  Level = "loose"  // above loose: send to the LLM check
	Strict Level = "strict" // above strict: flag outright (only when the model's meta.json sets outright)
)

// Mark is one word the tagger scores as part of planted text.
type Mark struct {
	Word  string
	Score float64
}

// Result is the verdict for one text.
type Result struct {
	Score       float64 // word model and tagger combined; the cut-offs apply to this
	WordScore   float64 // word model alone (ft5b_v2)
	TaggerScore float64 // tagger alone (tagger7)
	Level       Level
	Marks       []Mark // words scored above zero by the tagger, in order; filled when Level is not None
	Block       int    // number of consecutive lines scored together to reach Score (1 = the line alone)
}

// Model holds both scorers and the cut-offs.
type Model struct {
	words  *wordModel
	tagger *tagger
	meta   meta
}

// Cutoffs returns the cut-offs for a kind (log when the kind is unknown).
func (m *Model) Cutoffs(k Kind) Cutoffs {
	if c, ok := m.meta.Cutoffs[string(k)]; ok {
		return c
	}
	return m.meta.Cutoffs[string(Log)]
}

// Score scores one text against the single-line cut-offs.
func (m *Model) Score(text string, k Kind) Result {
	c := m.Cutoffs(k)
	return m.score(text, c.Loose, c.Strict)
}

func (m *Model) score(text string, loose, strict float64) Result {
	d := decode(text)
	ws := m.words.score(d)
	toks, wsc := m.tagger.wordScores(d)
	ts := best(wsc)
	r := Result{
		Score:       ts/m.meta.Combine.TaggerSpread + ws/m.meta.Combine.FTSpread,
		WordScore:   ws,
		TaggerScore: ts,
		Block:       1,
	}
	r.Level = levelOf(r.Score, loose, strict)
	if r.Level != None {
		for i, s := range wsc {
			if s > 0 && i < len(toks) {
				r.Marks = append(r.Marks, Mark{Word: toks[i], Score: s})
			}
		}
	}
	return r
}

func levelOf(s, loose, strict float64) Level {
	switch {
	case s > strict:
		return Strict
	case s > loose:
		return Loose
	}
	return None
}

func rank(l Level) int {
	switch l {
	case Strict:
		return 2
	case Loose:
		return 1
	}
	return 0
}

// ScoreLines scores consecutive lines from one source. Each line is scored alone against the single-line
// cut-offs, and inside every run of up to the model's block size of consecutive lines against the run
// cut-offs, so an instruction split across lines is still read whole. A run counts only when it scores
// higher than every one of its lines alone (reading them together reveals something none shows alone), and
// its result replaces a line's only when its level is higher.
func (m *Model) ScoreLines(lines []string, k Kind) []Result {
	c := m.Cutoffs(k)
	out := make([]Result, len(lines))
	single := make([]float64, len(lines))
	for i, l := range lines {
		out[i] = m.score(l, c.Loose, c.Strict)
		single[i] = out[i].Score
	}
	for size := 2; size <= m.meta.Block; size++ {
		for i := 0; i+size <= len(lines); i++ {
			r := m.score(strings.Join(lines[i:i+size], " "), c.RunLoose, c.RunStrict)
			r.Block = size
			if r.Score <= maxOf(single[i:i+size]) || r.Level == None {
				continue
			}
			for j := i; j < i+size; j++ {
				if rank(r.Level) > rank(out[j].Level) {
					out[j] = r
				}
			}
		}
	}
	return out
}

func maxOf(v []float64) float64 {
	m := v[0]
	for _, x := range v[1:] {
		if x > m {
			m = x
		}
	}
	return m
}
