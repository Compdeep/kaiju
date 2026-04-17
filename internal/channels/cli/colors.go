package cli

import "fmt"

// ANSI escape codes
const (
	reset     = "\033[0m"
	bold      = "\033[1m"
	dim       = "\033[2m"
	italic    = "\033[3m"
	underline = "\033[4m"

	// Cursor control
	clearLine  = "\033[2K"
	cursorUp   = "\033[1A"
	cursorDown = "\033[1B"
	saveCur    = "\033[s"
	restorCur  = "\033[u"

	// Alt screen buffer (xterm) — preserves main screen on exit
	altEnter    = "\033[?1049h"
	altExit     = "\033[?1049l"
	clearScreen = "\033[2J"
	cursorHome  = "\033[H"
	hideCursor  = "\033[?25l"
	showCursor  = "\033[?25h"

	// Alternate scroll mode. When ON (default in many terminals during raw
	// mode), mouse wheel is translated into arrow keys and fed to the app —
	// which makes wheel-up fire history recall in our line editor. Turn it
	// OFF so the terminal handles wheel natively for scrollback.
	altScrollOff = "\033[?1007l"
)

// Theme holds color codes for a visual theme.
type Theme struct {
	Name string

	// Prompt
	PromptBrand  string // "kaiju" text
	PromptArrow  string // ">" arrow
	PromptIntent string // [operate] tag

	// Messages
	UserLabel     string // "you" label
	AssistantLabel string // "kaiju" label
	UserText      string // user message body
	AssistantText string // assistant response body

	// Trace
	TraceLabel string // node labels
	TraceOK    string // green check
	TraceFail  string // red X
	TraceInfo  string // dim info text
	TraceTool  string // tool name highlight

	// Status line
	StatusBg   string // background for inline status
	StatusText string // text on status line

	// System
	Accent   string
	Muted    string
	Error    string
	Success  string
	Warning  string
}

var darkTheme = Theme{
	Name:           "dark",
	PromptBrand:    "\033[38;5;141m", // purple
	PromptArrow:    "\033[38;5;245m", // gray
	PromptIntent:   "\033[38;5;110m", // blue
	UserLabel:      "\033[38;5;45m", // cyan
	AssistantLabel: "\033[38;5;141m", // purple
	UserText:       "\033[38;5;253m", // bright white
	AssistantText:  "\033[38;5;253m", // bright white
	TraceLabel:     "\033[38;5;245m", // gray
	TraceOK:        "\033[38;5;114m", // green
	TraceFail:      "\033[38;5;204m", // red
	TraceInfo:      "\033[38;5;240m", // dark gray
	TraceTool:      "\033[38;5;110m", // blue
	StatusBg:       "\033[48;5;236m", // dark bg
	StatusText:     "\033[38;5;245m", // gray text
	Accent:         "\033[38;5;141m", // purple
	Muted:          "\033[38;5;240m", // dark gray
	Error:          "\033[38;5;204m", // red
	Success:        "\033[38;5;114m", // green
	Warning:        "\033[38;5;221m", // amber
}

var lightTheme = Theme{
	Name:           "light",
	PromptBrand:    "\033[38;5;62m",  // deep purple
	PromptArrow:    "\033[38;5;245m", // gray
	PromptIntent:   "\033[38;5;25m",  // deep blue
	UserLabel:      "\033[38;5;31m", // dark cyan
	AssistantLabel: "\033[38;5;62m",  // deep purple
	UserText:       "\033[38;5;235m", // dark
	AssistantText:  "\033[38;5;235m", // dark
	TraceLabel:     "\033[38;5;245m", // gray
	TraceOK:        "\033[38;5;28m",  // dark green
	TraceFail:      "\033[38;5;160m", // dark red
	TraceInfo:      "\033[38;5;249m", // light gray
	TraceTool:      "\033[38;5;25m",  // deep blue
	StatusBg:       "\033[48;5;254m", // light bg
	StatusText:     "\033[38;5;245m", // gray text
	Accent:         "\033[38;5;62m",  // deep purple
	Muted:          "\033[38;5;249m", // light gray
	Error:          "\033[38;5;160m", // dark red
	Success:        "\033[38;5;28m",  // dark green
	Warning:        "\033[38;5;172m", // amber
}

// clr wraps text in a color code + reset
func clr(color, text string) string {
	return color + text + reset
}

// cb wraps text in bold + color + reset
func cb(color, text string) string {
	return bold + color + text + reset
}

// statusLine renders a single-line status that can be overwritten
func statusLine(theme *Theme, text string) string {
	return fmt.Sprintf("\r%s%s %s %s", clearLine, theme.StatusBg, theme.StatusText+text, reset)
}
