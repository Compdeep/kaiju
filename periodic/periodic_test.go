package periodic

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func statusOf(r *Registry, name string) Status {
	for _, s := range r.Status() {
		if s.Name == name {
			return s
		}
	}
	return Status{}
}

func started(t *testing.T, path string) (*Registry, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	r := New(path)
	r.Start(ctx)
	return r, cancel
}

// A task runs at its interval, and each run is recorded.
func TestRunsAtItsInterval(t *testing.T) {
	r, _ := started(t, "")
	var n atomic.Int32
	if err := r.Add(Task{Name: "tick", Every: 20 * time.Millisecond, Run: func(context.Context) error { n.Add(1); return nil }}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "three runs", func() bool { return n.Load() >= 3 })
	s := statusOf(r, "tick")
	if s.Runs < 3 || s.Failures != 0 || s.LastEnd.IsZero() || s.NextRun.IsZero() {
		t.Errorf("status %+v", s)
	}
}

// A run that ignores its time limit is counted as failed, and the task's turns
// are skipped until it returns: runs never pile up behind a stuck one.
func TestAStuckRunIsCountedAndNotPiledUp(t *testing.T) {
	r, _ := started(t, "")
	release := make(chan struct{})
	var calls atomic.Int32
	r.Add(Task{Name: "stuck", Every: 10 * time.Millisecond, Timeout: 20 * time.Millisecond, AtStart: true,
		Run: func(context.Context) error { calls.Add(1); <-release; return nil }}) // ignores ctx
	waitUntil(t, "the time limit to be reported", func() bool { return statusOf(r, "stuck").Failures >= 1 })
	time.Sleep(100 * time.Millisecond) // several turns pass
	if c := calls.Load(); c != 1 {
		t.Errorf("%d runs started while the first was stuck, want 1", c)
	}
	if s := statusOf(r, "stuck"); !s.Running {
		t.Error("the stuck run is not shown as running")
	}
	close(release)
	waitUntil(t, "a new run once the stuck one returned", func() bool { return calls.Load() >= 2 })
}

// A panic is a failed run, not the end of the process, and the task carries on.
func TestAPanicIsAFailedRun(t *testing.T) {
	r, _ := started(t, "")
	var n atomic.Int32
	r.Add(Task{Name: "boom", Every: 10 * time.Millisecond, Run: func(context.Context) error {
		if n.Add(1) == 1 {
			panic("bad state")
		}
		return nil
	}})
	waitUntil(t, "a run after the panic", func() bool { return n.Load() >= 2 })
	if s := statusOf(r, "boom"); s.Failures < 1 {
		t.Errorf("status %+v, want the panic counted", s)
	}
}

// The last error is recorded, and cleared by a run that succeeds.
func TestTheLastErrorIsRecorded(t *testing.T) {
	r, _ := started(t, "")
	var n atomic.Int32
	r.Add(Task{Name: "flaky", Every: 10 * time.Millisecond, Run: func(context.Context) error {
		if n.Add(1) == 1 {
			return errors.New("database locked")
		}
		return nil
	}})
	waitUntil(t, "the error", func() bool { return statusOf(r, "flaky").LastError == "database locked" })
	waitUntil(t, "a clean run", func() bool { return statusOf(r, "flaky").LastError == "" && n.Load() >= 2 })
}

// Settings outside a task's range are refused; inside it they apply, are
// saved 0600, and a new registry reading the same file uses them.
func TestSettingsAreCheckedSavedAndReloaded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.json")
	r, _ := started(t, path)
	task := Task{Name: "sweep", Every: time.Hour, Min: time.Minute, Max: 24 * time.Hour, Run: func(context.Context) error { return nil }}
	r.Add(task)
	if err := r.Set("sweep", time.Second, true); err == nil {
		t.Error("an interval below the minimum was accepted")
	}
	if err := r.Set("sweep", 30*time.Minute, false); err != nil {
		t.Fatal(err)
	}
	if s := statusOf(r, "sweep"); s.Every != 30*time.Minute || s.Enabled {
		t.Errorf("status after Set %+v", s)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("settings file %v, mode %v; want 0600", err, fi.Mode().Perm())
	}
	r2, _ := started(t, path)
	r2.Add(task)
	if s := statusOf(r2, "sweep"); s.Every != 30*time.Minute || s.Enabled {
		t.Errorf("reloaded %+v, want 30m and off", s)
	}
}

// A broken settings file leaves every task on its defaults, says why, and is
// replaced by the next good save.
func TestABrokenSettingsFileFallsBackToDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.json")
	os.WriteFile(path, []byte("{not json"), 0o600)
	r, _ := started(t, path)
	r.Add(Task{Name: "sweep", Every: time.Hour, Min: time.Minute, Max: 2 * time.Hour, Run: func(context.Context) error { return nil }})
	if s := statusOf(r, "sweep"); s.Every != time.Hour || !s.Enabled {
		t.Errorf("status %+v, want the defaults", s)
	}
	if r.SettingsError() == "" {
		t.Error("the broken file was not reported")
	}
	if err := r.Set("sweep", 2*time.Hour, true); err != nil || r.SettingsError() != "" {
		t.Errorf("Set = %v, settings error %q", err, r.SettingsError())
	}
}

// A task that is off does not run on its interval, and RunNow runs it anyway.
func TestOffAndRunNow(t *testing.T) {
	r, _ := started(t, "")
	var n atomic.Int32
	r.Add(Task{Name: "manual", Every: 10 * time.Millisecond, Min: 10 * time.Millisecond, Max: time.Hour, Run: func(context.Context) error { n.Add(1); return nil }})
	r.Set("manual", 10*time.Millisecond, false)
	time.Sleep(60 * time.Millisecond)
	if n.Load() != 0 {
		t.Fatalf("an off task ran %d times", n.Load())
	}
	r.RunNow("manual")
	waitUntil(t, "the run asked for", func() bool { return n.Load() == 1 })
}

