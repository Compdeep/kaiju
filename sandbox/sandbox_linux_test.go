//go:build linux

package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	Enter()
	Enable(Options{Logf: func(string, ...any) {}})
	os.Exit(m.Run())
}

// resetSetup forgets what setup found, so the next locked command detects the
// lock and runs the self-test again.
func resetSetup(t *testing.T) {
	t.Helper()
	setupMu.Lock()
	setupDone, report, setupErr, tmpDir = false, Report{}, nil, ""
	setupMu.Unlock()
}

// eachLock runs f under every lock this kernel can apply: Landlock with
// seccomp when the kernel has Landlock, and seccomp alone always.
func eachLock(t *testing.T, f func(t *testing.T, landlock bool)) {
	for _, landlock := range []bool{true, false} {
		name := "seccomp-only"
		if landlock {
			name = "landlock+seccomp"
		}
		t.Run(name, func(t *testing.T) {
			if landlock && landlockABI() == 0 {
				t.Skipf("kernel %s has no Landlock", kernel())
			}
			forceSeccompOnly = !landlock
			resetSetup(t)
			t.Cleanup(func() { forceSeccompOnly = false; resetSetup(t) })
			if r := Status(); r.SelfTest != nil {
				t.Fatalf("self-test: %v", r.SelfTest)
			}
			f(t, landlock)
		})
	}
}

func kernel() string {
	var u syscall.Utsname
	if syscall.Uname(&u) != nil {
		return "?"
	}
	b := make([]byte, 0, len(u.Release))
	for _, c := range u.Release {
		if c == 0 {
			break
		}
		b = append(b, byte(c))
	}
	return string(b)
}

// run runs a shell script locked, with $1 set to dir, and returns its output.
func run(t *testing.T, dir, script string, extra ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := Command(ctx, true, nil, "sh", append([]string{"-c", script, "sh", dir}, extra...)...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// fixture is a folder the test user owns, so a refused change proves the lock
// and not file permissions.
func fixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "in"), []byte("probe"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func unchanged(t *testing.T, dir string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "in"))
	if err != nil || string(b) != "probe" {
		t.Fatalf("the file was changed or removed: %q %v", b, err)
	}
	st, _ := os.Stat(filepath.Join(dir, "in"))
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("permissions changed to %v", st.Mode().Perm())
	}
}

func absent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); err == nil {
		t.Fatalf("%s was created", path)
	}
}

func TestALockedCommandCanRead(t *testing.T) {
	eachLock(t, func(t *testing.T, landlock bool) {
		dir := fixture(t)
		cases := []struct{ name, script, want string }{
			{"a file", `cat "$1/in"`, "probe"},
			{"a folder", `ls "$1"`, "in"},
			{"the process list", `ps -o pid= -p $$ >/dev/null && echo ok`, "ok"},
			{"a pipe", `echo hello | tr a-z A-Z`, "HELLO"},
			{"a system file", `head -c 4 /etc/passwd`, "root"},
			{"/dev/null for errors", `ls /no/such/path 2>/dev/null; echo after`, "after"},
			{"/dev/null for output", `echo hidden >/dev/null; echo shown`, "shown"},
			{"/dev/null for both", `ls /no/such/path &>/dev/null; echo both`, "both"},
		}
		for _, c := range cases {
			out, err := run(t, dir, c.script)
			if err != nil || strings.TrimSpace(out) != c.want {
				t.Errorf("%s: want %q, got %q (%v)", c.name, c.want, out, err)
			}
		}
	})
}

func TestALockedCommandCannotChangeAFile(t *testing.T) {
	eachLock(t, func(t *testing.T, landlock bool) {
		cases := []struct{ name, script, creates string }{
			{"create", `echo x > "$1/new"`, "new"},
			{"append", `echo x >> "$1/in"`, ""},
			{"empty", `: > "$1/in"`, ""},
			{"truncate", `truncate -s 0 "$1/in"`, ""},
			{"remove", `rm -f "$1/in"`, ""},
			{"rename", `mv "$1/in" "$1/moved"`, "moved"},
			{"copy", `cp "$1/in" "$1/copy"`, "copy"},
			{"make a folder", `mkdir "$1/sub"`, "sub"},
			{"chmod", `chmod 777 "$1/in"`, ""},
			{"setuid", `chmod u+s "$1/in"`, ""},
			{"symbolic link", `ln -s "$1/in" "$1/link"`, "link"},
			{"hard link", `ln "$1/in" "$1/hard"`, "hard"},
			{"tee", `echo x | tee "$1/tee"`, "tee"},
			{"sed in place", `sed -i s/probe/owned/ "$1/in"`, ""},
			{"dd", `echo x | dd of="$1/dd"`, "dd"},
			{"a program started by a program", `sh -c 'sh -c "echo x > \"$0/deep\""' "$1"`, "deep"},
		}
		for _, c := range cases {
			dir := fixture(t)
			run(t, dir, c.script)
			unchanged(t, dir)
			if c.creates != "" {
				absent(t, filepath.Join(dir, c.creates))
			}
		}
	})
}

