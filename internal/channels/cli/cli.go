package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Compdeep/kaiju/internal/channels"
)

// SessionInfo is a summary of a session for display.
type SessionInfo struct {
	ID    string
	Title string
	Age   string
}

// Channel implements an interactive CLI channel (stdin/stdout).
type Channel struct {
	sessionID      string
	mu             sync.RWMutex
	intent         string
	intentLister   func() []string
	sessionCreator func() (string, error)               // creates new session, returns ID
	sessionLister  func(limit int) ([]SessionInfo, error) // lists recent sessions
	sessionLoader  func(id string) error                 // switches to a session
	theme          *Theme
	traceExpand    bool // Ctrl+O toggles full trace view
}

// New creates a CLI channel with the dark theme.
func New() *Channel {
	return &Channel{
		sessionID: "cli-local",
		theme:     &darkTheme,
	}
}

// SetIntentLister injects a function that returns the current list of valid
// intent names from the agent's intent registry.
func (c *Channel) SetIntentLister(fn func() []string) {
	c.intentLister = fn
}

// SetSessionHandlers injects callbacks for session management.
func (c *Channel) SetSessionHandlers(
	creator func() (string, error),
	lister func(limit int) ([]SessionInfo, error),
	loader func(id string) error,
) {
	c.sessionCreator = creator
	c.sessionLister = lister
	c.sessionLoader = loader
}

// SessionID returns the current session ID.
func (c *Channel) SessionID() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.sessionID
}

// SetSessionID updates the current session ID.
func (c *Channel) SetSessionID(id string) {
	c.mu.Lock()
	c.sessionID = id
	c.mu.Unlock()
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
	t := c.theme
	c.mu.RUnlock()

	brand := cb(t.PromptBrand, "kaiju")
	arrow := clr(t.PromptArrow, ">")

	if intent != "" {
		tag := clr(t.PromptIntent, "["+intent+"]")
		return fmt.Sprintf("  %s %s %s ", brand, tag, arrow)
	}
	return fmt.Sprintf("  %s %s ", brand, arrow)
}

// showPrompt prints a blank status line followed by the prompt.
// The blank line acts as a reserved slot for StatusUpdate to write into
// without disturbing the user's input on the prompt line.
func (c *Channel) showPrompt() {
	fmt.Printf("\n%s", c.prompt())
}

// Start reads lines from stdin and sends them as inbound messages.
func (c *Channel) Start(ctx context.Context, inbox chan<- channels.InboundMessage) error {
	scanner := bufio.NewScanner(os.Stdin)

	// Welcome banner
	t := c.theme
	c.printBanner()
	c.showPrompt()

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
			c.showPrompt()
			continue
		}

		// Built-in commands
		switch {
		case text == "/quit" || text == "/exit":
			fmt.Printf("  %s\n", clr(t.Muted, "bye."))
			return nil

		case text == "/help":
			c.printHelp()
			c.showPrompt()
			continue

		case strings.HasPrefix(text, "/intent"):
			c.handleIntentCommand(text)
			c.showPrompt()
			continue

		case strings.HasPrefix(text, "/theme"):
			c.handleThemeCommand(text)
			c.showPrompt()
			continue

		case text == "/new":
			c.handleNewSession()
			c.showPrompt()
			continue

		case text == "/resume" || strings.HasPrefix(text, "/resume "):
			c.handleResumeSession(text)
			c.showPrompt()
			continue

		case text == "/trace":
			c.mu.Lock()
			c.traceExpand = !c.traceExpand
			c.mu.Unlock()
			if c.traceExpand {
				fmt.Printf("  %s\n", clr(t.Success, "trace: expanded"))
			} else {
				fmt.Printf("  %s\n", clr(t.Muted, "trace: inline"))
			}
			c.showPrompt()
			continue
		}

		// User message
		fmt.Printf("  %s %s\n", cb(t.UserLabel, "you"), clr(t.Muted, "·"))

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

