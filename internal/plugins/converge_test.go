package plugins

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Nothing wanted that is not already here means no build is attempted. This is
// every ordinary start, and the cost of it has to be a set compare.
//
// It is asserted by proving Converge succeeds where a build could not: PATH is
// emptied, so had it tried to run the compiler it would have failed.
func TestConvergeDoesNothingWhenTheSetMatches(t *testing.T) {
	t.Setenv("PATH", "")
	if err := Converge(nil); err != nil {
		t.Fatalf("Converge(nil) = %v, want it to do nothing", err)
	}
	Add(fakePlugin{"already_here"})
	if err := Converge([]string{"already_here"}); err != nil {
		t.Fatalf("Converge of a compiled-in plugin = %v, want it to do nothing", err)
	}
}

// The loop guard. A rebuild whose result still does not satisfy the request must
// stop, because the second attempt would produce the same binary and exec into the
// same gap, forever.
func TestConvergeStopsAfterOneRebuild(t *testing.T) {
	t.Setenv(convergedEnv, "1")
	t.Setenv("PATH", "")
	err := Converge([]string{"pdf"})
	if err == nil {
		t.Fatal("Converge returned nil; with the guard set and the plugin still absent it must report the gap")
	}
	if !strings.Contains(err.Error(), "pdf") {
		t.Fatalf("the error does not name what could not be satisfied: %v", err)
	}
	// It must say it is carrying on, not that it is about to try again.
	if !strings.Contains(err.Error(), "serving without") {
		t.Fatalf("the error does not say it is serving without it: %v", err)
	}
}

// Preconditions are reported before a build is attempted, and each names the thing
// that is missing — so an operator reads "no Go toolchain" rather than a compiler's
// output, and the agent can answer "I cannot install that here".
func TestCanBuildNamesTheMissingPrecondition(t *testing.T) {
	t.Setenv("PATH", "")
	err := CanBuild()
	if err == nil {
		t.Fatal("CanBuild succeeded with an empty PATH")
	}
	if !strings.Contains(err.Error(), "Go toolchain") {
		t.Fatalf("the error does not name the toolchain: %v", err)
	}
}

// A directory that is not kaiju's own module is not a source tree to rebuild from.
// Any go.mod would otherwise do, and the compiler would be handed a neighbouring
// module and its output shipped as kaiju.
func TestSourceTreeRejectsAForeignModule(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/not-kaiju\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	// The executable here is the test binary, which lives in a temp directory of
	// its own, so neither candidate holds this module.
	if got := SourceTree(); got == dir {
		t.Fatalf("SourceTree accepted a foreign module at %s", got)
	}
}

// Tags are what the build line is assembled from, so the order has to be stable:
// an unstable one would rebuild a binary that is already correct.
func TestTagsAreSortedAndDeduplicated(t *testing.T) {
	a, err := Tags([]string{"pdf", "webreader", "illustrator"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := Tags([]string{"illustrator", "pdf", "webreader"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(a, ",") != strings.Join(b, ",") {
		t.Fatalf("the same set gave %v and %v", a, b)
	}
	if len(a) != 2 {
		t.Fatalf("tags = %v, want two: one for pdf and one shared by the python plugins", a)
	}
}
