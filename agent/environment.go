package agent

import (
	"log"
	"strings"
	"time"
)

// What an application says about the surroundings a run happens in.
//
// Config.Environment returns text this package appends to a stage's prompt and
// attaches no meaning to. Seven prompt-building sites read it through
// environmentSection, so one crash would take every stage with it.
//
// It replaced a provider whose name and whose four field names belonged to one
// product rather than to an engine other products build on. That provider
// stayed in the tree, unreachable, for the
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
 * environmentSection returns the surroundings block for a system prompt.
 * desc: Two things, in one block because they answer one question: what is true
 *       around this run. The date is this package's and is always stated; the
 *       description after it is the application's and is often empty.
 *
 *       The date is here rather than only in the gate's context because the gate
 *       puts it on whichever source happens to be non-empty first — on a fresh
 *       session that is the tool index, which is the middle of a very long
 *       system prompt. Every stage that writes a parameter appends this section,
 *       and it lands in the same place every time.
 *
 *       Both remain: the gate's line reaches stages that never read this, and
 *       this reaches stages that never call the gate. They are one fact from one
 *       clock, so they cannot disagree about anything that matters.
 *
 *       The blank lines are this package's, so neither part has to know what it
 *       is being appended to.
 * return: the block. Never empty.
 */
func (a *Agent) environmentSection() string {
	var sb strings.Builder
	sb.WriteString("\n\n## Now\n\nCurrent time: ")
	sb.WriteString(time.Now().UTC().Format(llmTimeFormat))
	sb.WriteString(" UTC.\n\n")
	sb.WriteString("Any date or time you write into a parameter comes from that line. " +
		"\"now\", \"today\", \"current\" and \"latest\" all mean that date. A date you " +
		"remember is a guess: it looks valid, the tool accepts it, and what comes " +
		"back is the right answer to the wrong question.\n")
	if text := a.describeEnvironment(); text != "" {
		sb.WriteString("\n" + text)
	}
	return sb.String()
}