func (c *Channel) printHelp() {
	t := c.theme
	cmds := []struct{ cmd, desc string }{
		{"/new", "start a new session"},
		{"/resume", "list recent sessions"},
		{"/resume <id>", "resume a session by ID"},
		{"/intent <name>", "set safety level (or 'auto')"},
		{"/intent", "show current level"},
		{"/theme dark|light", "switch color theme"},
		{"/trace", "toggle expanded/inline trace"},
		{"/quit", "exit"},
	}
	fmt.Println()
	for _, cmd := range cmds {
		fmt.Printf("  %s  %s\n", cb(t.Accent, fmt.Sprintf("%-20s", cmd.cmd)), clr(t.Muted, cmd.desc))
	}
	fmt.Println()
}

func (c *Channel) handleNewSession() {
	t := c.theme
	if c.sessionCreator == nil {
		fmt.Printf("  %s\n", clr(t.Error, "sessions not available (no database)"))
		return
	}
	id, err := c.sessionCreator()
	if err != nil {
		fmt.Printf("  %s %s\n", clr(t.Error, "error:"), clr(t.Muted, err.Error()))
		return
	}
	c.mu.Lock()
	c.sessionID = id
	c.mu.Unlock()
	fmt.Printf("  %s %s\n", clr(t.Muted, "session:"), cb(t.Accent, id[:8]))
}

func (c *Channel) handleResumeSession(text string) {
	t := c.theme
	parts := strings.Fields(text)

	if len(parts) >= 2 {
		// /resume <id> — switch directly (prefix match)
		id := parts[1]
		if c.sessionLoader != nil {
			if err := c.sessionLoader(id); err != nil {
				fmt.Printf("  %s %s\n", clr(t.Error, "error:"), clr(t.Muted, err.Error()))
				return
			}
		}
		// sessionLoader already called SetSessionID with the full ID
		c.mu.RLock()
		resolvedID := c.sessionID
		c.mu.RUnlock()
		short := resolvedID
		if len(short) > 8 {
			short = short[:8]
		}
		fmt.Printf("  %s %s\n", clr(t.Muted, "resumed:"), cb(t.Accent, short))
		return
	}

	// /resume — list recent sessions
	if c.sessionLister == nil {
		fmt.Printf("  %s\n", clr(t.Error, "sessions not available (no database)"))
		return
	}
	sessions, err := c.sessionLister(10)
	if err != nil {
		fmt.Printf("  %s %s\n", clr(t.Error, "error:"), clr(t.Muted, err.Error()))
		return
	}
	if len(sessions) == 0 {
		fmt.Printf("  %s\n", clr(t.Muted, "no sessions found"))
		return
	}
	// Filter out the current session
	c.mu.RLock()
	currentID := c.sessionID
	c.mu.RUnlock()
	var filtered []SessionInfo
	for _, s := range sessions {
		if s.ID != currentID {
			filtered = append(filtered, s)
		}
	}
	if len(filtered) == 0 {
		fmt.Printf("  %s\n", clr(t.Muted, "no other sessions found"))
		return
	}

	fmt.Println()
	for _, s := range filtered {
		id := s.ID
		if len(id) > 8 {
			id = id[:8]
		}
		fmt.Printf("  %s  %s  %s\n", cb(t.Accent, id), clr(t.Muted, s.Age), clr(t.AssistantText, s.Title))
	}
	fmt.Printf("\n  %s\n", clr(t.Muted, "use /resume <id> to switch"))
	fmt.Println()
}

func (c *Channel) handleThemeCommand(text string) {
	parts := strings.Fields(text)
	t := c.theme
	if len(parts) < 2 {
		fmt.Printf("  %s %s\n", clr(t.Muted, "theme:"), cb(t.Accent, t.Name))
		return
	}
	switch parts[1] {
	case "dark":
		c.mu.Lock()
		c.theme = &darkTheme
		c.mu.Unlock()
		fmt.Printf("  %s\n", clr(darkTheme.Muted, "theme: dark"))
	case "light":
		c.mu.Lock()
		c.theme = &lightTheme
		c.mu.Unlock()
		fmt.Printf("  %s\n", clr(lightTheme.Muted, "theme: light"))
	default:
		fmt.Printf("  %s\n", clr(t.Error, "unknown theme: "+parts[1]+" (use dark or light)"))
	}
}

