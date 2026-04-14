// Package memory provides conversation memory management following LangChain principles.
// Supports short-term (session history), long-term semantic (facts), long-term episodic
// (experiences), and procedural (future) memory types.
// All operations are scoped by user ID for multi-tenant isolation.
//
// ── Architectural boundary (READ THIS BEFORE WIRING MEMORY ANYWHERE) ────────
//
// Memory belongs at the CHAT BOUNDARY only. The two legitimate access points
// are:
//
//   1. Chat input  (internal/api/api.go handleChat)
//        - Loads conversation history into Trigger.History
//        - Calls InjectLongTermContext to prepend semantic + episodic facts
//        - Stores the user's incoming message
//
//   2. Chat output (after the aggregator runs, also in api.go)
//        - Stores the assistant's verdict as the next message
//        - Optionally extracts new semantic / episodic facts
//
// The agent's EXECUTION LAYER must never query or write memory:
//   - ContextGate has no memory source by design
//   - Graph nodes (executive, compute, reflector, debugger, observer,
//     aggregator) must not call this package directly
//   - Source implementations in internal/agent/contextgate.go must not
//     reach into memory state
//
// Why this matters (anti-prompt-injection security):
//
// The execution layer runs UNTRUSTED tool output through reasoning steps:
// bash command output, web fetches, the responses of compute/coder LLM
// calls, debugger plans, and so on. Any of those can contain adversarial
// content trying to manipulate the agent. If memory were reachable from
// inside the execution layer, a malicious tool result could either:
//   - Exfiltrate the user's stored facts by causing them to be quoted in
//     a subsequent LLM call that goes to a logging/network sink, or
//   - Rewrite the user's memory by inducing the agent to call a hidden
//     memory_store-like path with attacker-supplied content.
//
// By keeping memory at the chat boundary, both reads and writes are
// attested by the authenticated user request itself. Untrusted tool
// content cannot reach memory because there is no code path from the
// execution layer to this package.
//
// The ONLY exception is the explicit memory tools (memory_store,
// memory_recall, memory_search). Those let the LLM DELIBERATELY interact
// with memory as a tool call, just like file_write or bash. That requires
// the LLM to make an active decision and is auditable in the worklog.
// Automatic injection inside execution-layer code is not.
//
// If you want to add a new memory access path, ask: "is this attested by
// an authenticated user request, or is it triggered automatically by code
// that might be processing untrusted input?" If the latter, do not add it.
package memory

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/google/uuid"
	"github.com/Compdeep/kaiju/internal/agent/llm"
	"github.com/Compdeep/kaiju/internal/db"
)

const (
	// TypeSemantic is the memory type for factual knowledge.
	TypeSemantic = "semantic"
	// TypeEpisodic is the memory type for past experiences.
	TypeEpisodic = "episodic"
	// TypeProcedural is the memory type for planned future actions.
	TypeProcedural = "procedural"

	// DefaultCompactThreshold is the message count above which auto-compaction triggers.
	DefaultCompactThreshold = 30
	// CompactKeepRecent is the number of recent messages preserved during compaction.
	CompactKeepRecent = 10
	// DefaultMaxHistory is the default maximum number of messages to load for history.
	DefaultMaxHistory = 50
)

/*
 * Manager provides memory operations scoped to a single user.
 * desc: Constructed per-request; stateless beyond its DB and LLM handles. Manages sessions, messages, and long-term memories.
 */
type Manager struct {
	db     *db.DB
	llm    *llm.Client
	userID string
}

/*
 * New creates a memory manager bound to a specific user.
 * desc: Constructs a Manager with the given database, LLM client, and user ID for scoped memory operations.
 * param: database - database handle for persistence
 * param: llmClient - LLM client used for compaction summarization
 * param: userID - the user ID that scopes all memory operations
 * return: a configured Manager instance
 */
func New(database *db.DB, llmClient *llm.Client, userID string) *Manager {
	return &Manager{db: database, llm: llmClient, userID: userID}
}

// ─── Short-term (session history) ────────────────────────────────────────────

