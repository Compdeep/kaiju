package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// ErrInterrupt is returned by LineReader.Read when the user presses Ctrl+C.
var ErrInterrupt = errors.New("interrupt")

// CompleteFunc returns the byte index where the token under the cursor begins
// and the candidate replacements for that token. Return nil candidates for no
// completion. pos is the cursor byte offset within line.
type CompleteFunc func(line string, pos int) (start int, candidates []string)

// LineReader is a line editor with history and tab completion. It owns raw
// mode ONLY for the duration of Read — between reads the terminal stays in
// cooked mode so the terminal emulator handles mouse wheel / scrollback
// natively. Ctrl+O during Read fires onToggleAlt (if set).
type LineReader struct {
	historyFile string
	history     []string
	complete    CompleteFunc
	onToggleAlt func() // optional; called on Ctrl+O inside Read

	mu     sync.Mutex
	prompt string
}

func NewLineReader(historyFile string, complete CompleteFunc, onToggleAlt func()) *LineReader {
	lr := &LineReader{
		historyFile: historyFile,
		complete:    complete,
		onToggleAlt: onToggleAlt,
	}
	lr.loadHistory()
	return lr
}

func readByte() (byte, error) {
	var b [1]byte
	_, err := os.Stdin.Read(b[:])
	return b[0], err
}

func (lr *LineReader) SetPrompt(p string) {
	lr.mu.Lock()
	lr.prompt = p
	lr.mu.Unlock()
}

// Read blocks until the user submits a line (Enter), presses Ctrl+C (returns
// ErrInterrupt), or hits EOF (io.EOF). It takes raw mode for the duration
// and releases it on return, so the terminal handles mouse wheel for native
// scrollback while we're not actively reading.
func (lr *LineReader) Read() (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return readFallback()
	}
	state, err := term.MakeRaw(fd)
	if err != nil {
		return "", err
	}
	defer term.Restore(fd, state)
	if err := enableOPOST(fd); err != nil {
		return "", err
	}
	// Bracketed paste: let the terminal send ESC[200~…ESC[201~ around pasted
	// content so we can gulp it as one block instead of byte-by-byte.
	fmt.Fprint(os.Stdout, bracketPasteOn)
	defer fmt.Fprint(os.Stdout, bracketPasteOff)

	lr.mu.Lock()
	prompt := lr.prompt
	lr.mu.Unlock()

	var buf []rune
	pos := 0
	histIdx := len(lr.history)
	savedBuf := ""
	prevRows := 1 // rows the previous redraw occupied; clear this many before re-emitting

	redraw := func() {
		cols := termCols()
		promptVis := visibleLen(prompt)
		visLen := promptVis + utf8.RuneCountInString(string(buf))
		curRows := (visLen + cols - 1) / cols
		if curRows < 1 {
			curRows = 1
		}
		toClear := prevRows
		if curRows > toClear {
			toClear = curRows
		}

		var b strings.Builder
		// Cursor is somewhere on the bottom row of the previous render
		// (or one row below if an auto-wrap just happened). Normalise:
		// clear current row and every row above it within the render.
		b.WriteString("\r\x1b[K")
		for i := 1; i < toClear; i++ {
			b.WriteString("\x1b[A\r\x1b[K")
		}
		// Cursor now at col 1 of the top row of the cleared region.
		b.WriteString(prompt)
		b.WriteString(string(buf))
		trailing := len(buf) - pos
		if trailing > 0 {
			fmt.Fprintf(&b, "\x1b[%dD", trailing)
		}
		fmt.Fprint(os.Stdout, b.String())
		prevRows = curRows
	}

	for {
		b, err := readByte()
		if err != nil {
			if err == io.EOF {
				return "", io.EOF
			}
			return "", err
		}

		if b == 0x0f { // Ctrl+O — toggle expanded trace view
			if lr.onToggleAlt != nil {
				lr.onToggleAlt()
				redraw()
			}
			continue
		}

		switch {
		case b == '\r' || b == '\n':
			fmt.Fprint(os.Stdout, "\n")
			line := string(buf)
			lr.appendHistory(line)
			return line, nil

		case b == 0x03: // Ctrl+C
			fmt.Fprint(os.Stdout, "^C\n")
			return "", ErrInterrupt

		case b == 0x04: // Ctrl+D
			if len(buf) == 0 {
				fmt.Fprint(os.Stdout, "\n")
				return "", io.EOF
			}

		case b == 0x7f || b == 0x08: // backspace / DEL
			if pos > 0 {
				buf = append(buf[:pos-1], buf[pos:]...)
				pos--
				redraw()
			}

		case b == '\t':
			if lr.complete != nil {
				buf, pos = lr.tabComplete(buf, pos, prompt)
			}

		case b == 0x1b:
			seq, err := readCSI(readByte)
			if err != nil {
				continue
			}
			switch seq {
			case "A":
				if histIdx > 0 {
					if histIdx == len(lr.history) {
						savedBuf = string(buf)
					}
					histIdx--
					buf = []rune(lr.history[histIdx])
					pos = len(buf)
					redraw()
				}
			case "B":
				if histIdx < len(lr.history) {
					histIdx++
					if histIdx == len(lr.history) {
						buf = []rune(savedBuf)
					} else {
						buf = []rune(lr.history[histIdx])
					}
					pos = len(buf)
					redraw()
				}
			case "C":
				if pos < len(buf) {
					pos++
					fmt.Fprint(os.Stdout, "\x1b[C")
				}
			case "D":
				if pos > 0 {
					pos--
					fmt.Fprint(os.Stdout, "\x1b[D")
				}
			case "200~":
				paste, err := readPaste(readByte)
				if err != nil {
					continue
				}
				runes := []rune(paste)
				if len(runes) == 0 {
					continue
				}
				next := make([]rune, 0, len(buf)+len(runes))
				next = append(next, buf[:pos]...)
				next = append(next, runes...)
				next = append(next, buf[pos:]...)
				buf = next
				pos += len(runes)
				redraw()
			}

		case b >= 0x20:
			r, err := readRune(b, readByte)
			if err != nil || r == utf8.RuneError {
				continue
			}
			next := make([]rune, 0, len(buf)+1)
			next = append(next, buf[:pos]...)
			next = append(next, r)
			next = append(next, buf[pos:]...)
			buf = next
			pos++
			redraw()
		}
	}
}

