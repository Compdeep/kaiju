package agent

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// gofmt aligns struct fields, so the number of spaces after a field name depends on
// the longest name in the literal: prose.go has `ToolChoice: "none"` and preflight.go
// `ToolChoice:  "required"`. Matching the literal string missed the aligned ones, and
// a planted violation went undetected until this was a pattern.
var (
	reOffersTools  = regexp.MustCompile(`\bTools:\s`)
	reForbidsTools = regexp.MustCompile(`\bToolChoice:\s+"none"`)
	reSetsChoice   = regexp.MustCompile(`\bToolChoice:\s`)
)

// Every LLM request either offers tools or forbids them. Nothing in between.
//
// Leaving tool_choice out is not the same as forbidding tool calls: it leaves the
// decision to the provider's default. Kimi-k3 on a no-tools lane answered a turn
// with its own tool-call tokens as prose — "Let me find the architecture document
// first." followed by <|open|>call tool="bash" index="1"<|sep|> and a find command.
// Nothing was watching for them, so they reached the user as the answer. The same
// leak on the compactor would have written them into the conversation summary that
// every later turn reads.
//
// This walks the source rather than the behaviour, because the failure is a field
// nobody wrote. A test of behaviour would need every provider to agree, and the
// point is that they do not.
func TestEveryLLMRequestSaysWhetherToolsAreAllowed(t *testing.T) {
	root := repoRootFor(t)
	literal := regexp.MustCompile(`llm\.ChatRequest\{`)

	var offersTools, forbidsTools, silent []string

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			if info != nil && info.IsDir() && (info.Name() == ".git" || info.Name() == "node_modules" || info.Name() == "web") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		lines := strings.Split(string(src), "\n")
		for i, l := range lines {
			if !literal.MatchString(l) {
				continue
			}
			// The composite literal, to its closing brace.
			depth, end := 0, i
			for j := i; j < len(lines) && j < i+40; j++ {
				depth += strings.Count(lines[j], "{") - strings.Count(lines[j], "}")
				if depth <= 0 && j > i {
					end = j
					break
				}
			}
			block := strings.Join(lines[i:end+1], "\n")
			where := strings.TrimPrefix(path, root+"/") + ":" + itoa(i+1)
			switch {
			case reOffersTools.MatchString(block):
				offersTools = append(offersTools, where)
			case reSetsChoice.MatchString(block):
				forbidsTools = append(forbidsTools, where)
			default:
				silent = append(silent, where)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(offersTools) == 0 || len(forbidsTools) == 0 {
		t.Fatalf("the walk found %d offering and %d forbidding — it is not reading the source",
			len(offersTools), len(forbidsTools))
	}
	t.Logf("%d requests offer tools, %d forbid them", len(offersTools), len(forbidsTools))

	for _, where := range silent {
		t.Errorf("%s declares no tools and does not set ToolChoice, so whether the model may "+
			"emit a tool call is the provider's default. Set ToolChoice: \"none\".", where)
	}
}

// And the other direction: forbidding tools on a request that offers them would
// disable real tool calling. This is the one that must never regress.
func TestForbiddingToolsNeverTouchesARequestThatOffersThem(t *testing.T) {
	root := repoRootFor(t)
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		lines := strings.Split(string(src), "\n")
		for i, l := range lines {
			if !strings.Contains(l, "llm.ChatRequest{") {
				continue
			}
			depth, end := 0, i
			for j := i; j < len(lines) && j < i+40; j++ {
				depth += strings.Count(lines[j], "{") - strings.Count(lines[j], "}")
				if depth <= 0 && j > i {
					end = j
					break
				}
			}
			block := strings.Join(lines[i:end+1], "\n")
			if reOffersTools.MatchString(block) && reForbidsTools.MatchString(block) {
				t.Errorf("%s:%d offers tools AND forbids calling them, so the tools can never be used",
					strings.TrimPrefix(path, root+"/"), i+1)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// repoRootFor walks up from the working directory to the module root.
func repoRootFor(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Skip("module root not found")
	return ""
}
