package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/user/kaiju/internal/channels"
)

// Channel implements an interactive CLI channel (stdin/stdout).
type Channel struct {
	sessionID    string
	mu           sync.RWMutex
	intent       string // "", or any intent name from the registry
	intentLister func() []string // returns valid intent names from the registry; nil = no validation
}

// New creates a CLI channel.
func New() *Channel {
	return &Channel{sessionID: "cli-local"}
}

// SetIntentLister injects a function that returns the current list of valid
// intent names from the agent's intent registry. Optional — when unset, the
// CLI accepts any name and defers validation to the agent. When set, the CLI
// validates /intent <name> commands interactively and shows custom intents in
// help text.
func (c *Channel) SetIntentLister(fn func() []string) {
	c.intentLister = fn
}

func (c *Channel) ID() string { return "cli" }

// Intent returns the current intent level set by the user, or "" for auto.
func (c *Channel) Intent() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.intent
}

func (c *Channel) prompt() string {
	c.mu.RLock()
	intent := c.intent
	c.mu.RUnlock()
	if intent != "" {
		return fmt.Sprintf("  kaiju [%s]> ", intent)
	}
	return "  kaiju> "
}

// Start reads lines from stdin and sends them as inbound messages.
func (c *Channel) Start(ctx context.Context, inbox chan<- channels.InboundMessage) error {
	scanner := bufio.NewScanner(os.Stdin)
	fmt.Print("\n" + c.prompt())

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if !scanner.Scan() {
			return scanner.Err()
		}

		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			fmt.Print(c.prompt())
			continue
		}

		// Built-in commands
		if text == "/quit" || text == "/exit" {
			fmt.Println("  bye.")
			return nil
		}

		if strings.HasPrefix(text, "/intent") {
			c.handleIntentCommand(text)
			fmt.Print(c.prompt())
			continue
		}

		if text == "/help" {
			fmt.Println("  /intent <name>  — set safety level (any name from the intent registry, or 'auto')")
			fmt.Println("  /intent                  — show current level")
			fmt.Println("  /quit                    — exit")
			fmt.Print(c.prompt())
			continue
		}

		inbox <- channels.InboundMessage{
			ChannelID:  "cli",
			SessionID:  c.sessionID,
			SenderID:   "local",
			SenderName: "user",
			Text:       text,
			Timestamp:  time.Now(),
		}
	}
}

func (c *Channel) handleIntentCommand(text string) {
	parts := strings.Fields(text)
	if len(parts) == 1 {
		c.mu.RLock()
		intent := c.intent
		c.mu.RUnlock()
		if intent == "" {
			fmt.Println("  intent: auto (planner infers)")
		} else {
			fmt.Printf("  intent: %s\n", intent)
		}
		return
	}

	level := parts[1]
	if level == "auto" {
		c.mu.Lock()
		c.intent = ""
		c.mu.Unlock()
		fmt.Println("  intent set to: auto (planner infers)")
		return
	}
	// Validate against the registry if a lister is wired. Otherwise accept
	// any name and let the agent validate downstream.
	if c.intentLister != nil {
		valid := false
		names := c.intentLister()
		for _, n := range names {
			if n == level {
				valid = true
				break
			}
		}
		if !valid {
			fmt.Printf("  unknown intent: %s (use %s, or auto)\n", level, strings.Join(names, ", "))
			return
		}
	}
	c.mu.Lock()
	c.intent = level
	c.mu.Unlock()
	fmt.Printf("  intent set to: %s\n", level)
}

// Send prints the outbound message to stdout.
func (c *Channel) Send(_ context.Context, msg channels.OutboundMessage) error {
	fmt.Printf("\n%s\n\n%s", msg.Text, c.prompt())
	return nil
}

func (c *Channel) Close() error { return nil }