func TestALockedCommandCannotChangeAFileTimes(t *testing.T) {
	eachLock(t, func(t *testing.T, landlock bool) {
		dir := fixture(t)
		before, _ := os.Stat(filepath.Join(dir, "in"))
		run(t, dir, `touch -d 2000-01-01 "$1/in"`)
		after, _ := os.Stat(filepath.Join(dir, "in"))
		if !after.ModTime().Equal(before.ModTime()) {
			t.Fatalf("modification time changed from %v to %v", before.ModTime(), after.ModTime())
		}
	})
}

func TestALockedCommandCannotRunAScriptThatWrites(t *testing.T) {
	eachLock(t, func(t *testing.T, landlock bool) {
		dir := fixture(t)
		script := filepath.Join(dir, "backup_database.sh")
		if err := os.WriteFile(script, []byte("#!/bin/sh\necho owned > \"$1/fromscript\"\nchmod 777 \"$1/in\"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		run(t, dir, `"$1/backup_database.sh" "$1"`)
		absent(t, filepath.Join(dir, "fromscript"))
		unchanged(t, dir)
	})
}

func TestALockedCommandCannotStopAnotherProcess(t *testing.T) {
	eachLock(t, func(t *testing.T, landlock bool) {
		other := exec.Command("sleep", "30")
		if err := other.Start(); err != nil {
			t.Fatal(err)
		}
		defer other.Process.Kill()
		for _, target := range []string{strconv.Itoa(other.Process.Pid), strconv.Itoa(os.Getpid()), "-1"} {
			out, err := run(t, "", "kill -0 "+target+" && echo signalled")
			if err == nil || strings.Contains(out, "signalled") {
				t.Errorf("signalled %s: %q", target, out)
			}
		}
		if err := other.Process.Signal(syscall.Signal(0)); err != nil {
			t.Fatalf("the other process is gone: %v", err)
		}
		// Its own process group stays reachable: stopping the group ends the
		// command and what it started at once, not after the sleep.
		start := time.Now()
		run(t, "", `sleep 30 & kill -TERM 0; wait`)
		if d := time.Since(start); d > 10*time.Second {
			t.Fatalf("could not stop its own process group: took %v", d)
		}
	})
}

func TestALockedCommandCannotWriteTheAgentsMemory(t *testing.T) {
	eachLock(t, func(t *testing.T, landlock bool) {
		out, _ := run(t, "", fmt.Sprintf(`printf x | dd of=/proc/%d/mem bs=1 seek=4096 conv=notrunc 2>&1 && echo wrote`, os.Getpid()))
		if strings.Contains(out, "wrote") {
			t.Fatalf("wrote to the agent's memory: %q", out)
		}
	})
}

func TestALockedCommandGetsNoWritableFileFromTheAgent(t *testing.T) {
	eachLock(t, func(t *testing.T, landlock bool) {
		dir := t.TempDir()
		f, err := os.Create(filepath.Join(dir, "held"))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		// Without close-on-exec, as a careless caller might leave it.
		syscall.Syscall(syscall.SYS_FCNTL, f.Fd(), syscall.F_SETFD, 0)
		run(t, dir, fmt.Sprintf(`echo leaked >&%d`, f.Fd()))
		if b, _ := os.ReadFile(filepath.Join(dir, "held")); len(b) != 0 {
			t.Fatalf("wrote through a file the agent held open: %q", b)
		}
	})
}

func TestTheTemporaryFolderIsWritableWithLandlock(t *testing.T) {
	eachLock(t, func(t *testing.T, landlock bool) {
		out, err := run(t, "", `echo scratch > "$TMPDIR/f" && cat "$TMPDIR/f"`)
		if landlock && (err != nil || strings.TrimSpace(out) != "scratch") {
			t.Fatalf("could not use its temporary folder: %q %v", out, err)
		}
		if !landlock && strings.TrimSpace(out) == "scratch" {
			t.Fatal("seccomp alone let a file be written")
		}
	})
}

func TestAnUnlockedCommandCanWrite(t *testing.T) {
	dir := fixture(t)
	cmd := Command(context.Background(), false, nil, "sh", "-c", `echo x > "$1/new"`, "sh", dir)
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "new")); err != nil {
		t.Fatal("an unlocked command could not write")
	}
}

