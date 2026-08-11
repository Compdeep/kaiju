package agent

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// No application's vocabulary in the engine.
//
// This package is a framework. An application embedding it has machines with
// roles, work with a discipline, tools with names of its own — and none of that
// belongs here, not in the code, not in a comment, not in a test fixture. A
// framework that knows one application's words has quietly become that
// application's library.
//
// It got in through comments explaining where a change came from, and through
// test data borrowing a real deployment's machine names. Both read as harmless
// and both are how a name becomes load-bearing: the next person to touch the
// file assumes the word means something here.
//
// If a word below is genuinely this engine's, the fix is to say so and take it
// off the list, not to work around the check.
var foreignWords = []string{
	"enbarr", "omamori", // the application this engine is being embedded in
	"queen", "pawn", "knight", // its machine roles
	"security_triage", // its skill cards
}

func TestNoApplicationVocabularyInTheEngine(t *testing.T) {
	// Whole words only. "spawn" contains one of these and means nothing of the
	// sort, and a check that cannot tell the difference gets switched off.
	var patterns []*regexp.Regexp
	for _, w := range foreignWords {
		patterns = append(patterns, regexp.MustCompile(`(?i)\b`+regexp.QuoteMeta(w)+`\b`))
	}

	root := "."
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		if strings.Contains(path, "no_domain_leak_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		for i, line := range strings.Split(string(data), "\n") {
			for w, re := range patterns {
				if !re.MatchString(line) {
					continue
				}
				t.Errorf("%s:%d names %q, which is an application's word and not this "+
					"engine's:\n    %s", path, i+1, foreignWords[w], strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}
