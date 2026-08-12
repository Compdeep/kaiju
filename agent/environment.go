package agent

import "log"

// What an application says about the surroundings a run happens in.
//
// Config.Environment returns text this package appends to a stage's prompt and
// attaches no meaning to. Seven prompt-building sites read it through
// fleetSection, so one crash would take every stage with it.

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
