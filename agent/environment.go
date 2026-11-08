package agent

import (
	"log"
	"strings"
	"time"

	"github.com/Compdeep/kaiju/agent/llm"
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
	sb.WriteString("\n\n## Now\n\n" + timeNotice + "\n\n")
	sb.WriteString("Any date or time you write into a parameter comes from that line. " +
		"\"now\", \"today\", \"current\" and \"latest\" all mean that date. A date you " +
		"remember is a guess: it looks valid, the tool accepts it, and what comes " +
		"back is the right answer to the wrong question.\n")
	if text := a.describeEnvironment(); text != "" {
		sb.WriteString("\n" + text)
	}
	return sb.String()
}

// timeNotice is how environmentSection says where the time is. prepare looks
// for it, and adds the line it promises.
const timeNotice = "The current time is on the line in this request that starts \"Current time:\"."

/*
 * stateTheTime adds the current time to a request whose system prompt says
 * where to find it.
 * desc: At the end of the last message, when that is the user's: a provider
 *       reuses its work on a request only as far as the first character that
 *       differs from the last one it saw, and the clock is the one line that
 *       differs on every call. In the system prompt it cost everything after
 *       it — a reflector shared 23% of its request with the one before it.
 *
 *       When the request ends otherwise — the ReAct loop in the middle of a
 *       turn ends with a tool result — the line goes at the end of the system
 *       prompt, as it always did, rather than into a tool result or a turn of
 *       its own.
 *
 *       A request whose system prompt does not carry the section is left
 *       alone: a stage never told the time is not told it now.
 * param: req - the request, changed in place.
 */
func stateTheTime(req *llm.ChatRequest) {
	sys := -1
	for i, m := range req.Messages {
		if m.Role == "system" && strings.Contains(m.Content, timeNotice) {
			sys = i
			break
		}
	}
	if sys < 0 {
		return
	}
	line := timeLinePrefix + time.Now().UTC().Format(llmTimeFormat) + " UTC."
	last := &req.Messages[len(req.Messages)-1]
	if last.Role == "user" && len(last.Parts) == 0 {
		last.Content = withoutTimeLine(last.Content) + line
		return
	}
	req.Messages[sys].Content = withoutTimeLine(req.Messages[sys].Content) + line
}

// timeLinePrefix opens the line stateTheTime adds.
const timeLinePrefix = "\n\nCurrent time: "

// withoutTimeLine is text without a line stateTheTime added earlier, so a
// request sent through the door twice states the time once.
func withoutTimeLine(text string) string {
	i := strings.LastIndex(text, timeLinePrefix)
	if i >= 0 && strings.HasSuffix(text, " UTC.") && !strings.Contains(text[i+2:], "\n") {
		return text[:i]
	}
	return text
}
