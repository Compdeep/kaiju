package agent

import "log"

// What an application says about the surroundings a run happens in.
//
// Config.Environment returns text this package appends to a stage's prompt and
// attaches no meaning to. Seven prompt-building sites read it through
// environmentSection, so one crash would take every stage with it.
//
// It replaced a FleetContextProvider, whose vocabulary — fleet, peer, threat,
// campaign indicator — belonged to one product rather than to an engine other
// products build on. That provider stayed in the tree, unreachable, for the
// rest of the migration; it is gone now and this is all that is left of it.

/*
 * describeEnvironment asks the application to describe the surroundings.
 * desc: Nil returns nothing, which is the ordinary case. A panic returns
 *       nothing too — the same answer a missing description gives, so a run
 *       loses a paragraph of context rather than ending. Nothing downstream
 *       distinguishes an application that supplied no description from one whose
 *       description failed, and that is correct: neither has anything to say.
 * return: the text, or empty.
 */
func (a *Agent) describeEnvironment() (out string) {
	if a == nil || a.environment == nil {
		return ""
	}
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[agent] the environment description panicked, describing nothing: %v", r)
			out = ""
		}
	}()
	return a.environment()
}

/*
 * environmentSection returns the description block for a system prompt.
 * desc: Empty when the application supplies no description, which is the
 *       ordinary case and the reason every caller can append it unconditionally.
 *       The blank lines are this package's, so the description does not have to
 *       know what it is being appended to.
 * return: the block, or empty.
 */
func (a *Agent) environmentSection() string {
	text := a.describeEnvironment()
	if text == "" {
		return ""
	}
	return "\n\n" + text
}
