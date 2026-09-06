package agent

// The two ways a run decides whether to enter the graph.
//
// Interactive routes first: one small model call reads the query and answers
// chat or agent, and a chat answer short-circuits before any planning. It is the
// default because most turns are not investigations, and planning one that did
// not need it costs a plan.
//
// Autonomous skips that call and always plans. It also marks the run unattended
// (see unattended.go), which withholds every tool declaring RequiresHuman — the
// two go together because a run nobody is watching is one nobody can be asked.
//
// These are strings on the wire and in config files, so they are typed nowhere
// and mistyped anywhere. ParseExecutionMode is the one place that decides what
// counts, and it is deliberately strict: the mode was previously read by
// comparing against "autonomous" and nothing else, so "autonomus" was silently
// interactive and the run that was meant to plan every turn quietly routed
// instead — with nothing said at any point.
const (
	// ExecutionInteractive routes each turn before planning it.
	ExecutionInteractive = "interactive"
	// ExecutionAutonomous plans every turn and treats the run as unwatched.
	ExecutionAutonomous = "autonomous"
	// ExecutionUnset inherits: a trigger takes the configured default, and the
	// configured default takes ExecutionInteractive.
	ExecutionUnset = ""
)

/*
 * ParseExecutionMode reports whether s names an execution mode.
 * desc: Empty is valid and means unset — a request that says nothing takes the
 *       node's configured mode, and a config that says nothing takes
 *       ExecutionInteractive. Anything else is a typo, and the caller is
 *       expected to refuse it rather than pick a mode on the writer's behalf:
 *       both choices are wrong here, because one plans work nobody asked for and
 *       the other skips work somebody did.
 * param: s - the value as written in a request or a config file.
 * return: the mode, and whether it was one.
 */
func ParseExecutionMode(s string) (string, bool) {
	switch s {
	case ExecutionUnset, ExecutionInteractive, ExecutionAutonomous:
		return s, true
	default:
		return "", false
	}
}

// ExecutionModes lists the accepted values, for an error message or a
// capability listing. The unset value is not among them: it is the absence of a
// choice rather than one of the choices.
func ExecutionModes() []string {
	return []string{ExecutionInteractive, ExecutionAutonomous}
}
