package agent

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// A goroutine that panics must not take the process with it, and must keep
// whatever promise it made before it goes.
//
// These are source-reading rather than behavioural because the thing being
// asserted is that a guard EXISTS at each spawn point. A behavioural test can
// only prove the one path it drives; an unguarded goroutine kills the test
// binary rather than failing, so the failure would be a crash with no name on
// it.

// spawned are the functions this package runs on their own goroutine, and the
// file each lives in. A panic in any of them is a panic in a goroutine nothing
// else can recover from.
var spawned = []struct{ file, fn, guard string }{
	{"dispatcher.go", "fireNode", "guardNodeCompletion"},
	{"reflection.go", "fireReflection", "guardNodeCompletion"},
	{"rca.go", "fireHolmes", "guardNodeCompletion"},
	{"observer.go", "fireObserver", "guardNodeCompletion"},
	{"microplanner.go", "fireMicroPlanner", "guardNodeCompletion"},
	{"scheduler.go", "oneshotRetry", "guardNodeCompletion"},
	{"job_scheduler.go", "runJob", "recover()"},
	{"agent.go", "dagFanOut", "guardLoop"},
	{"mod_heartbeat.go", "run", "guardLoop"},
	{"kernel.go", "Run", "guardLoop"},
}

func TestEveryGoroutineIsGuarded(t *testing.T) {
	for _, s := range spawned {
		body := funcBody(t, readSource(t, s.file), s.fn)
		if !strings.Contains(body, s.guard) {
			t.Errorf("%s runs on its own goroutine with no guard: a panic there ends "+
				"the process, because recover() reaches nothing outside its own "+
				"goroutine", s.fn)
		}
	}
}

// The scheduler counts a node as in flight until exactly one completion arrives
// for it. guardNodeCompletion sends one unconditionally, which is only correct
// while nothing runs after a send — otherwise a panic after one would send a
// second, the in-flight count would fall twice, and the run would conclude with
// work still going.
//
// So this holds the property the guard relies on: in every function that owes a
// completion, each send is the last thing that happens on its path.
func TestNodeCompletionsAreTerminal(t *testing.T) {
	// Any send on the completion channel, not only a literal nodeCompletion.
	// The remote and local paths now build theirs through one function and send
	// what it returns, and a pattern that insisted on the type name would have
	// stopped seeing every send in fireNode while still passing.
	send := regexp.MustCompile(`^\s*(ch|completionCh) <- `)
	for _, s := range spawned {
		if s.guard != "guardNodeCompletion" {
			continue
		}
		lines := strings.Split(funcBody(t, readSource(t, s.file), s.fn), "\n")
		for i, line := range lines {
			if !send.MatchString(line) {
				continue
			}
			// Walk to the end of the send — it may be a multi-line literal or a
			// multi-line call — then require a return, or the end of the
			// function. Parentheses count as well as braces: a send of what a
			// function returns is written the second way, and counting only
			// braces read the continuation line as the next statement.
			j := i
			depthOf := func(l string) int {
				return strings.Count(l, "{") - strings.Count(l, "}") +
					strings.Count(l, "(") - strings.Count(l, ")")
			}
			for depth := depthOf(line); depth > 0 && j+1 < len(lines); {
				j++
				depth += depthOf(lines[j])
			}
			if j+1 >= len(lines) {
				continue // the send is the last statement in the function
			}
			if next := strings.TrimSpace(lines[j+1]); next != "return" && next != "}" {
				t.Errorf("%s:%d sends a completion and then keeps going (%q). A panic "+
					"after this point would send a second completion, the scheduler "+
					"would count the node done twice, and the run would conclude with "+
					"work still running", s.fn, i+1, next)
			}
		}
	}
}

// A guard that recovers and says nothing turns a crash into a mystery. Each one
// logs the stack, which names the panic site — the deferred function runs on top
// of the panicking frames, so they are still there to print.
func TestGuardsLogTheStack(t *testing.T) {
	src, err := os.ReadFile("panicguard.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, fn := range []string{"guardNodeCompletion", "guardLoop"} {
		if !strings.Contains(funcBody(t, string(src), fn), "debug.Stack()") {
			t.Errorf("%s recovers without printing the stack: the panic site is lost "+
				"and the bug it recovered from cannot be found", fn)
		}
	}
}
