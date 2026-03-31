package agent

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

/*
 * MemEntry is a single key-value pair in agent memory with optional TTL.
 * desc: Stores a named value with creation timestamp, optional expiration,
 *       and searchable tags.
 */
type MemEntry struct {
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
	Tags      []string  `json:"tags,omitempty"`
}

/*
 * Memory is a persistent key-value store with TTL support.
 * desc: Thread-safe memory backed by a JSON file on disk. Supports
 *       set, get, search by tags, delete, and periodic pruning of
 *       expired entries.
 */
type Memory struct {
	mu      sync.RWMutex
	entries map[string]*MemEntry
	path    string
}

/*
 * NewMemory creates a Memory backed by a JSON file in dir.
 * desc: Creates the directory if needed, initializes the in-memory map,
 *       and loads any existing entries from disk.
 * param: dir - the directory path for the memory.json file.
 * return: pointer to the new Memory, or error if directory creation fails.
 */
func NewMemory(dir string) (*Memory, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	m := &Memory{
		entries: make(map[string]*MemEntry),
		path:    filepath.Join(dir, "memory.json"),
	}
	m.load()
	return m, nil
}

/*
 * Set stores a key-value pair with an optional TTL.
 * desc: Creates or overwrites an entry and persists to disk.
 * param: key - the unique entry key.
 * param: value - the string value to store.
 * param: ttl - time-to-live duration (0 for no expiration).
 * param: tags - searchable tags for this entry.
 */
func (m *Memory) Set(key, value string, ttl time.Duration, tags []string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	entry := &MemEntry{
		Key:       key,
		Value:     value,
		CreatedAt: time.Now().UTC(),
		Tags:      tags,
	}
	if ttl > 0 {
		entry.ExpiresAt = entry.CreatedAt.Add(ttl)
	}
	m.entries[key] = entry
	m.saveLocked()
}

/*
 * Get retrieves a value by key.
 * desc: Returns the stored value if found and not expired.
 * param: key - the entry key to look up.
 * return: value string and true if found and valid, empty string and false otherwise.
 */
func (m *Memory) Get(key string) (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	entry, ok := m.entries[key]
	if !ok {
		return "", false
	}
	if !entry.ExpiresAt.IsZero() && time.Now().After(entry.ExpiresAt) {
		return "", false
	}
	return entry.Value, true
}

/*
 * Search returns all entries matching any of the given tags.
 * desc: Scans all non-expired entries and returns copies of those
 *       that have at least one matching tag.
 * param: tags - slice of tag strings to match against.
 * return: slice of matching MemEntry pointers (copies).
 */
func (m *Memory) Search(tags []string) []*MemEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()

	tagSet := make(map[string]bool, len(tags))
	for _, t := range tags {
		tagSet[t] = true
	}

	var results []*MemEntry
	now := time.Now()
	for _, entry := range m.entries {
		if !entry.ExpiresAt.IsZero() && now.After(entry.ExpiresAt) {
			continue
		}
		for _, t := range entry.Tags {
			if tagSet[t] {
				cp := *entry
				results = append(results, &cp)
				break
			}
		}
	}
	return results
}

/*
 * Delete removes a key from memory.
 * desc: Deletes the entry and persists the change to disk.
 * param: key - the entry key to remove.
 */
func (m *Memory) Delete(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.entries, key)
	m.saveLocked()
}

/*
 * Prune removes expired entries.
 * desc: Scans all entries, deletes those past their expiration, and persists
 *       if any were removed.
 * return: the number of entries pruned.
 */
func (m *Memory) Prune() int {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	pruned := 0
	for key, entry := range m.entries {
		if !entry.ExpiresAt.IsZero() && now.After(entry.ExpiresAt) {
			delete(m.entries, key)
			pruned++
		}
	}
	if pruned > 0 {
		m.saveLocked()
	}
	return pruned
}

/*
 * load reads persisted entries from the JSON file on disk.
 * desc: Called once during NewMemory initialization. Silently ignores
 *       missing files; logs parse errors.
 */
func (m *Memory) load() {
	data, err := os.ReadFile(m.path)
	if err != nil {
		return
	}
	var entries []*MemEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		log.Printf("[agent] memory load error: %v", err)
		return
	}
	for _, e := range entries {
		m.entries[e.Key] = e
	}
}

/*
 * saveLocked persists all entries to the JSON file on disk.
 * desc: Writes to a temporary file first and atomically renames for crash safety.
 *       Caller must hold m.mu write lock.
 */
func (m *Memory) saveLocked() {
	entries := make([]*MemEntry, 0, len(m.entries))
	for _, e := range m.entries {
		entries = append(entries, e)
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		log.Printf("[agent] memory save error: %v", err)
		return
	}
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		log.Printf("[agent] memory write error: %v", err)
		return
	}
	if err := os.Rename(tmp, m.path); err != nil {
		log.Printf("[agent] memory rename error: %v", err)
	}
}