/*
 * NewSession creates a new conversation session.
 * desc: Generates a UUID and creates a session record in the database for the current user.
 * param: ctx - request context
 * param: channel - the channel that initiated the session (e.g. "web", "cli")
 * return: the new session ID and any error
 */
func (m *Manager) NewSession(ctx context.Context, channel string) (string, error) {
	id := uuid.New().String()
	if err := m.db.CreateSession(id, channel, m.userID, ""); err != nil {
		return "", fmt.Errorf("memory: create session: %w", err)
	}
	return id, nil
}

/*
 * ListSessions returns the user's sessions, newest first.
 * desc: Queries the database for sessions belonging to the current user, limited by count.
 * param: limit - maximum number of sessions to return
 * return: the session list and any error
 */
func (m *Manager) ListSessions(limit int) ([]db.Session, error) {
	return m.db.ListSessionsForUser(m.userID, limit)
}

/*
 * LoadHistory loads conversation history as LLM messages.
 * desc: Fetches messages for the session, verifies ownership, and converts them to LLM message format. Returns nil if session is not found or not owned.
 * param: ctx - request context
 * param: sessionID - the session to load history from
 * param: maxMessages - maximum number of messages to retrieve
 * return: the message list and any error
 */
func (m *Manager) LoadHistory(ctx context.Context, sessionID string, maxMessages int) ([]llm.Message, error) {
	// Verify ownership
	_, err := m.db.GetSessionForUser(sessionID, m.userID)
	if err != nil {
		return nil, nil // not found or not owned — return empty, not error
	}

	if maxMessages <= 0 {
		maxMessages = DefaultMaxHistory
	}

	dbMsgs, err := m.db.GetMessages(sessionID, maxMessages)
	if err != nil {
		return nil, fmt.Errorf("memory: load history: %w", err)
	}

	msgs := make([]llm.Message, 0, len(dbMsgs))
	for _, dm := range dbMsgs {
		// Only pass valid LLM roles — skip any metadata rows
		// that might still exist from older databases.
		switch dm.Role {
		case "user", "assistant", "system":
			// valid LLM roles
		default:
			continue
		}
		// Truncate history messages to avoid flooding the executive with
		// full aggregator verdicts and long user messages from prior turns.
		content := dm.Content
		switch dm.Role {
		case "assistant":
			if len(content) > 700 {
				head := content[:500]
				tail := content[len(content)-200:]
				content = head + "\n...\n" + tail
			}
		case "user":
			if len(content) > 1300 {
				head := content[:1000]
				tail := content[len(content)-300:]
				content = head + "\n...\n" + tail
			}
		}
		msgs = append(msgs, llm.Message{
			Role:    dm.Role,
			Content: content,
		})
	}
	return msgs, nil
}

/*
 * StoreMessage saves a message to the session.
 * desc: Persists the message and auto-titles the session from the first user message.
 * param: sessionID - the session to store the message in
 * param: role - the message role (user, assistant, system)
 * param: content - the message content
 * return: any error from the database write
 */
func (m *Manager) StoreMessage(sessionID, role, content string) error {
	if err := m.db.AddMessage(sessionID, role, content); err != nil {
		return fmt.Errorf("memory: store message: %w", err)
	}

	// Auto-title: set session title from first user message
	if role == "user" {
		count, _ := m.db.MessageCount(sessionID)
		if count <= 1 {
			title := content
			if len(title) > 60 {
				title = title[:60] + "..."
			}
			m.db.UpdateSessionTitle(sessionID, title)
		}
	}
	return nil
}

/*
 * ShouldCompact returns true if the session has more messages than the threshold.
 * desc: Checks the message count against DefaultCompactThreshold to decide if compaction is needed.
 * param: sessionID - the session to check
 * return: true if compaction is recommended, and any error from counting
 */
func (m *Manager) ShouldCompact(sessionID string) (bool, error) {
	count, err := m.db.MessageCount(sessionID)
	if err != nil {
		return false, err
	}
	return count > DefaultCompactThreshold, nil
}

/*
 * DeleteSession removes a session and all its messages (ownership checked).
 * desc: Verifies the session belongs to the current user before deleting it and its messages.
 * param: sessionID - the session to delete
 * return: an error if the session is not found, not owned, or deletion fails
 */
