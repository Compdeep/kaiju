// Package periodic runs functions at fixed intervals, with a time limit on each
// run, and records how each run went.
//
// It replaces hand-written ticker loops. Those had no limit on a run, so one
// call that never returned stopped its loop for good; a panic in one ended the
// whole process; and nothing recorded when a loop last ran, so a loop that had
// stopped looked the same as one that was idle.
//
// A Registry runs every task it is given:
//   - each run gets the task's time limit; a run that ignores the limit is left
//     to finish on its own, the run is counted as failed, and the task's next
//     turn is skipped until that run has returned, so runs never pile up
//   - a panic in a run is recovered, logged with its stack and counted as failed;
//     the task runs again at its next turn
//   - the interval and an on/off switch can be changed while running, within the
//     range the task allows, and are kept in a settings file
//   - Status reports, per task, the last start and end, the last error, the
//     number of runs and failures, the next run, and whether the task is late
//     (no run finished in twice its interval)
package periodic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"sync"
	"time"
)

// Task is one function to run at an interval.
type Task struct {
	Name    string        // stable, unique; the key in the settings file
	Every   time.Duration // default interval
	Min     time.Duration // shortest interval a setting may give it; zero means Every
	Max     time.Duration // longest; zero means Every
	Timeout time.Duration // limit on one run; zero means Every
	AtStart bool          // run once when the registry starts, not only after the first interval
	First   time.Duration // with AtStart false: the first run comes this long after start, not after Every
	AtStop  bool          // run once more when the registry stops, to write what is kept in memory
	Run     func(ctx context.Context) error
}

// Status is how a task stands, for display.
type Status struct {
	Name      string        `json:"name"`
	Every     time.Duration `json:"every"`
	Min       time.Duration `json:"min"`
	Max       time.Duration `json:"max"`
	Timeout   time.Duration `json:"timeout"`
	Enabled   bool          `json:"enabled"`
	Running   bool          `json:"running"`
	LastStart time.Time     `json:"last_start"`
	LastEnd   time.Time     `json:"last_end"`
	LastError string        `json:"last_error"`
	Runs      int           `json:"runs"`
	Failures  int           `json:"failures"`
	NextRun   time.Time     `json:"next_run"`
	Late      bool          `json:"late"`
}

// Registry runs a set of tasks. The zero value is not usable; call New.
type Registry struct {
	path string // settings file; "" keeps settings in memory only

	mu          sync.Mutex
	tasks       map[string]*task
	ctx         context.Context // set by Start
	started     time.Time
	settingsErr string
}

type task struct {
	def     Task
	every   time.Duration
	enabled bool
	st      Status
	wake    chan struct{} // a setting changed or a run was asked for
	runNow  bool
}

// New returns a registry whose settings are kept in path (0600, written by an
// atomic rename). A missing file means every task runs on its defaults; a
// broken one too, and SettingsError says why.
func New(path string) *Registry {
	return &Registry{path: path, tasks: map[string]*task{}}
}

// Add registers a task. A task added after Start starts at once.
func (r *Registry) Add(t Task) error {
	if t.Name == "" || t.Run == nil || t.Every <= 0 {
		return fmt.Errorf("periodic: a task needs a name, a function and an interval")
	}
	if t.Min <= 0 {
		t.Min = t.Every
	}
	if t.Max <= 0 {
		t.Max = t.Every
	}
	if t.Timeout <= 0 {
		t.Timeout = t.Every
	}
	if t.Min > t.Every || t.Max < t.Every {
		return fmt.Errorf("periodic: %s: interval %s is outside its own range %s to %s", t.Name, t.Every, t.Min, t.Max)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.tasks[t.Name]; dup {
		return fmt.Errorf("periodic: %s is already registered", t.Name)
	}
	k := &task{def: t, every: t.Every, enabled: true, wake: make(chan struct{}, 1)}
	k.st.Name = t.Name
	r.tasks[t.Name] = k
	r.applySavedLocked(k)
	if r.ctx != nil {
		go r.loop(r.ctx, k)
	}
	return nil
}

// Start runs every task until ctx ends. Settings saved earlier are applied.
func (r *Registry) Start(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ctx != nil {
		return
	}
	r.ctx = ctx
	r.started = time.Now()
	for _, k := range r.tasks {
		go r.loop(ctx, k)
	}
}

// Set changes a task's interval and switch, applies it at once and saves it.
// An interval outside the task's range is refused and nothing changes.
func (r *Registry) Set(name string, every time.Duration, enabled bool) error {
	r.mu.Lock()
	k, ok := r.tasks[name]
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("periodic: no task %q", name)
	}
	if every < k.def.Min || every > k.def.Max {
		r.mu.Unlock()
		return fmt.Errorf("periodic: %s runs between every %s and every %s, not every %s", name, k.def.Min, k.def.Max, every)
	}
	k.every, k.enabled = every, enabled
	err := r.saveLocked()
	r.mu.Unlock()
	poke(k)
	return err
}

// RunNow runs a task at once, whether or not it is switched on, unless a run
// of it is already going.
func (r *Registry) RunNow(name string) error {
	r.mu.Lock()
	k, ok := r.tasks[name]
	if ok {
		k.runNow = true
	}
	r.mu.Unlock()
	if !ok {
		return fmt.Errorf("periodic: no task %q", name)
	}
	poke(k)
	return nil
}

