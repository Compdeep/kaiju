package plugins

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Converging a binary on the plugin set it was asked for.
//
// `kaiju serve --plugins a,b` declares what this installation should be. The set
// actually linked in is Compiled(), which is derived from the init() of each
// build-tagged package and so cannot drift from the truth. When the two differ
// the binary builds its replacement, proves it boots, swaps itself, and re-execs.
//
// The alternative, which is what shipped before this, is to log "requested but
// not compiled in" and serve anyway. That is how this install ran for weeks with
// a supervised Python plugin host and nothing attached to it.

// convergedEnv marks a process that converge already exec'd once. Without it a
// rebuild whose result still does not satisfy the request would exec forever.
const convergedEnv = "KAIJU_CONVERGED"

// buildTimeout bounds a rebuild. A cold cache for this module takes well under a
// minute; five is slack for a slow disk, not a budget to spend.
const buildTimeout = 5 * time.Minute

// SourceTree returns the module directory this binary can rebuild itself from,
// probing beside the executable and then the working directory. It returns ""
// when neither holds this module, which is the ordinary case for a binary
// deployed on its own.
func SourceTree() string {
	var roots []string
	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		roots = append(roots, filepath.Dir(exe))
	}
	if cwd, err := os.Getwd(); err == nil {
		roots = append(roots, cwd)
	}
	for _, r := range roots {
		b, err := os.ReadFile(filepath.Join(r, "go.mod"))
		if err != nil {
			continue
		}
		// Any go.mod is not enough — it must be OURS, or we would hand a build
		// of some neighbouring module to the linker and ship the result.
		if bytes.Contains(b, []byte("module github.com/Compdeep/kaiju")) {
			return r
		}
	}
	return ""
}

// CanBuild reports whether this installation can rebuild itself, and when it
// cannot, which precondition failed and in enough detail to act on. It is checked
// before a build is attempted so that "I cannot install that here" is an answer
// rather than a compiler error quoted at the user.
func CanBuild() error {
	if _, err := exec.LookPath("go"); err != nil {
		return fmt.Errorf("no Go toolchain on PATH, so this binary cannot rebuild itself; install one, or run a binary built with the plugins you want")
	}
	root := SourceTree()
	if root == "" {
		return fmt.Errorf("kaiju's source tree is not beside this binary or in the working directory, so there is nothing to rebuild from; build elsewhere with -tags plugin_<name> and deploy that")
	}
	// internal/gateway/embed.go carries `//go:embed all:web`, and that directory
	// is gitignored because npm writes it. Absent, the rebuild fails at compile
	// time with an embed error that says nothing about plugins. The way out is
	// -tags noui, which drops the web UI the user is most likely talking through,
	// so it is not a fallback to take quietly.
	if _, err := os.Stat(filepath.Join(root, "internal", "gateway", "web")); err != nil {
		return fmt.Errorf("the built web UI is missing from %s — run `make web` there first, because a rebuild embeds it",
			filepath.Join(root, "internal", "gateway", "web"))
	}
	return nil
}

// ldflags reproduces the Makefile's version stamping so a converged binary does
// not silently report itself as "dev". VERSION is a file at the module root and
// COMMIT comes from git; a missing git is not fatal, because the version is a
// label and the build is the point.
func ldflags(root string) string {
	version := "dev"
	if b, err := os.ReadFile(filepath.Join(root, "VERSION")); err == nil {
		if v := strings.TrimSpace(string(b)); v != "" {
			version = v
		}
	}
	commit := "unknown"
	cmd := exec.Command("git", "describe", "--always", "--dirty")
	cmd.Dir = root
	if out, err := cmd.Output(); err == nil {
		if c := strings.TrimSpace(string(out)); c != "" {
			commit = c
		}
	}
	return fmt.Sprintf("-s -w -X main.version=%s+%s", version, commit)
}