// readCSI consumes a CSI escape sequence body after the initial ESC. It
// expects the next byte to be '[' (otherwise returns an error so the caller
// can ignore the lone ESC). Returns the sequence without the leading "ESC["
// — e.g. "A", "D", "200~", "1;5D". The terminator (letter or '~') is kept.
func readCSI(next func() (byte, error)) (string, error) {
	b, err := next()
	if err != nil {
		return "", err
	}
	if b != '[' {
		return "", errors.New("not a CSI sequence")
	}
	var seq []byte
	for i := 0; i < 32; i++ {
		c, err := next()
		if err != nil {
			return "", err
		}
		seq = append(seq, c)
		if c == '~' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') {
			return string(seq), nil
		}
	}
	return "", errors.New("CSI sequence too long")
}

// readPaste consumes bytes until the bracketed-paste end marker (ESC[201~)
// and returns the pasted text with newlines and tabs flattened to spaces so
// the single-line editor stays coherent.
func readPaste(next func() (byte, error)) (string, error) {
	var body []byte
	for {
		b, err := next()
		if err != nil {
			return "", err
		}
		if b == 0x1b {
			seq, err := readCSI(next)
			if err != nil {
				// Malformed escape inside paste — drop it and keep going.
				continue
			}
			if seq == "201~" {
				break
			}
			// Any other escape inside the paste is discarded.
			continue
		}
		body = append(body, b)
	}
	// Flatten whitespace that would break the single-line editor.
	s := string(body)
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\t", " ")
	return s, nil
}

