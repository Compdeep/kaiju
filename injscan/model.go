package injscan

import (
	"embed"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io/fs"
	"math"
	"os"
	"strings"
	"sync"
)

// The model the package ships with, built into the program so it cannot go
// missing or fall out of step with the code.
//
//go:embed model
var builtIn embed.FS

var (
	defaultOnce  sync.Once
	defaultModel *Model
	defaultErr   error
)

// Default returns the built-in model, loaded once.
func Default() (*Model, error) {
	defaultOnce.Do(func() {
		sub, err := fs.Sub(builtIn, "model")
		if err != nil {
			defaultErr = err
			return
		}
		defaultModel, defaultErr = LoadFS(sub)
	})
	return defaultModel, defaultErr
}

// Model files, written by the training scripts into one folder:
//
//	meta.json        sizes, how the two scores combine, the cut-offs
//	ft_words.txt     word model words, one per line, row order
//	ft_scalars.f32   word model numbers: one per word row, then one per bucket row (little-endian float32)
//	tg_words.txt     tagger words, one per line, row order
//	tg_vec.i8        tagger word vectors, dim signed bytes per row
//	tg_scale.f32     one scale per tagger row (vector = bytes * scale)
//	tg_mlp.f32       mu, sd (5*dim+1 each), w1 (features x hidden), b1, w2 (hidden each), b2
type meta struct {
	Version string `json:"version"`
	FT      struct {
		Bucket     int `json:"bucket"`
		WordNgrams int `json:"wordNgrams"`
	} `json:"ft"`
	Tagger struct {
		Dim    int `json:"dim"`
		Hidden int `json:"hidden"`
	} `json:"tagger"`
	Combine struct {
		TaggerSpread float64 `json:"tagger_spread"`
		FTSpread     float64 `json:"ft_spread"`
	} `json:"combine"`
	Cutoffs map[string]Cutoffs `json:"cutoffs"` // "telemetry", "file"
	// Outright says whether a line above Strict is flagged without the LLM check. When false every line
	// above Loose goes to the LLM check and no line is given the Strict level.
	Outright bool `json:"outright"`
	Block    int  `json:"block"` // most consecutive lines scored together
}

// Cutoffs are the score levels for one kind of text: above Loose a line goes to the LLM check, above
// Strict it is flagged outright. Lines read together in a run score higher than lines alone, so runs have
// their own pair; all four are set so the stated share of real lines is let through in all.
type Cutoffs struct {
	Loose     float64 `json:"loose"`
	Strict    float64 `json:"strict"`
	RunLoose  float64 `json:"run_loose"`
	RunStrict float64 `json:"run_strict"`
}

func readWords(fsys fs.FS, path string) ([]string, error) {
	b, err := fs.ReadFile(fsys, path)
	if err != nil {
		return nil, err
	}
	s := strings.TrimSuffix(string(b), "\n")
	return strings.Split(s, "\n"), nil
}

func readF32(fsys fs.FS, path string) ([]float32, error) {
	b, err := fs.ReadFile(fsys, path)
	if err != nil {
		return nil, err
	}
	if len(b)%4 != 0 {
		return nil, fmt.Errorf("%s: size %d is not a multiple of 4", path, len(b))
	}
	out := make([]float32, len(b)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return out, nil
}

func index(words []string) map[string]int32 {
	m := make(map[string]int32, len(words))
	for i, w := range words {
		if _, dup := m[w]; !dup {
			m[w] = int32(i)
		}
	}
	return m
}

// Load reads a model folder.
func Load(dir string) (*Model, error) { return LoadFS(os.DirFS(dir)) }

// LoadFS reads a model from a file system holding the model files at its root.
func LoadFS(fsys fs.FS) (*Model, error) {
	var md meta
	b, err := fs.ReadFile(fsys, "meta.json")
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &md); err != nil {
		return nil, fmt.Errorf("meta.json: %w", err)
	}
	fw, err := readWords(fsys, "ft_words.txt")
	if err != nil {
		return nil, err
	}
	fsc, err := readF32(fsys, "ft_scalars.f32")
	if err != nil {
		return nil, err
	}
	if len(fsc) != len(fw)+md.FT.Bucket {
		return nil, fmt.Errorf("ft_scalars.f32 has %d numbers, want %d words + %d buckets", len(fsc), len(fw), md.FT.Bucket)
	}
	wm := &wordModel{ids: index(fw), scalars: fsc, nwords: len(fw), bucket: uint64(md.FT.Bucket), ngrams: md.FT.WordNgrams}

	tw, err := readWords(fsys, "tg_words.txt")
	if err != nil {
		return nil, err
	}
	vb, err := fs.ReadFile(fsys, "tg_vec.i8")
	if err != nil {
		return nil, err
	}
	sc, err := readF32(fsys, "tg_scale.f32")
	if err != nil {
		return nil, err
	}
	mlp, err := readF32(fsys, "tg_mlp.f32")
	if err != nil {
		return nil, err
	}
	D, H := md.Tagger.Dim, md.Tagger.Hidden
	F := 5*D + 1
	if len(vb) != len(tw)*D || len(sc) != len(tw) {
		return nil, fmt.Errorf("tagger tables: %d words, %d vector bytes, %d scales", len(tw), len(vb), len(sc))
	}
	if want := 2*F + F*H + 2*H + 1; len(mlp) != want {
		return nil, fmt.Errorf("tg_mlp.f32 has %d numbers, want %d", len(mlp), want)
	}
	vec := make([]int8, len(vb))
	for i, c := range vb {
		vec[i] = int8(c)
	}
	o := 0
	take := func(n int) []float32 { s := mlp[o : o+n]; o += n; return s }
	tg := &tagger{ids: index(tw), vec: vec, scale: sc, dim: D, hidden: H}
	mu, sd, w1, b1 := take(F), take(F), take(F*H), take(H)
	tg.w2 = take(H)
	tg.b2 = take(1)[0]
	tg.fold(mu, sd, w1, b1)

	for k, c := range md.Cutoffs {
		if c.RunLoose == 0 && c.RunStrict == 0 { // model folders from before run cut-offs: runs use the single-line pair
			c.RunLoose, c.RunStrict = c.Loose, c.Strict
		}
		if !md.Outright {
			c.Strict, c.RunStrict = math.Inf(1), math.Inf(1)
		}
		md.Cutoffs[k] = c
	}
	if md.Block < 1 {
		md.Block = 1
	}
	return &Model{words: wm, tagger: tg, meta: md}, nil
}
