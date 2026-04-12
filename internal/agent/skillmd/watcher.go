package skillmd

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Compdeep/kaiju/internal/agent/tools"
)

// Watcher polls skill directories for changes and hot-reloads SKILL.md files.
type Watcher struct {
	dirs     []string
	registry *tools.Registry
	managed  map[string]*SkillMD // name -> currently loaded skill (only skills we loaded)
	interval time.Duration
	mu       sync.Mutex
}

// NewWatcher creates a skill file watcher.
func NewWatcher(dirs []string, reg *tools.Registry, interval time.Duration) *Watcher {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return &Watcher{
		dirs:     dirs,
		registry: reg,
		managed:  make(map[string]*SkillMD),
		interval: interval,
	}
}

// Start blocks until ctx is cancelled, polling for skill file changes.
func (w *Watcher) Start(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.poll()
		}
	}
}

// SetManaged registers a skill as managed by this watcher.
func (w *Watcher) SetManaged(s *SkillMD) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.managed[s.Name()] = s
}

func (w *Watcher) poll() {
	w.mu.Lock()
	defer w.mu.Unlock()

	// Discover all current skill files
	found := make(map[string]*discoveredSkill)
	for _, dir := range w.dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			skillPath := filepath.Join(dir, entry.Name(), "SKILL.md")
			info, err := os.Stat(skillPath)
			if err != nil {
				continue
			}
			data, err := os.ReadFile(skillPath)
			if err != nil {
				continue
			}
			fm, body, err := Parse(data)
			if err != nil {
				continue
			}
			// First wins — home dir skills take priority over bundled/repo.
			if _, exists := found[fm.Name]; exists {
				continue
			}
			found[fm.Name] = &discoveredSkill{
				fm:      fm,
				body:    body,
				path:    skillPath,
				baseDir: filepath.Join(dir, entry.Name()),
				modTime: info.ModTime(),
			}
		}
	}

	// Detect deleted skills
	for name := range w.managed {
		if _, exists := found[name]; !exists {
			w.registry.Unregister(name)
			delete(w.managed, name)
			log.Printf("[skillmd] unregistered deleted skill: %s", name)
		}
	}

	// Detect new and modified skills
	for name, ds := range found {
		// Don't overwrite builtins
		if w.registry.IsBuiltin(name) {
			continue
		}

		// Platform gating
		if err := CheckGating(ds.fm.Metadata); err != nil {
			// If we were managing it and it no longer passes gating, remove
			if _, was := w.managed[name]; was {
				w.registry.Unregister(name)
				delete(w.managed, name)
				log.Printf("[skillmd] ungated skill removed: %s (%v)", name, err)
			}
			continue
		}

		existing, isManaged := w.managed[name]
		if isManaged && existing.modTime.Equal(ds.modTime) {
			continue // unchanged
		}

		s := NewSkillMD(ds.fm, ds.body, ds.baseDir, ds.path, ds.modTime, w.registry)
		w.registry.Replace(s, "skillmd:"+ds.path)
		w.managed[name] = s

		if isManaged {
			log.Printf("[skillmd] reloaded: %s", name)
		} else {
			log.Printf("[skillmd] registered: %s", name)
		}
	}
}

type discoveredSkill struct {
	fm      *Frontmatter
	body    string
	path    string
	baseDir string
	modTime time.Time
}