// Status lists every task, by name.
func (r *Registry) Status() []Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Status, 0, len(r.tasks))
	for _, k := range r.tasks {
		s := k.st
		s.Every, s.Min, s.Max, s.Timeout, s.Enabled = k.every, k.def.Min, k.def.Max, k.def.Timeout, k.enabled
		since := s.LastEnd
		if since.IsZero() {
			since = r.started
		}
		s.Late = k.enabled && !r.started.IsZero() && time.Since(since) > 2*k.every
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// SettingsError is why the settings file could not be used, or "".
func (r *Registry) SettingsError() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.settingsErr
}

func poke(k *task) {
	select {
	case k.wake <- struct{}{}:
	default:
	}
}

func (r *Registry) loop(ctx context.Context, k *task) {
	r.mu.Lock()
	first := k.def.AtStart && k.enabled
	r.mu.Unlock()
	wait := k.def.First
	for {
		if !first {
			r.mu.Lock()
			if wait <= 0 {
				wait = k.every
			}
			k.st.NextRun = time.Now().Add(wait)
			r.mu.Unlock()
			t := time.NewTimer(wait)
			wait = 0
			select {
			case <-ctx.Done():
				t.Stop()
				r.atStop(k)
				return
			case <-k.wake:
				t.Stop()
				r.mu.Lock()
				now := k.runNow
				k.runNow = false
				r.mu.Unlock()
				if !now {
					continue // a new interval or switch: wait again from now
				}
			case <-t.C:
				r.mu.Lock()
				on := k.enabled
				r.mu.Unlock()
				if !on {
					continue
				}
			}
		}
		first = false
		r.runOnce(ctx, k)
	}
}

// atStop gives a task its last run when the registry stops, with a fresh time
// limit: the registry's own context has ended.
func (r *Registry) atStop(k *task) {
	if !k.def.AtStop {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), k.def.Timeout)
	defer cancel()
	r.runOnce(ctx, k)
}

// runOnce runs the task once with its time limit. A run that does not return
// within the limit is counted as failed and left to finish; until it does, the
// task's turns are skipped.
func (r *Registry) runOnce(ctx context.Context, k *task) {
	r.mu.Lock()
	if k.st.Running {
		r.mu.Unlock()
		log.Printf("[periodic] %s: skipped, the previous run has not returned", k.def.Name)
		return
	}
	k.st.Running = true
	k.st.LastStart = time.Now()
	k.st.Runs++
	limit := k.def.Timeout
	r.mu.Unlock()

	runCtx, cancel := context.WithTimeout(ctx, limit)
	done := make(chan error, 1)
	go func() {
		var err error
		defer func() {
			if p := recover(); p != nil {
				log.Printf("[periodic] %s panicked: %v\n%s", k.def.Name, p, debug.Stack())
				err = fmt.Errorf("panicked: %v", p)
			}
			cancel()
			r.mu.Lock()
			k.st.Running = false
			r.mu.Unlock()
			done <- err
		}()
		err = k.def.Run(runCtx)
	}()

	var err error
	select {
	case err = <-done:
	case <-time.After(limit + time.Second):
		err = fmt.Errorf("did not return within its time limit of %s", limit)
		log.Printf("[periodic] WARNING: %s %v; its next turns are skipped until it does", k.def.Name, err)
	}
	r.mu.Lock()
	k.st.LastEnd = time.Now()
	k.st.LastError = ""
	if err != nil {
		k.st.Failures++
		k.st.LastError = err.Error()
	}
	r.mu.Unlock()
	if err != nil {
		log.Printf("[periodic] %s: %v", k.def.Name, err) // the loops this replaced logged their failures
	}
}

type saved struct {
	EverySec int64 `json:"every_sec"`
	Enabled  bool  `json:"enabled"`
}

// applySavedLocked applies the settings file's entry for k, if any and valid.
func (r *Registry) applySavedLocked(k *task) {
	all, err := r.readLocked()
	if err != nil {
		r.settingsErr = err.Error()
		return
	}
	s, ok := all[k.def.Name]
	if !ok {
		return
	}
	every := time.Duration(s.EverySec) * time.Second
	if every < k.def.Min || every > k.def.Max {
		log.Printf("[periodic] %s: saved interval %s is outside %s to %s; using %s", k.def.Name, every, k.def.Min, k.def.Max, k.every)
	} else {
		k.every = every
	}
	k.enabled = s.Enabled
}

func (r *Registry) readLocked() (map[string]saved, error) {
	all := map[string]saved{}
	if r.path == "" {
		return all, nil
	}
	b, err := os.ReadFile(r.path)
	if errors.Is(err, os.ErrNotExist) {
		return all, nil
	}
	if err != nil {
		return all, err
	}
	if err := json.Unmarshal(b, &all); err != nil {
		return map[string]saved{}, fmt.Errorf("%s cannot be read (%v); every task runs on its defaults", r.path, err)
	}
	return all, nil
}

// saveLocked writes every task's settings, keeping entries for tasks not
// registered in this process.
func (r *Registry) saveLocked() error {
	if r.path == "" {
		return nil
	}
	all, err := r.readLocked()
	if err != nil {
		all = map[string]saved{} // a broken file is replaced by a good one
	}
	for name, k := range r.tasks {
		all[name] = saved{EverySec: int64(k.every / time.Second), Enabled: k.enabled}
	}
	b, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(r.path), ".periodic-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), r.path); err != nil {
		return err
	}
	r.settingsErr = ""
	return nil
}