func readRune(first byte, next func() (byte, error)) (rune, error) {
	if first < 0x80 {
		return rune(first), nil
	}
	var total int
	switch {
	case first&0xE0 == 0xC0:
		total = 2
	case first&0xF0 == 0xE0:
		total = 3
	case first&0xF8 == 0xF0:
		total = 4
	default:
		return utf8.RuneError, nil
	}
	seq := make([]byte, total)
	seq[0] = first
	for i := 1; i < total; i++ {
		b, err := next()
		if err != nil {
			return utf8.RuneError, err
		}
		seq[i] = b
	}
	r, _ := utf8.DecodeRune(seq)
	return r, nil
}

func (lr *LineReader) tabComplete(buf []rune, pos int, prompt string) ([]rune, int) {
	line := string(buf)
	byteCursor := len(string(buf[:pos]))
	start, cands := lr.complete(line, byteCursor)
	if len(cands) == 0 {
		return buf, pos
	}

	current := line[start:byteCursor]

	if len(cands) == 1 {
		buf, pos = applyReplacement(line, start, byteCursor, cands[0]+" ")
		redrawLine(prompt, buf, pos)
		return buf, pos
	}

	lcp := longestCommonPrefix(cands)
	if len(lcp) > len(current) {
		buf, pos = applyReplacement(line, start, byteCursor, lcp)
		redrawLine(prompt, buf, pos)
		return buf, pos
	}

	fmt.Fprint(os.Stdout, "\n")
	for _, c := range cands {
		fmt.Fprint(os.Stdout, "  ", c, "\n")
	}
	redrawLine(prompt, buf, pos)
	return buf, pos
}

func applyReplacement(line string, startByte, endByte int, replacement string) ([]rune, int) {
	merged := line[:startByte] + replacement + line[endByte:]
	buf := []rune(merged)
	pos := utf8.RuneCountInString(line[:startByte] + replacement)
	return buf, pos
}

// redrawLine is used by the tab-complete helper. It only handles the
// single-row case — multi-row redraws go through the closure in Read so
// prevRows can be tracked. For the completion flow this is acceptable
// because the completion text insertion happens after a newline break.
func redrawLine(prompt string, buf []rune, pos int) {
	fmt.Fprint(os.Stdout, "\r\x1b[K", prompt, string(buf))
	trailing := len(buf) - pos
	if trailing > 0 {
		fmt.Fprintf(os.Stdout, "\x1b[%dD", trailing)
	}
}

func longestCommonPrefix(strs []string) string {
	if len(strs) == 0 {
		return ""
	}
	prefix := strs[0]
	for _, s := range strs[1:] {
		for !strings.HasPrefix(s, prefix) {
			prefix = prefix[:len(prefix)-1]
			if prefix == "" {
				return ""
			}
		}
	}
	return prefix
}

func (lr *LineReader) loadHistory() {
	if lr.historyFile == "" {
		return
	}
	data, err := os.ReadFile(lr.historyFile)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		if line != "" {
			lr.history = append(lr.history, line)
		}
	}
}

func (lr *LineReader) appendHistory(line string) {
	if line == "" {
		return
	}
	if n := len(lr.history); n > 0 && lr.history[n-1] == line {
		return
	}
	lr.history = append(lr.history, line)
	if lr.historyFile == "" {
		return
	}
	if dir := filepath.Dir(lr.historyFile); dir != "" {
		_ = os.MkdirAll(dir, 0o755)
	}
	f, err := os.OpenFile(lr.historyFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	f.WriteString(line + "\n")
}

func enableOPOST(fd int) error {
	t, err := unix.IoctlGetTermios(fd, tcGetAttr)
	if err != nil {
		return err
	}
	t.Oflag |= unix.OPOST | unix.ONLCR
	return unix.IoctlSetTermios(fd, tcSetAttr, t)
}

// HistoryFilePath returns ~/.kaiju/history, or "" if home dir is unavailable.
func HistoryFilePath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".kaiju", "history")
}

func readFallback() (string, error) {
	var line []byte
	var b [1]byte
	for {
		n, err := os.Stdin.Read(b[:])
		if err != nil {
			if err == io.EOF && len(line) > 0 {
				return string(line), nil
			}
			return "", err
		}
		if n == 0 {
			continue
		}
		if b[0] == '\n' {
			return string(line), nil
		}
		line = append(line, b[0])
	}
}
