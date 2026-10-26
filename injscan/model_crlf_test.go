package injscan

import (
	"bytes"
	"io/fs"
	"testing"
	"testing/fstest"
)

// A checkout on Windows turned the word lists' line endings into CRLF, which
// left a \r on every word: nothing matched, every line scored as harmless and
// the screen flagged nothing, with no error. A converted copy must score as the
// files do.
func TestAModelWithCRLFLineEndingsScoresTheSame(t *testing.T) {
	want, err := Default()
	if err != nil {
		t.Fatalf("built-in model: %v", err)
	}
	sub, err := fs.Sub(builtIn, "model")
	if err != nil {
		t.Fatal(err)
	}
	crlf := fstest.MapFS{}
	err = fs.WalkDir(sub, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(sub, p)
		if err != nil {
			return err
		}
		if p == "ft_words.txt" || p == "tg_words.txt" || p == "meta.json" {
			b = bytes.ReplaceAll(b, []byte("\n"), []byte("\r\n"))
		}
		crlf[p] = &fstest.MapFile{Data: b}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := LoadFS(crlf)
	if err != nil {
		t.Fatalf("CRLF copy: %v", err)
	}
	if g, w := got.Score(selfCheckLine, Log), want.Score(selfCheckLine, Log); g.Score != w.Score || g.Level == None {
		t.Errorf("CRLF copy scores the self-check line %.3f (%q), the files %.3f (%q)", g.Score, g.Level, w.Score, w.Level)
	}
}
