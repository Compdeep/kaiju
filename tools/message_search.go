package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Compdeep/kaiju/agent"
	"github.com/Compdeep/kaiju/agent/toolapi"
)

// MessageSearch looks through stored conversation messages.
//
// An agent is sent a window of recent messages and a summary of what came
// before, so anything said earlier than that window is in the database and not
// in front of the model. This is how it reads what it can no longer see.
type MessageSearch struct {
	store toolapi.MessageStore
}

/*
 * NewMessageSearch builds the message search tool.
 * desc: Returns a tool that reads the given store.
 * param: store - where the messages are kept
 * return: the tool
 */
func NewMessageSearch(store toolapi.MessageStore) *MessageSearch {
	return &MessageSearch{store: store}
}

// currentSession is the conversation the run belongs to.
//
// Read from the run rather than held on the tool: one registry serves every
// conversation on the machine, so a session fixed when the tool was built would
// have every run searching whichever conversation happened to be first. Empty
// when there is no run, which is what a direct call or the ReAct loop gets.
func currentSession(ctx context.Context) string {
	ec := agent.ExecContextFrom(ctx)
	if ec == nil || ec.Graph == nil {
		return ""
	}
	return ec.Graph.SessionID
}

// RequiresTarget is false: the messages are the application's own, wherever the
// agent is pointed.
func (m *MessageSearch) RequiresTarget() bool { return false }

/*
 * Name returns the tool identifier.
 * desc: Returns "message_search" as the tool name.
 * return: the string "message_search"
 */
func (m *MessageSearch) Name() string { return "message_search" }

/*
 * Description returns a human-readable description of the tool.
 * desc: Explains that this tool searches earlier conversation messages.
 * return: description string
 */
func (m *MessageSearch) Description() string {
	return "Search earlier conversation messages for words. Reaches messages older than the ones in front of you, including any a summary has replaced. Use it to recall what was said or decided before."
}

/*
 * Impact returns the safety impact level for this tool.
 * desc: Always ImpactObserve — searching reads and changes nothing.
 * return: ImpactObserve (0)
 */
func (m *MessageSearch) Impact(map[string]any) int { return toolapi.ImpactObserve }

/*
 * Parameters returns the JSON schema for the tool's input parameters.
 * desc: Declares the required query and the optional scope and limit.
 * return: JSON schema as raw bytes
 */
