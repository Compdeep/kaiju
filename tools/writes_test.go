package tools

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every file a tool writes in its own code goes through toolapi
// (agent/toolapi/write.go), which finds where the write really lands. A tool
// that writes with os directly is held by nothing but the path's text, which
// a symlink in the workspace gets past.
func TestEveryToolWritesThroughToolapi(t *testing.T) {
	direct := regexp.MustCompile(`\bos\.(Create|WriteFile|OpenFile|Mkdir|MkdirAll|Rename)\(`)
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			if direct.MatchString(line) {
				t.Errorf("%s:%d writes with os directly; use toolapi.CreateForStep, WriteFileForStep, MkdirAllForStep or the In forms: %s",
					f, i+1, strings.TrimSpace(line))
			}
		}
	}
}