func (m *Manager) DeleteSession(sessionID string) error {
	_, err := m.db.GetSessionForUser(sessionID, m.userID)
	if err != nil {
		return fmt.Errorf("memory: session not found or not owned")
	}
	return m.db.DeleteSession(sessionID)
}

// ─── Long-term memory ────────────────────────────────────────────────────────

/*
 * StoreMemory saves a long-term memory scoped to this user.
 * desc: Persists a memory entry with a user-scoped namespace derived from the user ID and memory type.
 * param: key - the memory key/label
 * param: content - the memory content
 * param: memType - the memory type (semantic, episodic, procedural)
 * param: tags - optional tags for categorization
 * return: any error from the database write
 */
func (m *Manager) StoreMemory(key, content, memType string, tags []string) error {
	namespace := m.userID + "/" + memType
	return m.db.StoreMemory(db.Memory{
		Namespace: namespace,
		Key:       key,
		Content:   content,
		Type:      memType,
		Tags:      tags,
	})
}

/*
 * RecallMemories searches the user's memories plus global memories.
 * desc: Queries across the user's semantic and episodic namespaces as well as the global semantic namespace.
 * param: query - search query string (can be empty for all)
 * param: types - optional list of memory types to filter by
 * param: limit - maximum number of results
 * return: the matching memories and any error
 */
func (m *Manager) RecallMemories(query string, types []string, limit int) ([]db.Memory, error) {
	namespaces := []string{
		m.userID + "/" + TypeSemantic,
		m.userID + "/" + TypeEpisodic,
		"_global/" + TypeSemantic,
	}
	return m.db.SearchMemories(namespaces, query, types, limit)
}

/*
 * ForgetMemory deletes a memory, verifying ownership.
 * desc: Checks that the memory's namespace starts with the current user's ID before deleting it.
 * param: id - the memory ID to delete
 * return: an error if the memory is not found, not owned, or deletion fails
 */
func (m *Manager) ForgetMemory(id string) error {
	mem, err := m.db.GetMemory(id)
	if err != nil {
		return fmt.Errorf("memory: not found")
	}
	// Verify ownership — must start with user's namespace
	if !strings.HasPrefix(mem.Namespace, m.userID+"/") {
		return fmt.Errorf("memory: access denied")
	}
	return m.db.DeleteMemory(id)
}

/*
 * InjectLongTermContext builds a formatted string of the user's long-term memories for injection into the LLM system prompt.
 * desc: Loads semantic and episodic memories and formats them as a markdown section for context injection.
 * param: ctx - request context
 * return: the formatted memory context string (empty if no memories), and any error
 */
func (m *Manager) InjectLongTermContext(ctx context.Context) (string, error) {
	// Load semantic memories
	semanticNS := []string{m.userID + "/" + TypeSemantic, "_global/" + TypeSemantic}
	semanticMems, err := m.db.SearchMemories(semanticNS, "", []string{TypeSemantic}, 20)
	if err != nil {
		return "", err
	}

	// Load recent episodic memories
	episodicNS := []string{m.userID + "/" + TypeEpisodic}
	episodicMems, err := m.db.SearchMemories(episodicNS, "", []string{TypeEpisodic}, 10)
	if err != nil {
		return "", err
	}

	if len(semanticMems) == 0 && len(episodicMems) == 0 {
		return "", nil
	}

	var sb strings.Builder
	sb.WriteString("## Your Memory\n\n")

	if len(semanticMems) > 0 {
		sb.WriteString("### Known Facts\n")
		for _, mem := range semanticMems {
			sb.WriteString(fmt.Sprintf("- **%s**: %s\n", mem.Key, mem.Content))
		}
		sb.WriteString("\n")
	}

	if len(episodicMems) > 0 {
		sb.WriteString("### Past Experiences\n")
		for _, mem := range episodicMems {
			sb.WriteString(fmt.Sprintf("- %s\n", mem.Content))
		}
		sb.WriteString("\n")
	}

	log.Printf("[memory] injecting %d semantic + %d episodic memories for user %s",
		len(semanticMems), len(episodicMems), m.userID)

	return sb.String(), nil
}