func (m *MessageSearch) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"query": {
				"type": "string",
				"description": "Words to look for. Every word must appear in a message for it to match, and words are matched by their stem, so \"process\" finds \"processes\"."
			},
			"scope": {
				"type": "string",
				"enum": ["session", "all"],
				"default": "session",
				"description": "\"session\" searches this conversation only; \"all\" searches every conversation."
			},
			"limit": {
				"type": "integer",
				"minimum": 1,
				"maximum": 50,
				"default": 10,
				"description": "Most messages to return, best match first."
			}
		},
		"required": ["query"],
		"additionalProperties": false
	}`)
}

/*
 * OutputSchema returns the JSON schema for the tool's output.
 * desc: Declares the query, the scope searched, the count and the messages.
 * return: JSON schema as raw bytes
 */
func (m *MessageSearch) OutputSchema() json.RawMessage {
	return toolapi.EnvelopeSchema(toolapi.PayloadSchemaOf(messageSearchData{}))
}

// Execute satisfies the Tool interface for callers outside the DAG.
func (m *MessageSearch) Execute(ctx context.Context, params map[string]any) (string, error) {
	return toolapi.StringResult(m.ExecuteTyped(ctx, params))
}

func (m *MessageSearch) ExecuteTyped(ctx context.Context, params map[string]any) (toolapi.ToolMessage, error) {
	// Built without a store. Reported as a failed step rather than a panic, so
	// the run carries on and the reason is on the record.
	if m.store == nil {
		return toolapi.ToolFail("search", "message_search is not available: this agent was built with no message store", nil), nil
	}

	query, _ := params["query"].(string)
	query = strings.TrimSpace(query)
	if query == "" {
		return toolapi.ToolFail("search", "message_search needs a query — none was supplied", nil), nil
	}
	if !hasWord(query) {
		// Punctuation only. Failed rather than answered with nothing found, which
		// would read as "the conversation does not mention it" and send the caller
		// looking for the wrong thing.
		return toolapi.ToolFail("search", fmt.Sprintf("message_search needs words to look for; %q has none", query), nil), nil
	}

	scope, _ := params["scope"].(string)
	if scope == "" {
		scope = "session"
	}
	if scope != "session" && scope != "all" {
		return toolapi.ToolFail("search", fmt.Sprintf("scope is %q; it takes \"session\" or \"all\"", scope), nil), nil
	}
	sessionID := currentSession(ctx)
	if scope == "all" {
		sessionID = ""
	}
	// Asked for this conversation from a run that is not in one. Widened rather
	// than refused, and said so in the result: the caller wanted the messages, and
	// every message there is qualifies.
	widened := false
	if scope == "session" && sessionID == "" {
		scope, widened = "all", true
	}

	limit, given := toolapi.ParamInt(params, "limit")
	if !given || limit < 1 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}

	found, err := m.store.SearchMessages(ctx, query, sessionID, limit)
	if err != nil {
		return toolapi.ToolFail("search", "message_search could not read the messages: "+err.Error(), nil), nil
	}

	data := messageSearchData{Query: query, Scope: scope, Count: len(found), Messages: found}
	if len(found) == 0 {
		where := "this conversation"
		if scope == "all" {
			where = "any conversation"
		}
		// The payload goes with it, so a step reading count or scope gets the same
		// fields whether the search found anything or not.
		return toolapi.ToolEmptyWith("search", fmt.Sprintf("no message in %s mentions %s", where, query), data), nil
	}

	var text strings.Builder
	for _, f := range found {
		text.WriteString("- **" + f.Role + "**")
		if scope == "all" && f.SessionTitle != "" {
			text.WriteString(" in \"" + f.SessionTitle + "\"")
		}
		if f.CreatedAt > 0 {
			text.WriteString(", " + time.Unix(f.CreatedAt, 0).UTC().Format("2006-01-02 15:04"))
		}
		text.WriteString(": " + oneLine(f.Content) + "\n")
	}
	if widened {
		text.WriteString("\n(searched every conversation: this run is not in one)")
	}
	return toolapi.ToolOK("search", strings.TrimRight(text.String(), "\n"), data), nil
}

// hasWord reports whether there is anything in the query an index could look
// for, rather than only punctuation.
func hasWord(query string) bool {
	for _, r := range query {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

// messageSearchExcerpt is how much of a message goes in the listing.
const messageSearchExcerpt = 300

// oneLine flattens a message to a single readable line.
//
// The listing is a set of results to choose between, not the messages
// themselves; a reply that ran to a page would push the other results out of
// sight. The whole text of each is in the payload beside it.
func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	// Counted in characters, not bytes. A byte count cuts a multi-byte character
	// in half — a Japanese character is three bytes, an emoji four — and the
	// listing the model reads then carries a broken one where the message ends.
	if utf8.RuneCountInString(s) <= messageSearchExcerpt {
		return s
	}
	return string([]rune(s)[:messageSearchExcerpt]) + "…"
}

// messageSearchData is what message_search returns beside its listing.
type messageSearchData struct {
	Query    string                 `json:"query" desc:"the words searched for"`
	Scope    string                 `json:"scope" desc:"whether one conversation was searched or all of them"`
	Count    int                    `json:"count" desc:"messages found"`
	Messages []toolapi.FoundMessage `json:"messages" desc:"the matching messages, best match first"`
}

var _ toolapi.Tool = (*MessageSearch)(nil)