func (c *Channel) handleIntentCommand(text string) {
	parts := strings.Fields(text)
	t := c.theme
	if len(parts) == 1 {
		c.mu.RLock()
		intent := c.intent
		c.mu.RUnlock()
		if intent == "" {
			fmt.Printf("  %s %s\n", clr(t.Muted, "intent:"), clr(t.Accent, "auto"))
		} else {
			fmt.Printf("  %s %s\n", clr(t.Muted, "intent:"), cb(t.Accent, intent))
		}
		return
	}

	level := parts[1]
	if level == "auto" {
		c.mu.Lock()
		c.intent = ""
		c.mu.Unlock()
		fmt.Printf("  %s %s\n", clr(t.Muted, "intent:"), clr(t.Accent, "auto"))
		return
	}

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
			fmt.Printf("  %s %s\n", clr(t.Error, "unknown:"), clr(t.Muted, level+" (use "+strings.Join(names, ", ")+", or auto)"))
			return
		}
	}
	c.mu.Lock()
	c.intent = level
	c.mu.Unlock()
	fmt.Printf("  %s %s\n", clr(t.Muted, "intent:"), cb(t.Accent, level))
}

// Send prints the outbound message to stdout with colors.
func (c *Channel) Send(_ context.Context, msg channels.OutboundMessage) error {
	c.mu.RLock()
	t := c.theme
	c.mu.RUnlock()

	// Clear the status line above the prompt before printing response
	c.StatusUpdate("")
	fmt.Printf("\n  %s\n", cb(t.AssistantLabel, "kaiju"))
	// Indent response lines for visual separation
	for _, line := range strings.Split(msg.Text, "\n") {
		if line == "" {
			fmt.Println()
		} else {
			fmt.Printf("  %s\n", clr(t.AssistantText, line))
		}
	}
	c.showPrompt()
	return nil
}

// StatusUpdate prints a status line ABOVE the prompt without disturbing user input.
// Uses cursor save/restore to write on the line above, then return to the prompt.
// Call with empty string to clear the status line.
func (c *Channel) StatusUpdate(text string) {
	c.mu.RLock()
	t := c.theme
	expand := c.traceExpand
	c.mu.RUnlock()

	if text == "" {
		// Clear the status line above the prompt
		fmt.Printf("%s%s\r%s%s", saveCur, cursorUp, clearLine, restorCur)
		return
	}

	if expand {
		// Expanded: print each status on its own line (above prompt).
		// Scroll the prompt down by inserting a line above it.
		fmt.Printf("%s%s\r%s  %s %s\n%s",
			saveCur, cursorUp, clearLine,
			clr(t.TraceTool, "▸"), clr(t.TraceInfo, text),
			restorCur)
	} else {
		// Inline: overwrite the single status line above the prompt
		fmt.Printf("%s%s\r%s  %s%s",
			saveCur, cursorUp, clearLine,
			clr(t.TraceInfo, text),
			restorCur)
	}
}

// TraceNode prints a single node completion in the trace.
func (c *Channel) TraceNode(tool, tag, state string, ms int64) {
	c.mu.RLock()
	t := c.theme
	expand := c.traceExpand
	c.mu.RUnlock()

	if !expand {
		// Inline mode: just update the status line
		icon := clr(t.TraceOK, "✓")
		if state == "failed" {
			icon = clr(t.TraceFail, "✗")
		}
		fmt.Print(statusLine(t, fmt.Sprintf("%s %s %s %dms", icon, tool, tag, ms)))
		return
	}

	// Expanded mode: full line per node
	icon := clr(t.TraceOK, "✓")
	if state == "failed" {
		icon = clr(t.TraceFail, "✗")
	} else if state == "skipped" {
		icon = clr(t.Muted, "—")
	}
	fmt.Printf("  %s %s %s %s\n",
		icon,
		clr(t.TraceTool, fmt.Sprintf("%-10s", tool)),
		clr(t.TraceLabel, tag),
		clr(t.TraceInfo, fmt.Sprintf("%dms", ms)),
	)
}

