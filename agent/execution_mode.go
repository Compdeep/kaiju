package agent

// How a turn is handled: whether it is answered directly, planned, or routed
// between the two.
//
// One setting with three values, because it used to be two settings with four
// combinations and only three behaviours. chat_mode picked a lane, execution
// mode decided whether the router ran, and execution mode was not read at all on
// the chat lane — so two of the four combinations were identical and nothing
// said so. A third control existed to narrow the chat lane and no interface
// sent it.
//
// The three are what a caller actually wants to say, and each is reachable:
//
//	chat   answer it, and only answer it. No planner, no tools, no escalation,
//	       whatever the message says. Uses the chat model where one is set,
//	       falling back to the reasoning model. This is what people assume "chat
//	       mode" means, and what the old chat lane did NOT promise: that lane
//	       still asked the router and could leave for the planner mid-turn.
//
//	auto   let the router read the message and decide — a conversational answer
//	       where that is enough, the planner and tools where it is not.
//
//	agent  plan it, always, whatever the message says. Also marks the run as one
//	       nobody is watching (see unattended.go), which withholds every tool
//	       declaring RequiresHuman. The two go together because a run nobody is
//	       watching is one nobody can be asked.
const (
	// ExecutionChat answers directly and never escalates.
	ExecutionChat = "chat"
	// ExecutionAuto lets the router choose between answering and planning.
	ExecutionAuto = "auto"
	// ExecutionAgent always plans, and treats the run as unwatched.
	ExecutionAgent = "agent"
	// ExecutionUnset inherits: a trigger takes the configured default, and the
	// configured default takes ExecutionAuto.
	ExecutionUnset = ""
)

// The names these values used to have, accepted so that config files and
// clients written against them keep working.
//
// "interactive" is gone as a name rather than kept as a preference: it promised
// a run that checks in with a person, and nothing about it ever did — what it
// meant was "let the router decide", which is what auto says. "autonomous" was
// accurate and is retired only to sit alongside the other two.
//
// Accepted on the way in, never produced on the way out: ParseExecutionMode
// returns the current name for both, so nothing downstream has to know two
// spellings and no listing offers a name we would rather nobody wrote.
var executionAliases = map[string]string{
	"interactive": ExecutionAuto,
	"autonomous":  ExecutionAgent,
}

/*
 * ParseExecutionMode reports whether s names a way of handling a turn.
 * desc: Empty is valid and means unset — a request that says nothing takes the
 *       node's configured mode, and a config that says nothing takes
 *       ExecutionAuto. A retired name is accepted and answered with the current
 *       one, so a caller writing "autonomous" is understood and everything
 *       downstream sees only "agent".
 *
 *       Anything else is a typo, and the caller is expected to refuse it rather
 *       than pick a mode on the writer's behalf: the three choices differ in
 *       whether tools run at all, so guessing is not a small error.
 * param: s - the value as written in a request or a config file.
 * return: the current name for the mode, and whether it was one.
 */
func ParseExecutionMode(s string) (string, bool) {
	switch s {
	case ExecutionUnset, ExecutionChat, ExecutionAuto, ExecutionAgent:
		return s, true
	}
	if current, ok := executionAliases[s]; ok {
		return current, true
	}
	return "", false
}

/*
 * executionOf reports the mode a trigger is asking for, by its current name.
 * desc: A trigger reaching the engine from an application is built in Go and
 *       never passes the parser, so it can carry a retired name — and one does:
 *       an application marking its unwatched work wrote "autonomous" into the
 *       field directly. Comparing that against the current name alone would
 *       answer "not agent", and the run would be treated as watched, which is
 *       the answer that hands it the tools reserved for a person to approve.
 *
 *       So the comparison goes through the same understanding the doors use.
 *       An unrecognised value answers empty, which reads as unset — the
 *       cautious end of every question asked of it.
 * param: t - the trigger.
 * return: the current name for its mode, or empty.
 */
func executionOf(t Trigger) string {
	mode, _ := ParseExecutionMode(t.ExecutionMode)
	return mode
}

/*
 * IsAutonomous reports whether a trigger asks for a run nobody is watching.
 * desc: Exported because applications answer this question for the engine — an
 *       application that knows its own kinds of unwatched work supplies a rule,
 *       and that rule still has to read this field for the triggers that carry
 *       it. Without this it reads the field itself, which means a copy of the
 *       current spelling living outside this package, answering "not
 *       autonomous" for a trigger written with the retired one.
 *
 *       That answer is the dangerous direction: it says a person is there, and
 *       the run is handed the tools that exist to be asked for.
 * param: t - the trigger.
 * return: true when the trigger asks for the agent mode, by any accepted name.
 */
func IsAutonomous(t Trigger) bool { return executionOf(t) == ExecutionAgent }

// ExecutionModes lists the accepted values, for an error message or a
// capability listing. Retired names are absent: they are understood, not
// offered. The unset value is absent too — it is the absence of a choice.
func ExecutionModes() []string {
	return []string{ExecutionChat, ExecutionAuto, ExecutionAgent}
}

// The reasoning lane's thinking switch.
//
// Only this lane has one. The router and the executor force a small tool call
// and send reasoning off whatever is configured, because reasoning there
// consumes the budget the call was to fill; answer and chat take the model's
// default and are better for it. Here the planning is what the budget is for,
// so which way it goes is a judgement about the deployment.
const (
	// ReasoningOn asks for reasoning even from a model that ships it off.
	ReasoningOn = "on"
	// ReasoningOff refuses it even from a model that ships it on.
	ReasoningOff = "off"
	// ReasoningDefault takes whatever the model does unasked.
	ReasoningDefault = ""
)

/*
 * ParseReasoning reports whether s names a reasoning setting.
 * desc: Empty is valid and means the model's own default, which is what every
 *       config file said before the switch existed. Anything else is refused
 *       rather than corrected, for the reason ParseExecutionMode gives: the two
 *       possible corrections are opposite, and each is wrong when the writer
 *       meant the other.
 * param: s - the value as written in a request or a config file.
 * return: the setting, and whether it was one.
 */
func ParseReasoning(s string) (string, bool) {
	switch s {
	case ReasoningDefault, ReasoningOn, ReasoningOff:
		return s, true
	default:
		return "", false
	}
}

// ReasoningModes lists the settings that ask for something, for an error message
// or a capability listing. The default is not among them: it is the absence of a
// choice rather than one of the choices.
func ReasoningModes() []string {
	return []string{ReasoningOn, ReasoningOff}
}
