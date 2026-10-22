package sandbox

import (
	"fmt"
	"strings"
)

// What a program a tool starts may do.
//
// A step runs at a level, and each effect is allowed from a level up. The
// kernel enforces the effects on every system call the program makes, so no
// command is judged by its name or its text, and a program nobody has thought
// of is held the same way as any other. An application gives the table
// (Options.Effects); without one, DefaultEffects applies.

// Effect is one kind of thing a program can do.
type Effect string

const (
	// ReadFiles reads and lists any file, whatever its permissions
	// (CAP_DAC_READ_SEARCH, when the starting program holds it). Refused, a
	// program reads only what its account may.
	ReadFiles Effect = "read_files"
	// ReadPrograms traces other programs and reads their memory and their
	// /proc/<pid> files: strace, gdb -p, ss -p, lsof (CAP_SYS_PTRACE, when the
	// starting program holds it). The ptrace requests that change the traced
	// program are refused even when this is allowed.
	ReadPrograms Effect = "read_programs"
	// ReadKernelLog reads the kernel log (CAP_SYSLOG, read actions only).
	ReadKernelLog Effect = "read_kernel_log"
	// Network opens internet sockets: connecting out, resolving names,
	// listening on a port.
	Network Effect = "network"
	// ChangeFiles writes, creates, removes, renames, truncates or changes the
	// permissions, owner or times of a file.
	ChangeFiles Effect = "change_files"
	// SignalPrograms signals programs outside the command's own process
	// group, or changes their limits, priority or memory placement — only
	// those its account may, unless it runs as root. Refused, a command may
	// signal only its whole group or itself: seccomp cannot tell whether a
	// process id is in the group, so a program that stops a child by its id
	// (strace's start-up check, timeout, a shell's kill $!) fails.
	SignalPrograms Effect = "signal_programs"
	// Root runs the program with every right: through Options.Root, or as
	// this program's own account when there is none.
	Root Effect = "root"
)

// Never is the level of an effect that is never allowed.
const Never = -1

// Effects is the lowest level at which each effect is allowed.
type Effects map[Effect]int

// lockEffects are the effects the lock enforces, in the order they are
// described.
var lockEffects = []Effect{ReadFiles, ReadPrograms, ReadKernelLog, Network, ChangeFiles, SignalPrograms}

/*
 * DefaultEffects is the table used when an application gives none: reading,
 * the network and signalling programs at every level, changing files and root
 * from 200.
 */
func DefaultEffects() Effects {
	return Effects{
		ReadFiles:      0,
		ReadPrograms:   0,
		ReadKernelLog:  0,
		Network:        0,
		SignalPrograms: 0,
		ChangeFiles:    200,
		Root:           200,
	}
}

/*
 * Level is the lowest level at which eff is allowed. An effect the table does
 * not name keeps its default.
 */
func (e Effects) Level(eff Effect) int {
	if l, ok := e[eff]; ok {
		return l
	}
	return DefaultEffects()[eff]
}

// Allows reports whether eff is allowed at level.
func (e Effects) Allows(eff Effect, level int) bool {
	l := e.Level(eff)
	return l != Never && level >= l
}

// allowedAt is the lock's effects, each allowed or not at level.
func (e Effects) allowedAt(level int) map[Effect]bool {
	a := make(map[Effect]bool, len(lockEffects))
	for _, eff := range lockEffects {
		a[eff] = e.Allows(eff, level)
	}
	return a
}

var effectWords = map[Effect]string{
	ReadFiles:      "read any file",
	ReadPrograms:   "trace other programs and read their memory",
	ReadKernelLog:  "read the kernel log",
	Network:        "use the network",
	ChangeFiles:    "change files",
	SignalPrograms: "signal or change other programs",
	Root:           "run as root",
}

/*
 * Describe says what the programs of a step at level may and may not do, in
 * one sentence a tool adds to the output of a command that failed, so a
 * refused command is not retried as though it might work.
 * return: the sentence, or "" when everything is allowed.
 */
func Describe(level int) string {
	eff := currentEffects()
	if eff.Allows(Root, level) {
		return ""
	}
	var may, mayNot []string
	for _, e := range append(lockEffects, Root) {
		if eff.Allows(e, level) {
			may = append(may, effectWords[e])
			continue
		}
		w := effectWords[e]
		if l := eff.Level(e); l == Never {
			w += " (never)"
		} else {
			w += fmt.Sprintf(" (needs %d)", l)
		}
		mayNot = append(mayNot, w)
	}
	s := fmt.Sprintf("this step runs at level %d", level)
	if len(may) > 0 {
		s += "; its programs may " + list(may, "and")
	}
	return s + "; they may not " + list(mayNot, "or")
}

func list(words []string, last string) string {
	if len(words) < 2 {
		return strings.Join(words, "")
	}
	return strings.Join(words[:len(words)-1], ", ") + " " + last + " " + words[len(words)-1]
}
