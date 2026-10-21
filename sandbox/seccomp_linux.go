//go:build linux

package sandbox

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/unix"
)

// The system calls a locked command may not make, for the processor this is
// built for (seccomp_linux_amd64.go, seccomp_linux_arm64.go).
type syscallTable struct {
	// arch is the AUDIT_ARCH value a call must carry. Any other is refused, so
	// a 32-bit call cannot get round a table written for 64-bit numbers.
	arch uint32
	// x32 refuses calls with the x32 bit set (amd64 only).
	x32 bool
	// always are refused whatever else locks the command: what Landlock does
	// not cover.
	always []uintptr
	// signals are allowed only when their first argument is the command
	// itself, its own process group, or 0 (its own group). Linux delivers a
	// signal sent to any thread's id to that thread's process, so refusing the
	// starting program's id alone would leave its other threads' ids open;
	// the command may signal nothing outside its own group instead. A program
	// the command starts can still be stopped through the group.
	signals []uintptr
	// fileWrites are refused when seccomp alone holds the lock.
	fileWrites []uintptr
	// opens are refused, when seccomp alone holds the lock, if the flags in the
	// given argument ask to write, create or truncate.
	opens []openCall
	// openat2 takes its flags in a struct seccomp cannot read. Refused with
	// ENOSYS when seccomp alone holds the lock, so a C library falls back to
	// openat, whose flags can be read.
	openat2 uintptr
}

type openCall struct {
	nr  uintptr
	arg int
}

// writeFlags are the open flags that change a file.
const writeFlags = unix.O_WRONLY | unix.O_RDWR | unix.O_CREAT | unix.O_TRUNC | unix.O_APPEND

func seccompSupported() bool { return table != nil }

// program is a seccomp filter being assembled, with jumps to named places
// resolved at the end.
type program struct {
	ins    []unix.SockFilter
	jt, jf []string
	labels map[string]int
}

func (p *program) emit(code uint16, k uint32, jt, jf string) {
	p.ins = append(p.ins, unix.SockFilter{Code: code, K: k})
	p.jt = append(p.jt, jt)
	p.jf = append(p.jf, jf)
}

func (p *program) label(name string) { p.labels[name] = len(p.ins) }

func (p *program) resolve() error {
	for i := range p.ins {
		for _, j := range []struct {
			name string
			to   *uint8
		}{{p.jt[i], &p.ins[i].Jt}, {p.jf[i], &p.ins[i].Jf}} {
			if j.name == "" {
				continue
			}
			at, ok := p.labels[j.name]
			if !ok {
				return fmt.Errorf("no place named %q", j.name)
			}
			off := at - i - 1
			if off < 0 || off > 255 {
				return fmt.Errorf("jump to %q out of range", j.name)
			}
			*j.to = uint8(off)
		}
	}
	return nil
}

const (
	ldW  = unix.BPF_LD | unix.BPF_W | unix.BPF_ABS
	jeq  = unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K
	jge  = unix.BPF_JMP | unix.BPF_JGE | unix.BPF_K
	jset = unix.BPF_JMP | unix.BPF_JSET | unix.BPF_K
	ret  = unix.BPF_RET | unix.BPF_K

	// Offsets in struct seccomp_data. An argument's low 32 bits come first on
	// both little-endian processors this is built for.
	offNr   = 0
	offArch = 4
)

func offArg(i int) uint32 { return uint32(16 + 8*i) }

/*
 * buildFilter assembles the filter for one locked command.
 * param: self - the command's process id, which is also its group's.
 * param: fileWrites - also refuse writing files (no Landlock).
 */
func buildFilter(t *syscallTable, self int, fileWrites bool) ([]unix.SockFilter, error) {
	p := &program{labels: map[string]int{}}
	p.emit(ldW, offArch, "", "")
	p.emit(jeq, t.arch, "", "deny")
	p.emit(ldW, offNr, "", "")
	if t.x32 {
		p.emit(jge, 0x40000000, "deny", "")
	}
	for _, nr := range t.always {
		p.emit(jeq, uint32(nr), "deny", "")
	}
	pid := uint32(self)
	for i, nr := range t.signals {
		next := fmt.Sprintf("signal-%d", i)
		p.emit(jeq, uint32(nr), "", next)
		p.emit(ldW, offArg(0), "", "")
		p.emit(jeq, 0, "allow", "")
		p.emit(jeq, pid, "allow", "")
		p.emit(jeq, -pid, "allow", "deny")
		p.label(next)
	}
	if fileWrites {
		for _, nr := range t.fileWrites {
			p.emit(jeq, uint32(nr), "deny", "")
		}
		if t.openat2 != 0 {
			p.emit(jeq, uint32(t.openat2), "enosys", "")
		}
		for i, o := range t.opens {
			next := fmt.Sprintf("open-%d", i)
			p.emit(jeq, uint32(o.nr), "", next)
			p.emit(ldW, offArg(o.arg), "", "")
			p.emit(jset, writeFlags, "deny", "allow")
			p.label(next)
		}
	}
	p.label("allow")
	p.emit(ret, unix.SECCOMP_RET_ALLOW, "", "")
	p.label("deny")
	p.emit(ret, unix.SECCOMP_RET_ERRNO|uint32(unix.EPERM), "", "")
	p.label("enosys")
	p.emit(ret, unix.SECCOMP_RET_ERRNO|uint32(unix.ENOSYS), "", "")
	if err := p.resolve(); err != nil {
		return nil, err
	}
	return p.ins, nil
}

// applySeccomp installs the filter on this thread. no_new_privs is already set.
func applySeccomp(self int, fileWrites bool) error {
	if table == nil {
		return fmt.Errorf("no system call table for this processor")
	}
	ins, err := buildFilter(table, self, fileWrites)
	if err != nil {
		return err
	}
	prog := unix.SockFprog{Len: uint16(len(ins)), Filter: &ins[0]}
	return unix.Prctl(unix.PR_SET_SECCOMP, unix.SECCOMP_MODE_FILTER, uintptr(unsafe.Pointer(&prog)), 0, 0)
}
