package agent

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/Compdeep/kaiju/internal/workspace"
)

// The coder's contract: one declared message in, one declared message out.
//
// Neither existed. The input was a map[string]any and every caller filled a
// different subset of it — edit_file seven keys, the architect eleven, a plan's own
// compute step four — so the coder could rely on nothing being present. One call
// was handed `Available Data: None`, asked to preserve an Express server it had
// never seen, and wrote 2,953 bytes of invented TypeScript over the real 2,458,
// dropping four of five route mounts.
//
// The output was a bag the engine guessed at: edits arrived, so it edited; code
// arrived, so it wrote. There was no way to say "nothing needed changing" and no way
// to say "I cannot do this", and `code` was a required field — so a coder with
// nothing useful to contribute still had to contribute a file.
//
// Declaring both is what makes the stage composable: edit_file, the architect and a
// plan's compute step all fill the same request, the engine fills the parts only it
// can know, and everything reading the result parses one shape.

// CoderRequest is everything the coder is given.
//
// Goal and File are the caller's. Exists, Lines and Head are the engine's — it
// stats and reads the file, so the coder is never guessing at what is there. The
// rest is optional because only some callers have it: the architect has a brief and
// interfaces, a wired plan step has context, a session with earlier edits has Prior.
type CoderRequest struct {
	Goal string `json:"goal"` // what to do
	File string `json:"file"` // the one file this call may change

	// Engine-filled. Together these are the answer to "what is actually there",
	// which no caller can be trusted to supply and the coder must not invent.
	Exists bool   `json:"exists"`
	Lines  int    `json:"lines"` // how many lines the file has
	Head   string `json:"head"`  // its first lines, numbered

	Context    any      `json:"context,omitempty"`    // outputs wired from earlier steps
	Prior      []string `json:"prior,omitempty"`      // what earlier coders did to this file
	Brief      string   `json:"brief,omitempty"`      // the architect's notes
	Interfaces any      `json:"interfaces,omitempty"` // types and APIs to implement against
	Structure  string   `json:"structure,omitempty"`  // the project tree
}

// The four things a coder may report. Every one of them is a complete answer.
const (
	// CoderEdited: replacements to apply to an existing file.
	CoderEdited = "edited"
	// CoderCreated: a whole file, because there was none.
	CoderCreated = "created"
	// CoderNoChange: the file already satisfies the goal. Not a failure, and not a
	// success either — the node reports empty so a later stage sees work that did
	// not need doing rather than work that was done.
	CoderNoChange = "no_change"
	// CoderBlocked: the goal cannot be met with what was given. The node fails and
	// the reason says who has to change something.
	CoderBlocked = "blocked"
)

// CoderBlockedReason says what is missing and who can supply it.
//
// "You did not tell me what to preserve" and "I could not read the file" want
// different responses, and as one undifferentiated error string the reflector
// guesses. It has guessed wrong before: a raw "no such file or directory" twice sent
// it to investigate a timing problem that did not exist.
type CoderBlockedReason struct {
	Needs string `json:"needs"` // what would unblock it, concretely
	From  string `json:"from"`  // "planner" or "environment"
}

// Who has to act on a block.
const (
	BlockedFromPlanner     = "planner"     // the step needs different or more input
	BlockedFromEnvironment = "environment" // the machine or the file is not as needed
)

// CoderResult is everything the coder returns.
//
// Status is required and leads, so the coder states what it did before describing
// it. That ordering is the point: a reply claiming `edited` with no edits is a
// contradiction the engine can catch, where previously an empty reply was
// indistinguishable from a model that had failed.
type CoderResult struct {
	Status  string `json:"status"`
	Summary string `json:"summary"` // what and why, in the coder's words

	Edits   []EditOp            `json:"edits,omitempty"`
	Code    json.RawMessage     `json:"code,omitempty"`
	Blocked *CoderBlockedReason `json:"blocked,omitempty"`

	Language string `json:"language,omitempty"`
	Filename string `json:"filename,omitempty"`
	// Execute belongs to the script-writing path, where a compute writes a file and
	// a grafted bash node runs it. It is not part of editing.
	Execute string `json:"execute,omitempty"`
}

/*
 * Validate reports whether a result says something coherent.
 * desc: The schema requires status and the parser infers it when a provider drops
 *       it, so by here it is set. What this catches is a status that contradicts
 *       its own payload — `edited` with no edits, `blocked` with no reason — which
 *       the engine would otherwise apply as an empty change and report as success.
 * return: nil, or an error naming the contradiction.
 */
func (r *CoderResult) Validate() error {
	switch r.Status {
	case CoderEdited:
		if len(r.Edits) == 0 {
			return fmt.Errorf("status %q with no edits: say no_change if the file already satisfies the goal, or blocked if you cannot meet it", r.Status)
		}
	case CoderCreated:
		if len(r.Code) == 0 {
			return fmt.Errorf("status %q with no code: a created file needs its content", r.Status)
		}
	case CoderNoChange:
		if strings.TrimSpace(r.Summary) == "" {
			return fmt.Errorf("status %q with no summary: say why nothing needed changing", r.Status)
		}
	case CoderBlocked:
		if r.Blocked == nil || strings.TrimSpace(r.Blocked.Needs) == "" {
			return fmt.Errorf("status %q with no reason: say what is missing and whether the planner or the environment has to supply it", r.Status)
		}
	case "":
		return fmt.Errorf("no status: say which of edited, created, no_change or blocked happened")
	default:
		return fmt.Errorf("unknown status %q: one of edited, created, no_change, blocked", r.Status)
	}
	return nil
}