func (c *Channel) printBanner() {
	c.mu.RLock()
	t := c.theme
	c.mu.RUnlock()

	g := []string{
		"\033[38;5;55m",  // 0  deep purple
		"\033[38;5;92m",  // 1  purple
		"\033[38;5;128m", // 2  magenta
		"\033[38;5;134m", // 3  purple-pink
		"\033[38;5;169m", // 4  pink
		"\033[38;5;176m", // 5  light pink
		"\033[38;5;141m", // 6  lavender
		"\033[38;5;105m", // 7  blue-purple
		"\033[38;5;69m",  // 8  blue
		"\033[38;5;75m",  // 9  cyan
		"\033[38;5;81m",  // 10 bright cyan
		"\033[38;5;213m", // 11 hot pink (eyes)
	}

	// KAIJU outline — each char gets colored based on its column position
	// to create a left-to-right neon gradient sweep
	name := []string{
		` ____  __.  _____  .___     ____.____ ___ `,
		`|    |/ _| /  _  \ |   |   |    |    |   \`,
		`|      <  /  /_\  \|   |   |    |    |   /`,
		`|    |  \/    |    \   /\__|    |    |  / `,
		`|____|__ \____|__  /___\________|______/  `,
		`        \/       \/                       `,
	}

	// Synthwave neon gradient — cyan → purple → hot pink → magenta
	neon := []string{
		"\033[38;5;51m",  // electric cyan
		"\033[38;5;45m",  // cyan
		"\033[38;5;39m",  // blue-cyan
		"\033[38;5;63m",  // blue-purple
		"\033[38;5;99m",  // purple
		"\033[38;5;135m", // bright purple
		"\033[38;5;171m", // magenta
		"\033[38;5;207m", // hot pink
		"\033[38;5;213m", // neon pink
		"\033[38;5;219m", // light pink
		"\033[38;5;213m", // neon pink (bounce)
		"\033[38;5;207m", // hot pink
		"\033[38;5;171m", // magenta
		"\033[38;5;135m", // bright purple
		"\033[38;5;99m",  // purple
		"\033[38;5;63m",  // blue-purple
	}

	// Block fill characters for retro feel — structural chars get blocks
	fillMap := map[byte]string{
		'|': "█", '_': "▄", '/': "╱", '\\': "╲", '<': "◄",
		'.': "░", ':': "░", '^': "▀",
	}

	fmt.Println()
	fmt.Println()

	for _, line := range name {
		colored := ""
		for j := 0; j < len(line); j++ {
			ch := line[j]
			gi := neon[j%len(neon)]

			if ch == ' ' {
				colored += " "
				continue
			}
			if fill, ok := fillMap[ch]; ok {
				colored += gi + bold + fill + reset
			} else {
				colored += gi + bold + string(ch) + reset
			}
		}
		fmt.Printf("  %s\n", colored)
	}

	fmt.Println()
	fmt.Printf("  %s%s%s  %s%s%s\n",
		g[5], "▓▒░", reset,
		clr(t.Accent, "executive kernel"),
		clr(t.Muted, " · "),
		clr(t.Muted, "v0.3"),
	)
	fmt.Printf("  %s\n", clr(t.Muted, "/help · /theme dark|light · /quit"))
	fmt.Println()
}

func (c *Channel) Close() error { return nil }

// logTimestampRe strips Go's default log timestamp prefix (e.g. "2026/04/06 22:50:16 ").
var logTimestampRe = regexp.MustCompile(`^\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2} `)

// logTagRe strips bracket tags like [dag], [agent], [cli].
var logTagRe = regexp.MustCompile(`^\[[\w-]+\] `)

// LogWriter returns an io.Writer that routes log output through StatusUpdate.
// Use with log.SetOutput(cliCh.LogWriter()) to keep log lines from polluting the chat.
func (c *Channel) LogWriter() *cliLogWriter {
	return &cliLogWriter{ch: c}
}

type cliLogWriter struct {
	ch *Channel
}

func (w *cliLogWriter) Write(p []byte) (int, error) {
	line := strings.TrimRight(string(p), "\n")

	// Strip timestamp
	line = logTimestampRe.ReplaceAllString(line, "")
	// Strip [tag] prefix
	line = logTagRe.ReplaceAllString(line, "")

	if line == "" {
		return len(p), nil
	}

	w.ch.StatusUpdate(line)
	return len(p), nil
}