func TestWithNoLockACommandIsRefusedNotRunUnlocked(t *testing.T) {
	saved := table
	table = nil
	resetSetup(t)
	t.Cleanup(func() { table = saved; resetSetup(t) })

	dir := fixture(t)
	cmd := Command(context.Background(), true, nil, "sh", "-c", `echo x > "$1/new"`, "sh", dir)
	if err := cmd.Run(); err == nil || !strings.Contains(err.Error(), "no way to lock") {
		t.Fatalf("want a refusal, got %v", err)
	}
	absent(t, filepath.Join(dir, "new"))

	// Only an application that asks for it runs such a command unlocked.
	Enable(Options{AllowUnlocked: true, Logf: func(string, ...any) {}})
	t.Cleanup(func() { Enable(Options{Logf: func(string, ...any) {}}) })
	if err := Command(context.Background(), true, nil, "true").Run(); err != nil {
		t.Fatalf("AllowUnlocked did not run it: %v", err)
	}
}

func TestTheSelfTestCatchesALockThatDoesNotHold(t *testing.T) {
	probeCommand = func(ctx context.Context, _ bool, _ Report, _ []string, name string, args []string) (*exec.Cmd, error) {
		return exec.CommandContext(ctx, name, args...), nil
	}
	resetSetup(t)
	t.Cleanup(func() { probeCommand = platformCommand; resetSetup(t) })

	if Status().SelfTest == nil {
		t.Fatal("the self-test passed a command that was not locked")
	}
	err := Command(context.Background(), true, nil, "true").Run()
	if err == nil || !strings.Contains(err.Error(), "self-test failed") {
		t.Fatalf("want every locked command refused, got %v", err)
	}
}

func TestAProgramThatHasNotEnabledTheSandboxRefuses(t *testing.T) {
	mu.Lock()
	enabled = false
	mu.Unlock()
	t.Cleanup(func() { Enable(Options{Logf: func(string, ...any) {}}) })
	err := Command(context.Background(), true, nil, "true").Run()
	if err == nil || !errors.Is(err, err) || !strings.Contains(err.Error(), "sandbox.Enter") {
		t.Fatalf("want a refusal naming sandbox.Enter, got %v", err)
	}
}

func TestRewriteDevNull(t *testing.T) {
	cases := map[string]string{
		`ls x 2>/dev/null`:            `ls x 2>&9`,
		`ls x >/dev/null 2>&1`:        `ls x >&9 2>&1`,
		`ls x > /dev/null`:            `ls x >&9`,
		`ls x >>/dev/null`:            `ls x >&9`,
		`ls x &>/dev/null`:            `ls x >&9 2>&9`,
		`ls x 1>/dev/null; echo y`:    `ls x 1>&9; echo y`,
		`cat </dev/null`:              `cat </dev/null`,
		`ls /dev/nullish 2>/dev/null`: `ls /dev/nullish 2>&9`,
		`echo ok`:                     `echo ok`,
	}
	for in, want := range cases {
		if got, _ := rewriteDevNull(in); got != want {
			t.Errorf("%q: want %q, got %q", in, want, got)
		}
	}
}

// The program that starts locked commands is non-dumpable, so a locked command,
// running as the same account, cannot read its memory, environment or memory
// map, even with seccomp alone holding the lock.
func TestALockedCommandCannotReadTheAgentsMemory(t *testing.T) {
	eachLock(t, func(t *testing.T, landlock bool) {
		pid := os.Getpid()
		// The control: a same-account process that is still dumpable can be
		// read under seccomp alone, so the refusal below is the agent's own
		// setting and not something else. (Landlock refuses both.)
		if !landlock {
			other := exec.Command("sleep", "30")
			if err := other.Start(); err != nil {
				t.Fatal(err)
			}
			defer other.Process.Kill()
			out, _ := run(t, "", fmt.Sprintf(`head -c 1 /proc/%d/environ > /dev/null && echo GOT_IT`, other.Process.Pid))
			if !strings.Contains(out, "GOT_IT") {
				t.Fatalf("could not read an ordinary process either, so this test shows nothing: %q", out)
			}
		}
		for _, f := range []string{"environ", "maps", "mem"} {
			out, err := run(t, "", fmt.Sprintf(`head -c 1 /proc/%d/%s > /dev/null && echo GOT_IT`, pid, f))
			if err == nil || strings.Contains(out, "GOT_IT") {
				t.Errorf("read the agent's %s: %q", f, out)
			}
		}
	})
}

