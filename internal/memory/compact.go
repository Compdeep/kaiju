package memory

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/user/kaiju/internal/agent/llm"
)

const compactPrompt = `Summarize the following conversation history in 2-3 concise paragraphs.

Preserve:
- Key facts and information shared
- Decisions made and action items
- Important context needed to continue the conversation
- The user's goals and preferences expressed

Do NOT include:
- Greetings or pleasantries
- Redundant information
- Detailed tool call outputs (just mention what was learned)

Conversation:`

/*
 * Compact summarizes old messages in a session, replacing them with a summary.
 * desc: Verifies ownership, splits messages into old and recent, uses the LLM to summarize old messages, deletes them, and inserts a summary system message. Keeps the most recent CompactKeepRecent messages intact.
 * param: ctx - context for the LLM call
 * param: sessionID - the session to compact
 * return: the generated summary text and any error
 */
func (m *Manager) Compact(ctx context.Context, sessionID string) (string, error) {
	// Verify ownership
	_, err := m.db.GetSessionForUser(sessionID, m.userID)
	if err != nil {
		return "", fmt.Errorf("memory: session not found or not owned")
	}

	count, err := m.db.MessageCount(sessionID)
	if err != nil {
		return "", err
	}
	if count <= CompactKeepRecent {
		return "", nil // nothing to compact
	}

	// Load all messages
	allMsgs, err := m.db.GetMessages(sessionID, 0)
	if err != nil {
		return "", fmt.Errorf("memory: load messages: %w", err)
	}

	if len(allMsgs) <= CompactKeepRecent {
		return "", nil
	}

	// Split: old messages to summarize, recent to keep
	splitIdx := len(allMsgs) - CompactKeepRecent
	toSummarize := allMsgs[:splitIdx]
	keep := allMsgs[splitIdx:]

	// Format old messages for summarization
	var formatted strings.Builder
	for _, msg := range toSummarize {
		formatted.WriteString(fmt.Sprintf("%s: %s\n\n", msg.Role, msg.Content))
	}

	// LLM call to summarize
	resp, err := m.llm.Complete(ctx, &llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "system", Content: compactPrompt},
			{Role: "user", Content: formatted.String()},
		},
		Temperature: 0.3,
		MaxTokens:   1024,
	})
	if err != nil {
		return "", fmt.Errorf("memory: compact LLM call: %w", err)
	}

	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("memory: compact returned no choices")
	}

	summary := resp.Choices[0].Message.Content

	// Delete old messages, keep newest
	if err := m.db.DeleteOldestMessages(sessionID, CompactKeepRecent); err != nil {
		return "", fmt.Errorf("memory: delete old messages: %w", err)
	}

	// Insert summary as system message before the remaining messages
	earliestKeepTime := keep[0].CreatedAt
	summaryContent := "[Conversation summary]: " + summary
	if err := m.db.PrependMessage(sessionID, "system", summaryContent, earliestKeepTime-1); err != nil {
		return "", fmt.Errorf("memory: insert summary: %w", err)
	}

	log.Printf("[memory] compacted session %s: %d messages → summary + %d recent", sessionID, len(toSummarize), CompactKeepRecent)

	return summary, nil
}
