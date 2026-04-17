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

// LineReader is a minimal raw-mode stdin reader with history recall (up/down)
// and tab completion. Safe to call Read from a single goroutine.
type LineReader struct {
	historyFile string
	history     []string
	complete    CompleteFunc

	mu     sync.Mutex
	prompt string
}

func NewLineReader(historyFile string, complete CompleteFunc) *LineReader {
	lr := &LineReader{historyFile: historyFile, complete: complete}
	lr.loadHistory()
	return lr
}

func (lr *LineReader) SetPrompt(p string) {
	lr.mu.Lock()
	lr.prompt = p
	lr.mu.Unlock()
}

// Read blocks until the user submits a line (Enter), presses Ctrl+C (returns
// ErrInterrupt), or hits EOF (io.EOF). The caller is expected to have already
// rendered the prompt on screen; Read uses the stored prompt string only when
// it needs to redraw the input line (backspace, history, completion).
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
	// MakeRaw disables OPOST, which breaks \n → \r\n for concurrent writers
	// (Send, StatusUpdate). Re-enable output processing while we hold raw input.
	if err := enableOPOST(fd); err != nil {
		return "", err
	}

	lr.mu.Lock()
	prompt := lr.prompt
	lr.mu.Unlock()

	var buf []rune
	pos := 0
	histIdx := len(lr.history)
	savedBuf := ""

	redraw := func() {
		fmt.Fprint(os.Stdout, "\r\x1b[K", prompt, string(buf))
		trailing := len(buf) - pos
		if trailing > 0 {
			fmt.Fprintf(os.Stdout, "\x1b[%dD", trailing)
		}
	}

	for {
		b, err := readByte()
		if err != nil {
			if err == io.EOF {
				return "", io.EOF
			}
			return "", err
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
			b2, err := readByte()
			if err != nil || b2 != '[' {
				continue
			}
			b3, err := readByte()
			if err != nil {
				continue
			}
			switch b3 {
			case 'A':
				if histIdx > 0 {
					if histIdx == len(lr.history) {
						savedBuf = string(buf)
					}
					histIdx--
					buf = []rune(lr.history[histIdx])
					pos = len(buf)
					redraw()
				}
			case 'B':
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
			case 'C':
				if pos < len(buf) {
					pos++
					fmt.Fprint(os.Stdout, "\x1b[C")
				}
			case 'D':
				if pos > 0 {
					pos--
					fmt.Fprint(os.Stdout, "\x1b[D")
				}
			}

		case b >= 0x20:
			r, err := readRune(b)
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

func readByte() (byte, error) {
	var b [1]byte
	_, err := os.Stdin.Read(b[:])
	return b[0], err
}

func readRune(first byte) (rune, error) {
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
		b, err := readByte()
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
