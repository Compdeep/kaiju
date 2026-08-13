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
	send := regexp.MustCompile(`^\s*(ch|completionCh) <- nodeCompletion`)
	for _, s := range spawned {
		if s.guard != "guardNodeCompletion" {
			continue
		}
		lines := strings.Split(funcBody(t, readSource(t, s.file), s.fn), "\n")
		for i, line := range lines {
			if !send.MatchString(line) {
				continue
			}
			// Walk to the end of the send — it may be a multi-line literal —
			// then require a return, or the end of the function.
			j := i
			for depth := strings.Count(line, "{") - strings.Count(line, "}"); depth > 0 && j+1 < len(lines); {
				j++
				depth += strings.Count(lines[j], "{") - strings.Count(lines[j], "}")
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
