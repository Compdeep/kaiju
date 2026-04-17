package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Compdeep/kaiju/internal/channels"
	"golang.org/x/term"
)

// SessionInfo is a summary of a session for display.
type SessionInfo struct {
	ID    string
	Title string
	Age   string
}

// statusEntry is a single annotation (debug / trace line) captured during
// a query. Kept in a bounded ring so the expanded (Ctrl+O) view can render
// the full history of the current + prior queries.
type statusEntry struct {
	ts   time.Time
	text string
}

const statusBufferSize = 500

// Channel implements an interactive CLI channel (stdin/stdout).
type Channel struct {
	sessionID      string
	mu             sync.RWMutex
	intent         string
	intentLister   func() []string
	sessionCreator func() (string, error)                 // creates new session, returns ID
	sessionLister  func(limit int) ([]SessionInfo, error) // lists recent sessions
	sessionLoader  func(id string) error                  // switches to a session
	theme          *Theme

	// Rendering. All terminal writes take renderMu to serialize output across
	// goroutines (Start loop, Send callback, LogWriter from log.SetOutput).
	renderMu     sync.Mutex
	inlineRows   int  // rows consumed by the current collapsed status block
	inlineActive bool // true between user submit and Send — when status may render inline
	altActive    bool // true while the expanded alt-screen view is on

	// Status buffer (ring). All debug / status lines go here, regardless of
	// whether they render inline. Not part of LLM context — annotations only.
	bufMu     sync.Mutex
	statusBuf []statusEntry

	pump *keyPump
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

// showPrompt prints the prompt on a fresh line. Cursor ends at end of prompt.
func (c *Channel) showPrompt() {
	c.renderMu.Lock()
	defer c.renderMu.Unlock()
	fmt.Printf("\n%s", c.prompt())
}

// Start reads lines from stdin and sends them as inbound messages.
func (c *Channel) Start(ctx context.Context, inbox chan<- channels.InboundMessage) error {
	// Key pump owns raw mode for the lifetime of the channel. It intercepts
	// Ctrl+O (alt-screen toggle) and forwards other bytes to LineReader.
	pump, err := startKeyPump(c)
	if err != nil {
		return fmt.Errorf("cli: init key pump: %w", err)
	}
	defer pump.Close()
	c.pump = pump

	rl := NewLineReader(HistoryFilePath(), c.complete, pump.ReadByte)

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

		rl.SetPrompt(c.prompt())
		text, err := rl.Read()
		if err != nil {
			if errors.Is(err, ErrInterrupt) {
				c.showPrompt()
				continue
			}
			if errors.Is(err, io.EOF) {
				fmt.Printf("  %s\n", clr(t.Muted, "bye."))
				return nil
			}
			return err
		}

		text = strings.TrimSpace(text)
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
			// /trace is a fallback for Ctrl+O — toggles the expanded alt-screen view.
			c.toggleAltScreen()
			continue
		}

		// User message — rewrite the just-typed prompt line as "you: <text>"
		// so the message stays visible in scrollback, then arm inline status.
		c.renderMu.Lock()
		c.rewritePromptAsUserLineLocked(text)
		c.inlineActive = true
		c.inlineRows = 0
		c.renderMu.Unlock()

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
		{"/trace (Ctrl+O)", "toggle expanded trace view"},
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

// Send prints the outbound message to stdout. If the expanded alt-screen view
// is currently showing, we leave it first so the response renders on the main
// screen.
func (c *Channel) Send(_ context.Context, msg channels.OutboundMessage) error {
	c.mu.RLock()
	t := c.theme
	c.mu.RUnlock()

	c.renderMu.Lock()
	defer c.renderMu.Unlock()

	// If user was in the expanded view when the response arrived, pop back
	// to the main screen before rendering the answer.
	if c.altActive {
		c.leaveAltScreenLocked()
	}
	c.clearInlineLocked()
	c.inlineActive = false

	// Label on its own line, then each response line. No leading newline —
	// clearInlineLocked left the cursor at the start of a clean row.
	fmt.Printf("  %s\n", cb(t.AssistantLabel, "kaiju"))
	for _, line := range strings.Split(msg.Text, "\n") {
		if line == "" {
			fmt.Println()
		} else {
			fmt.Printf("  %s\n", clr(t.AssistantText, line))
		}
	}
	fmt.Printf("\n%s", c.prompt())
	return nil
}

// StatusUpdate appends text to the buffer (always) and renders it inline
// when a query is in flight. Empty text clears the inline region without
// touching the buffer.
func (c *Channel) StatusUpdate(text string) {
	if text == "" {
		c.renderMu.Lock()
		c.clearInlineLocked()
		c.renderMu.Unlock()
		return
	}

	c.pushStatus(text)

	c.renderMu.Lock()
	defer c.renderMu.Unlock()

	// Mirror into the alt screen when it's showing.
	if c.altActive {
		c.appendAltLineLocked(text)
		return
	}
	if !c.inlineActive {
		// Buffered only — no active query, nothing to render.
		return
	}
	c.renderInlineLocked(text)
}

// TraceNode records a node completion as a status line. Same rendering path
// as StatusUpdate — stored in buffer, shown inline while a query runs.
func (c *Channel) TraceNode(tool, tag, state string, ms int64) {
	c.mu.RLock()
	t := c.theme
	c.mu.RUnlock()

	var icon string
	switch state {
	case "failed":
		icon = clr(t.TraceFail, "✗")
	case "skipped":
		icon = clr(t.Muted, "—")
	default:
		icon = clr(t.TraceOK, "✓")
	}
	line := fmt.Sprintf("%s %s %s %dms", icon, tool, tag, ms)
	plain := fmt.Sprintf("%s %s %s %dms", stateGlyph(state), tool, tag, ms)

	c.pushStatus(plain)

	c.renderMu.Lock()
	defer c.renderMu.Unlock()
	if c.altActive {
		c.appendAltStyledLocked(line)
		return
	}
	if !c.inlineActive {
		return
	}
	c.renderInlineStyledLocked(line)
}

func stateGlyph(state string) string {
	switch state {
	case "failed":
		return "x"
	case "skipped":
		return "-"
	default:
		return "+"
	}
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

// complete returns tab-completion candidates for the current input line.
// Completes slash commands on the first word, and known argument values
// (intent names, theme names) on the second word.
func (c *Channel) complete(line string, pos int) (int, []string) {
	if pos > len(line) {
		pos = len(line)
	}
	prefix := line[:pos]

	start := strings.LastIndexByte(prefix, ' ') + 1
	needle := prefix[start:]

	head := strings.TrimSpace(prefix[:start])
	if head == "" {
		cmds := []string{"/help", "/quit", "/exit", "/new", "/resume", "/intent", "/theme", "/trace"}
		var out []string
		for _, cmd := range cmds {
			if strings.HasPrefix(cmd, needle) {
				out = append(out, cmd)
			}
		}
		return start, out
	}

	fields := strings.Fields(head)
	var options []string
	switch fields[0] {
	case "/intent":
		options = []string{"auto"}
		if c.intentLister != nil {
			options = append(options, c.intentLister()...)
		}
	case "/theme":
		options = []string{"dark", "light"}
	}
	var out []string
	for _, opt := range options {
		if strings.HasPrefix(opt, needle) {
			out = append(out, opt)
		}
	}
	return start, out
}

func (c *Channel) Close() error { return nil }

// --- Status buffer ---------------------------------------------------------

func (c *Channel) pushStatus(text string) {
	c.bufMu.Lock()
	c.statusBuf = append(c.statusBuf, statusEntry{ts: time.Now(), text: text})
	if n := len(c.statusBuf); n > statusBufferSize {
		c.statusBuf = c.statusBuf[n-statusBufferSize:]
	}
	c.bufMu.Unlock()
}

func (c *Channel) snapshotBuffer() []statusEntry {
	c.bufMu.Lock()
	defer c.bufMu.Unlock()
	out := make([]statusEntry, len(c.statusBuf))
	copy(out, c.statusBuf)
	return out
}

// rewritePromptAsUserLineLocked replaces the "kaiju > <text>" line(s) the
// user just typed with "you: <text>". LineReader already printed a trailing
// \n after Enter, so the cursor is one row below the last wrap row. Move up
// through every wrap row, clear each, and emit the replacement.
// Caller holds renderMu.
func (c *Channel) rewritePromptAsUserLineLocked(text string) {
	t := c.theme
	cols := termCols()

	promptVis := visibleLen(c.prompt())
	rowsUsed := (promptVis + utf8.RuneCountInString(text) + cols - 1) / cols
	if rowsUsed < 1 {
		rowsUsed = 1
	}

	var b strings.Builder
	for i := 0; i < rowsUsed; i++ {
		b.WriteString(cursorUp)
		b.WriteString("\r")
		b.WriteString(clearLine)
	}
	fmt.Fprintf(&b, "  %s %s\n",
		cb(t.UserLabel, "you:"),
		clr(t.UserText, text))
	fmt.Fprint(os.Stdout, b.String())
}

// --- Inline rendering (collapsed mode) ------------------------------------

// renderInlineLocked renders text as the collapsed status block, overwriting
// the previous block (which may have wrapped to multiple rows). The last-
// rendered text is shown full size — we do not truncate.
//
// Invariant: before the call, the cursor is at the first column of the top
// row of the previous block (if any); after the call, cursor is at the end
// of the newly rendered content and inlineRows reflects its row count.
// Callers hold renderMu.
func (c *Channel) renderInlineLocked(text string) {
	t := c.theme
	styled := "  " + clr(t.TraceInfo, text)
	plainLen := 2 + utf8.RuneCountInString(text)
	c.paintInlineLocked(styled, plainLen)
}

// renderInlineStyledLocked renders pre-styled text (with its own ANSI codes).
// The visible length is computed by stripping escape sequences.
func (c *Channel) renderInlineStyledLocked(styled string) {
	plainLen := visibleLen(styled) + 2
	c.paintInlineLocked("  "+styled, plainLen)
}

// paintInlineLocked does the actual write. plainLen is the visible (no ANSI)
// character count so we can predict wrap rows.
func (c *Channel) paintInlineLocked(styled string, plainLen int) {
	cols := termCols()
	prevRows := c.inlineRows

	var b strings.Builder

	// Move to col 1 of the top row of the previous block, clear each row.
	b.WriteString("\r")
	if prevRows > 1 {
		fmt.Fprintf(&b, "\033[%dA", prevRows-1)
	}
	for i := 0; i < prevRows; i++ {
		b.WriteString(clearLine)
		if i < prevRows-1 {
			b.WriteString(cursorDown)
			b.WriteString("\r")
		}
	}
	if prevRows > 1 {
		// Back to top of the (now blank) region.
		fmt.Fprintf(&b, "\033[%dA", prevRows-1)
		b.WriteString("\r")
	}

	b.WriteString(styled)
	fmt.Fprint(os.Stdout, b.String())

	newRows := 1
	if cols > 0 {
		newRows = (plainLen + cols - 1) / cols
		if newRows < 1 {
			newRows = 1
		}
	}
	c.inlineRows = newRows
}

// clearInlineLocked clears the collapsed block and leaves the cursor at col 1
// of the top row of the (now blank) region. Caller holds renderMu.
func (c *Channel) clearInlineLocked() {
	prev := c.inlineRows
	if prev == 0 {
		// Cursor is already at a fresh column-1 row.
		fmt.Fprint(os.Stdout, "\r")
		return
	}

	var b strings.Builder
	b.WriteString("\r")
	if prev > 1 {
		fmt.Fprintf(&b, "\033[%dA", prev-1)
	}
	for i := 0; i < prev; i++ {
		b.WriteString(clearLine)
		if i < prev-1 {
			b.WriteString(cursorDown)
			b.WriteString("\r")
		}
	}
	if prev > 1 {
		fmt.Fprintf(&b, "\033[%dA", prev-1)
		b.WriteString("\r")
	}
	fmt.Fprint(os.Stdout, b.String())
	c.inlineRows = 0
}

// --- Alt screen (expanded view) -------------------------------------------

func (c *Channel) toggleAltScreen() {
	c.renderMu.Lock()
	defer c.renderMu.Unlock()
	if c.altActive {
		c.leaveAltScreenLocked()
	} else {
		c.enterAltScreenLocked()
	}
}

func (c *Channel) enterAltScreenLocked() {
	if c.altActive {
		return
	}
	c.altActive = true
	t := c.theme
	fmt.Fprint(os.Stdout, altEnter, cursorHome, clearScreen)
	fmt.Fprintf(os.Stdout, "  %s  %s\n",
		cb(t.Accent, "trace"),
		clr(t.Muted, "— Ctrl+O or /trace to close"))
	fmt.Fprintln(os.Stdout)

	for _, e := range c.snapshotBuffer() {
		c.writeAltEntryLocked(e.text)
	}
}

func (c *Channel) leaveAltScreenLocked() {
	if !c.altActive {
		return
	}
	c.altActive = false
	// Exiting the alt buffer can flip alternate scroll mode back on in some
	// terminals — re-assert off so wheel keeps using native scrollback.
	fmt.Fprint(os.Stdout, altExit, altScrollOff)
	// Main screen restored to its prior state — inlineRows is still valid
	// because we never touched main while alt was active.
}

func (c *Channel) appendAltLineLocked(text string) {
	t := c.theme
	fmt.Fprintf(os.Stdout, "  %s %s\n",
		clr(t.TraceTool, "▸"),
		clr(t.TraceInfo, text))
}

func (c *Channel) appendAltStyledLocked(styled string) {
	fmt.Fprintf(os.Stdout, "  %s\n", styled)
}

func (c *Channel) writeAltEntryLocked(text string) {
	t := c.theme
	fmt.Fprintf(os.Stdout, "  %s %s\n",
		clr(t.TraceTool, "▸"),
		clr(t.TraceInfo, text))
}

// --- Utilities -------------------------------------------------------------

// ansiRe strips CSI escape sequences (color / cursor). We use it to compute
// visible width for wrap accounting.
var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]`)

func visibleLen(s string) int {
	return utf8.RuneCountInString(ansiRe.ReplaceAllString(s, ""))
}

func termCols() int {
	cols, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || cols <= 0 {
		return 80
	}
	return cols
}

// --- Key pump --------------------------------------------------------------

// keyPump owns raw-mode stdin for the lifetime of the CLI channel. It reads
// bytes one at a time, intercepts Ctrl+O (0x0f) to toggle the expanded view,
// and forwards everything else to a buffered channel that LineReader drains
// via ReadByte.
type keyPump struct {
	ch    *Channel
	bytes chan byte
	stop  chan struct{}

	fd       int
	rawState *term.State
}

func startKeyPump(ch *Channel) (*keyPump, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return nil, fmt.Errorf("stdin is not a terminal")
	}
	state, err := term.MakeRaw(fd)
	if err != nil {
		return nil, err
	}
	// MakeRaw disables OPOST; re-enable so \n → \r\n works for concurrent
	// writers (Send, StatusUpdate).
	if err := enableOPOST(fd); err != nil {
		_ = term.Restore(fd, state)
		return nil, err
	}
	// Keep mouse wheel out of our input stream so the terminal can scroll
	// native scrollback instead of firing history recall.
	fmt.Fprint(os.Stdout, altScrollOff)
	p := &keyPump{
		ch:       ch,
		bytes:    make(chan byte, 256),
		stop:     make(chan struct{}),
		fd:       fd,
		rawState: state,
	}
	go p.run()
	return p, nil
}

func (p *keyPump) run() {
	var buf [1]byte
	for {
		n, err := os.Stdin.Read(buf[:])
		if err != nil {
			if err == io.EOF {
				close(p.bytes)
				return
			}
			select {
			case <-p.stop:
				return
			default:
			}
			continue
		}
		if n == 0 {
			continue
		}
		b := buf[0]
		if b == 0x0f { // Ctrl+O
			p.ch.toggleAltScreen()
			continue
		}
		select {
		case p.bytes <- b:
		case <-p.stop:
			return
		}
	}
}

// ReadByte returns the next non-intercepted input byte. Blocks until one
// is available or the pump is closed.
func (p *keyPump) ReadByte() (byte, error) {
	b, ok := <-p.bytes
	if !ok {
		return 0, io.EOF
	}
	return b, nil
}

// Close restores terminal state. The reader goroutine itself leaks until
// the process exits (stdin.Read is uninterruptible), but that is safe.
func (p *keyPump) Close() error {
	select {
	case <-p.stop:
	default:
		close(p.stop)
	}
	return term.Restore(p.fd, p.rawState)
}

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
