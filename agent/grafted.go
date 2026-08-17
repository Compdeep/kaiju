package agent

// The tool names this engine plants itself.
//
// Most steps come from a plan: the planner names a tool and the dispatcher looks
// it up. But the scheduler also creates steps nobody planned — an exec step
// after a compute run wrote code, a health check after a build, a revalidation
// after a fix — and it names the tool for those in Go, as a literal.
//
// So an application that registers its own set instead of calling
// tools.Register has to supply these names, under these names, or those steps
// fail at dispatch with "unknown tool". That failure arrives inside a run, on
// the machine the step was aimed at, long after startup. Nothing at
// registration time says a name is missing, because nothing knows what the
// scheduler will need until it needs it.
//
// The list was written in a comment in tools/register.go and nowhere an
// application could read it, so the only way to cover it was to know it.

// GraftedToolNames returns the tool names the scheduler creates steps for by
// itself, so an application can check its registry covers them.
//
//	for _, name := range agent.GraftedToolNames() {
//	    if _, ok := reg.Get(name); !ok {
//	        return fmt.Errorf("the engine grafts %q and nothing registers it", name)
//	    }
//	}
//
// A name here is a requirement, not a suggestion: the engine will name it
// whether or not anything answers. Everything else the engine calls comes from
// a plan, and a planner only names tools the registry showed it.
func GraftedToolNames() []string {
	// Copied, so a caller cannot edit the engine's own answer.
	out := make([]string, len(graftedToolNames))
	copy(out, graftedToolNames)
	return out
}

// graftedToolNames is held to the scheduler's own source by grafted_test.go, so
// a graft added with a new name fails the build rather than waiting to fail in a
// run.
var graftedToolNames = []string{
	"bash",    // exec steps: run generated code, run a validator's check
	"service", // health checks after a build
}