// Rebuild builds a binary carrying exactly the plugins named in want, proves it
// starts and reports that set, and replaces the running executable with it. It
// does NOT exec: the caller decides when the process is replaced, which matters
// because a tool that replaces its own process mid-turn destroys the answer
// describing what it did.
//
// The new binary is written beside the current one so the rename is within one
// filesystem and therefore atomic. On any failure the current binary is left
// exactly as it was.
func Rebuild(want []string) error {
	if err := CanBuild(); err != nil {
		return err
	}
	tags, err := Tags(want)
	if err != nil {
		return err
	}
	root := SourceTree()
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("couldn't find this binary's own path: %w", err)
	}
	if resolved, lerr := filepath.EvalSymlinks(exe); lerr == nil {
		// Follow the symlink so the rename replaces the real file rather than
		// turning a deployment symlink into a binary.
		exe = resolved
	}
	next := exe + ".next"

	log.Printf("[plugins] building %s with %s", filepath.Base(next), strings.Join(tags, ","))
	build := exec.Command("go", "build",
		"-ldflags", ldflags(root),
		"-tags", strings.Join(tags, ","),
		"-o", next, "./cmd/kaiju")
	build.Dir = root
	build.Env = os.Environ()
	var stderr bytes.Buffer
	build.Stderr = &stderr
	done := time.AfterFunc(buildTimeout, func() {
		if build.Process != nil {
			_ = build.Process.Kill()
		}
	})
	runErr := build.Run()
	done.Stop()
	if runErr != nil {
		os.Remove(next)
		return fmt.Errorf("the build failed: %w: %s", runErr, lastLines(stderr.String(), 5))
	}

	// Prove it boots and carries what was asked before trusting it. A binary that
	// links but panics on start would otherwise replace a working one, and the
	// supervisor would restart it into the same panic.
	check := exec.Command(next, "selfcheck", "--plugins", strings.Join(want, ","))
	check.Dir = root
	out, checkErr := check.CombinedOutput()
	if checkErr != nil {
		os.Remove(next)
		return fmt.Errorf("the new binary failed its selfcheck, so the running one was left alone: %w: %s",
			checkErr, lastLines(string(out), 5))
	}

	if err := os.Rename(next, exe); err != nil {
		os.Remove(next)
		return fmt.Errorf("couldn't replace %s with the new binary: %w", exe, err)
	}
	log.Printf("[plugins] %s now carries: %s", filepath.Base(exe), strings.TrimSpace(string(out)))
	return nil
}

// Converge brings this process into line with want, rebuilding and re-execing if
// it has to. It reports whether it is about to replace the process; when it
// returns nil and false, the caller carries on serving.
//
// It runs at startup, before anything binds a port or opens the database, so
// there is no in-flight work for the exec to destroy.
func Converge(want []string) error {
	missing := Missing(want)
	if len(missing) == 0 {
		return nil
	}
	// Already rebuilt once and still short. Rebuilding again would produce the
	// same binary and exec into the same gap, so say what could not be satisfied
	// and let the caller serve without it.
	if os.Getenv(convergedEnv) != "" {
		return fmt.Errorf("after rebuilding, %s still not compiled in — serving without %s",
			strings.Join(missing, ", "), plural(len(missing), "it", "them"))
	}
	log.Printf("[plugins] %s requested but not in this binary — rebuilding to include %s",
		strings.Join(missing, ", "), plural(len(missing), "it", "them"))
	if err := Rebuild(want); err != nil {
		return err
	}
	return ReExec()
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// lastLines keeps the tail of a compiler's output, which is where it says what
// was wrong. The head is usually the command line.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "; ")
}

// Replacing the process after a plugin was installed mid-session.
//
// Converge runs at startup, where exec is free: nothing has bound a port and no
// turn is in flight. A plugin installed through the agent has neither luxury. The
// tool that installed it still has an answer to deliver, and exec'ing before that
// answer reaches the user loses the only account of what happened.
//
// So the tool arms the replacement instead of performing it: build, rename, answer,
// and some seconds later drain and exec. The delay is a delay, not a guarantee —
// a turn still running when it fires is cut short, and ReconcileDanglingTurns at
// the next startup closes it out. The drain makes in-flight HTTP survive; it
// cannot make an in-flight plan survive.
var (
	armOnce sync.Once
	drainMu sync.Mutex
	drainFn func()
)

// SetDrain registers how to shut the server down before the process is replaced.
// Called once at startup by the owner of the listener.
func SetDrain(fn func()) {
	drainMu.Lock()
	defer drainMu.Unlock()
	drainFn = fn
}

// ArmReExec schedules the replacement of this process, once. Repeated calls after
// the first are ignored, because two timers racing to exec the same binary is a
// way to lose the loser's work for no benefit.
func ArmReExec(after time.Duration) {
	armOnce.Do(func() {
		log.Printf("[plugins] the binary is replaced; this process is handing over in %s", after)
		time.AfterFunc(after, func() {
			drainMu.Lock()
			fn := drainFn
			drainMu.Unlock()
			if fn != nil {
				fn()
			}
			if err := ReExec(); err != nil {
				// Nothing above can recover this: the binary on disk is already the
				// new one, so the supervisor restarting us lands on it anyway.
				log.Printf("[plugins] handover failed, leaving the restart to the supervisor: %v", err)
			}
		})
	})
}