// A locked command cannot change the starting program's limits or priority,
// and can change its own.
func TestALockedCommandCannotStarveTheAgent(t *testing.T) {
	eachLock(t, func(t *testing.T, landlock bool) {
		pid := os.Getpid()
		var before syscall.Rlimit
		syscall.Getrlimit(syscall.RLIMIT_NOFILE, &before)
		prioBefore, _ := syscall.Getpriority(syscall.PRIO_PROCESS, 0)

		run(t, "", fmt.Sprintf(`prlimit --pid %d --nofile=8:8`, pid))
		run(t, "", fmt.Sprintf(`renice -n 19 -p %d`, pid))
		run(t, "", fmt.Sprintf(`renice -n 19 -u %d`, os.Getuid()))

		var after syscall.Rlimit
		syscall.Getrlimit(syscall.RLIMIT_NOFILE, &after)
		if after != before {
			t.Errorf("the agent's open-file limit changed from %v to %v", before, after)
		}
		if p, _ := syscall.Getpriority(syscall.PRIO_PROCESS, 0); p != prioBefore {
			t.Errorf("the agent's priority changed from %d to %d", prioBefore, p)
		}

		out, err := run(t, "", `ulimit -n 64 && nice -n 5 echo own`)
		if err != nil || strings.TrimSpace(out) != "own" {
			t.Errorf("could not change its own limits and priority: %q %v", out, err)
		}
	})
}

// A locked command keeps no capability but those that read. Here the test
// holds none, so it has none; the installed service holds CAP_NET_BIND_SERVICE
// as well, which this drops.
func TestALockedCommandKeepsOnlyCapabilitiesThatRead(t *testing.T) {
	eachLock(t, func(t *testing.T, landlock bool) {
		out, err := run(t, "", `grep -E '^Cap(Prm|Eff|Amb):' /proc/self/status`)
		if err != nil {
			t.Fatalf("%q %v", out, err)
		}
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			f := strings.Fields(line)
			if len(f) != 2 {
				continue
			}
			v, _ := strconv.ParseUint(f[1], 16, 64)
			if v&^(1<<2|1<<34) != 0 {
				t.Errorf("%s holds a capability that is not a read: %s", f[0], f[1])
			}
		}
	})
}

// A failed setup is run again after retryAfter, so a failure that passes does
// not refuse every locked command until the program restarts.
func TestAFailedSetupIsTriedAgain(t *testing.T) {
	savedRetry := retryAfter
	retryAfter = time.Hour
	probeCommand = func(ctx context.Context, _ bool, _ Report, _ []string, name string, args []string) (*exec.Cmd, error) {
		return exec.CommandContext(ctx, name, args...), nil
	}
	resetSetup(t)
	t.Cleanup(func() { retryAfter = savedRetry; probeCommand = platformCommand; resetSetup(t) })

	if err := Command(context.Background(), true, nil, "true").Run(); err == nil {
		t.Fatal("a setup whose self-test failed let a command run")
	}
	probeCommand = platformCommand // the cause has passed
	if err := Command(context.Background(), true, nil, "true").Run(); err == nil {
		t.Fatal("the failure was not kept for retryAfter")
	}
	retryAfter = 0
	if err := Command(context.Background(), true, nil, "true").Run(); err != nil {
		t.Fatalf("the setup was not run again after retryAfter: %v", err)
	}
}

// A temporary folder removed while the program runs is made again.
func TestARemovedTemporaryFolderIsMadeAgain(t *testing.T) {
	resetSetup(t)
	t.Cleanup(func() { resetSetup(t) })
	if err := Command(context.Background(), true, nil, "true").Run(); err != nil {
		t.Fatal(err)
	}
	_, first, _ := setup()
	os.RemoveAll(first)
	if err := Command(context.Background(), true, nil, "true").Run(); err != nil {
		t.Fatalf("a locked command failed after its temporary folder was removed: %v", err)
	}
	_, second, err := setup()
	if err != nil || second == first {
		t.Fatalf("no new folder: %q %v", second, err)
	}
	if _, err := os.Stat(second); err != nil {
		t.Fatalf("the new folder does not exist: %v", err)
	}
}

// Locked commands started at the same time, including the first ones, while
// the check has not run yet: each is locked on its own, every write is refused,
// every read works, and the shared check result is read safely (go test -race).
func TestLockedCommandsInParallel(t *testing.T) {
	resetSetup(t)
	t.Cleanup(func() { resetSetup(t) })
	dir := fixture(t)
	const n = 20
	type result struct {
		write bool
		out   string
		err   error
	}
	results := make(chan result, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			if i%2 == 0 {
				out, err := run(t, dir, fmt.Sprintf(`echo x > "$1/new-%d"`, i))
				results <- result{true, out, err}
				return
			}
			out, err := run(t, dir, `cat "$1/in"`)
			results <- result{false, out, err}
		}(i)
	}
	for i := 0; i < n; i++ {
		r := <-results
		if !r.write && (r.err != nil || strings.TrimSpace(r.out) != "probe") {
			t.Errorf("a read in parallel failed: %q %v", r.out, r.err)
		}
	}
	for i := 0; i < n; i += 2 {
		absent(t, filepath.Join(dir, fmt.Sprintf("new-%d", i)))
	}
	unchanged(t, dir)
}