// A task with no finished run in twice its interval is late.
func TestLate(t *testing.T) {
	r, _ := started(t, "")
	release := make(chan struct{})
	defer close(release)
	r.Add(Task{Name: "slow", Every: 10 * time.Millisecond, Timeout: time.Hour, AtStart: true,
		Run: func(ctx context.Context) error { <-release; return nil }})
	waitUntil(t, "the task to be late", func() bool { return statusOf(r, "slow").Late })
}

// Tasks are refused without a name, function or interval, or twice.
func TestAddRefusesBadTasks(t *testing.T) {
	r := New("")
	ok := func(context.Context) error { return nil }
	for _, tk := range []Task{{Every: time.Second, Run: ok}, {Name: "a", Every: time.Second}, {Name: "a", Run: ok}} {
		if err := r.Add(tk); err == nil {
			t.Errorf("accepted %+v", tk)
		}
	}
	if err := r.Add(Task{Name: "a", Every: time.Second, Run: ok}); err != nil {
		t.Fatal(err)
	}
	if err := r.Add(Task{Name: "a", Every: time.Second, Run: ok}); err == nil {
		t.Error("accepted a second task with the same name")
	}
}

// First delays the first run; later runs follow the interval.
func TestFirstDelaysTheFirstRun(t *testing.T) {
	r, _ := started(t, "")
	var n atomic.Int32
	r.Add(Task{Name: "settle", Every: time.Hour, First: 30 * time.Millisecond, Run: func(context.Context) error { n.Add(1); return nil }})
	time.Sleep(10 * time.Millisecond)
	if n.Load() != 0 {
		t.Fatal("ran before its first delay")
	}
	waitUntil(t, "the first run", func() bool { return n.Load() == 1 })
}

// AtStop gives a task one last run when the registry stops, so state kept in
// memory is written.
func TestAtStopRunsOnceMoreOnStop(t *testing.T) {
	r, cancel := started(t, "")
	var n atomic.Int32
	r.Add(Task{Name: "flush", Every: time.Hour, AtStop: true, Run: func(context.Context) error { n.Add(1); return nil }})
	cancel()
	waitUntil(t, "the last run", func() bool { return n.Load() == 1 })
}

// A failed run is logged with the task's name, as the loops it replaced logged
// their failures.
func TestAFailedRunIsLogged(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	r, _ := started(t, "")
	r.Add(Task{Name: "cert-renew", Every: time.Hour, AtStart: true, Run: func(context.Context) error { return errors.New("no key file") }})
	waitUntil(t, "the failure", func() bool { return statusOf(r, "cert-renew").Failures == 1 })
	if !bytes.Contains(buf.Bytes(), []byte("[periodic] cert-renew: no key file")) {
		t.Errorf("log = %q", buf.String())
	}
}

// Off starts a task switched off; a saved setting switches it on.
func TestOffStartsSwitchedOffUntilASettingSaysOtherwise(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.json")
	var n atomic.Int32
	task := Task{Name: "sweep", Every: 10 * time.Millisecond, Min: 10 * time.Millisecond, Max: time.Hour, Off: true, Run: func(context.Context) error { n.Add(1); return nil }}
	r, cancel := started(t, path)
	r.Add(task)
	time.Sleep(50 * time.Millisecond)
	if n.Load() != 0 || statusOf(r, "sweep").Enabled {
		t.Fatalf("an Off task ran %d times / enabled %v", n.Load(), statusOf(r, "sweep").Enabled)
	}
	r.Set("sweep", 10*time.Millisecond, true)
	cancel()
	r2, _ := started(t, path)
	r2.Add(task)
	waitUntil(t, "the saved switch to win", func() bool { return n.Load() > 0 })
}

// An interval outside the task's range, as from configuration, widens the range
// instead of the task being refused.
func TestTheRangeIncludesTheTasksOwnInterval(t *testing.T) {
	r := New("")
	ok := func(context.Context) error { return nil }
	if err := r.Add(Task{Name: "fast", Every: time.Second, Min: time.Minute, Max: time.Hour, Run: ok}); err != nil {
		t.Fatal(err)
	}
	if err := r.Add(Task{Name: "slow", Every: 48 * time.Hour, Min: time.Minute, Max: time.Hour, Run: ok}); err != nil {
		t.Fatal(err)
	}
	if s := statusOf(r, "fast"); s.Min != time.Second || s.Max != time.Hour {
		t.Errorf("fast range %s to %s, want 1s to 1h", s.Min, s.Max)
	}
	if s := statusOf(r, "slow"); s.Min != time.Minute || s.Max != 48*time.Hour {
		t.Errorf("slow range %s to %s, want 1m to 48h", s.Min, s.Max)
	}
}

// Every reports the interval as currently set.
func TestEveryIsTheCurrentInterval(t *testing.T) {
	r := New("")
	r.Add(Task{Name: "lane", Every: time.Hour, Min: time.Minute, Max: 24 * time.Hour, Run: func(context.Context) error { return nil }})
	r.Set("lane", 2*time.Hour, true)
	if got := r.Every("lane"); got != 2*time.Hour {
		t.Errorf("Every = %s, want 2h", got)
	}
	if r.Every("missing") != 0 {
		t.Error("Every of a missing task is not 0")
	}
}
