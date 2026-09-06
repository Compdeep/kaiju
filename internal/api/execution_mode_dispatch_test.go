package api

import (
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent"
)

func handlerSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("api.go")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parser.ParseFile(token.NewFileSet(), "api.go", b, parser.SkipObjectResolution); err != nil {
		t.Fatalf("parsing api.go: %v", err)
	}
	return string(b)
}

// chat_mode is the older spelling of execution_mode "chat", and is read ONLY
// when execution_mode says nothing.
//
// Both fields describe the same thing, so a request carrying both must not be
// able to mean two things at once. The one that can say all three values wins,
// and the older one fills in behind it — which is what keeps a client written
// before the rename working without letting it contradict a newer one.
func TestTheOlderSpellingIsReadOnlyWhenTheCurrentOneIsSilent(t *testing.T) {
	src := handlerSource(t)
	i := strings.Index(src, `if req.ExecutionMode != "" {`)
	if i < 0 {
		t.Fatal("execution_mode is no longer read from the request")
	}
	j := strings.Index(src[i:], "chatLane :=")
	if j < 0 {
		t.Fatal("the resolved answer is gone; the handler must decide this once")
	}
	block := src[i : i+j]
	if !strings.Contains(block, "} else if req.ChatMode {") {
		t.Error("chat_mode is not the else branch, so it can be read alongside " +
			"execution_mode and the two can disagree about one turn")
	}
	if !strings.Contains(block, "agent.ExecutionChat") {
		t.Error("chat_mode no longer maps to the chat mode, so every client written " +
			"against it silently changes behaviour")
	}
}

// The handler decides once and reads that answer everywhere. It used to test
// req.ChatMode at four separate points; a fifth added later would be a fifth
// chance for one branch to disagree with the others.
func TestTheHandlerDoesNotReDeriveTheModeFromChatMode(t *testing.T) {
	src := handlerSource(t)
	i := strings.Index(src, "chatLane :=")
	if i < 0 {
		t.Fatal("the resolved answer is gone")
	}
	// Word-boundaried: req.ChatModel is a different field and shares the prefix.
	reread := regexp.MustCompile(`req\.ChatMode\b`).FindAllString(src[i:], -1)
	if len(reread) > 0 {
		t.Errorf("req.ChatMode is read %d time(s) after the mode is resolved; every "+
			"branch must use the one answer", len(reread))
	}
}

// The mode is resolved before anything reads it. It was written below its first
// reader once, which does not compile — but the ordering is what matters and a
// later edit could reintroduce it more subtly.
func TestTheModeIsResolvedBeforeItIsRead(t *testing.T) {
	src := handlerSource(t)
	resolved := strings.Index(src, "chatLane :=")
	firstRead := strings.Index(src, "if !chatLane {")
	if resolved < 0 || firstRead < 0 {
		t.Fatal("cannot find both the resolution and its first reader")
	}
	if resolved > firstRead {
		t.Error("the mode is read before it is resolved")
	}
}

// A value the parser refuses is refused here, not corrected. The three modes
// differ in whether tools run at all, so guessing is not a small error.
func TestAnUnknownModeIsRefusedByTheParser(t *testing.T) {
	for _, in := range []string{"autonomus", "Chat", "planner", " agent"} {
		if _, ok := agent.ParseExecutionMode(in); ok {
			t.Errorf("%q would be accepted by the handler", in)
		}
	}
	// And the retired names still work, which is what keeps existing clients up.
	for _, in := range []string{"interactive", "autonomous"} {
		if _, ok := agent.ParseExecutionMode(in); !ok {
			t.Errorf("%q is refused; clients written against it break", in)
		}
	}
}