/*
 * inferStatus fills a missing status from what the reply carries.
 * desc: The schema requires status, and a provider that drops a required field
 *       would otherwise fail a reply that is otherwise usable. Strict schema,
 *       forgiving parser — and it says when it had to guess, because a coder
 *       routinely omitting the field is a prompt problem worth seeing.
 * return: true when a status was inferred.
 */
func (r *CoderResult) inferStatus() bool {
	if r.Status != "" {
		return false
	}
	switch {
	case len(r.Edits) > 0:
		r.Status = CoderEdited
	case len(r.Code) > 0:
		r.Status = CoderCreated
	case r.Blocked != nil:
		r.Status = CoderBlocked
	default:
		// Nothing arrived at all. Not a no-op — a no-op is a claim about the file,
		// and this reply makes no claim about anything.
		return false
	}
	return true
}

// How much of the file the coder is handed up front.
//
// Enough that an ordinary file arrives whole and needs no second look: main.ts is
// 58 lines, and every section file in a 14-file documentation pass was under 400.
// Bounded because a prompt is not a place to put a large file, and because a coder
// that cannot hold the file must edit regions rather than rewrite it — which is the
// shape to prefer anyway.
const (
	coderHeadLines = 400
	coderHeadChars = 60000
)

// coderFileFacts is what the engine knows about the file and the coder must not
// guess: whether it is there, how long it is, and what its opening lines say.
type coderFileFacts struct {
	Path   string
	Exists bool
	Lines  int
	Bytes  int
	Head   string // numbered, so an edit can cite the lines it changes
	Whole  bool   // true when Head is the entire file
}

/*
 * coderFileFacts establishes what is on disk for the file this call will write.
 * desc: The first task file, because that is the one the write and the edits both
 *       target. Absent is a fact too — it is how creation is distinguished from
 *       editing, and it is reported rather than inferred from a silence.
 * param: graph - for the project prefix; may be nil.
 * param: taskFiles - the call's files; the first is the one that gets written.
 * param: tag - for the log line when a file exists and cannot be read.
 * return: the facts, with Exists false when there is nothing there.
 */
func (a *Agent) coderFileFacts(graph *Graph, taskFiles []string, tag string) coderFileFacts {
	if len(taskFiles) == 0 {
		return coderFileFacts{}
	}
	target := taskFiles[0]
	if !strings.HasPrefix(target, "/") && !strings.HasPrefix(target, projectPrefix(graph, taskFiles)) {
		target = projectPrefix(graph, taskFiles) + target
	}
	facts := coderFileFacts{Path: target}

	full, err := workspace.Resolve(a.cfg.Workspace, target)
	if err != nil {
		log.Printf("[dag] compute %s: task file %s is not a path this run may touch: %v", tag, target, err)
		return facts
	}
	data, err := os.ReadFile(full)
	if err != nil {
		// Absent is ordinary — it means create. Present and unreadable is the start
		// of a blind rewrite, and the two used to be the same silence.
		if !os.IsNotExist(err) {
			log.Printf("[dag] compute %s: %s exists and could not be read, so this becomes a write: %v", tag, full, err)
		}
		return facts
	}
	if len(data) == 0 {
		return facts // nothing to edit
	}

	facts.Exists = true
	facts.Bytes = len(data)
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	facts.Lines = len(lines)

	shown := lines
	if len(shown) > coderHeadLines {
		shown = shown[:coderHeadLines]
	}
	var sb strings.Builder
	for i, l := range shown {
		if sb.Len() > coderHeadChars {
			shown = shown[:i]
			break
		}
		fmt.Fprintf(&sb, "%6d| %s\n", i+1, l)
	}
	facts.Head = sb.String()
	facts.Whole = len(shown) == len(lines)
	return facts
}

/*
 * render writes the facts into the coder's prompt.
 * desc: Numbered, because an edit names the lines it changes and cannot cite
 *       numbers it was never given. And when the file is longer than what is shown,
 *       it says so and says what to do about it — a coder that rewrites a file it
 *       has only partly seen loses everything below the cut, which is how a server
 *       lost four of its five routes.
 * return: the markdown block, or "" when there is no file.
 */
func (f coderFileFacts) render() string {
	if f.Path == "" {
		return ""
	}
	if !f.Exists {
		return fmt.Sprintf("\n## The file you are writing\n%s — does not exist yet, so this is a new file.\n", f.Path)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "\n## The file you are changing\n%s — %d lines, %d bytes.\n", f.Path, f.Lines, f.Bytes)
	if f.Whole {
		sb.WriteString("What follows is the whole file.\n")
	} else {
		shown := strings.Count(f.Head, "\n")
		fmt.Fprintf(&sb, "What follows is lines 1-%d. There are %d lines you have NOT been shown. "+
			"Edit the part you are changing — replacing the whole file would discard everything below line %d. "+
			"If you need to see further, say so with status blocked.\n", shown, f.Lines-shown, shown)
	}
	sb.WriteString("\nThe line numbers are the file's own. Cite them in an edit's `lines`.\n\n```\n")
	sb.WriteString(f.Head)
	sb.WriteString("```\n")
	return sb.String()
}
