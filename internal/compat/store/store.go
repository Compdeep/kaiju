// Package store provides a compatibility shim for the omamori event store.
// The agent code nil-checks eventStore before every call, so passing nil
// or a NoopStore both work. This package exists so the copied agent code
// compiles with only an import-path rewrite.
package store

// Store is the persistence backend for investigations and actions.
// In kaiju v0.1 we use NoopStore; a real implementation can be swapped in later.
type Store struct {
	insert func(inv Investigation) error
	action func(act Action) error
}

// NewNoopStore returns a Store that discards all writes.
func NewNoopStore() *Store {
	return &Store{
		insert: func(Investigation) error { return nil },
		action: func(Action) error { return nil },
	}
}

func (s *Store) InsertInvestigation(inv Investigation) error {
	if s == nil || s.insert == nil {
		return nil
	}
	return s.insert(inv)
}

func (s *Store) InsertAction(act Action) error {
	if s == nil || s.action == nil {
		return nil
	}
	return s.action(act)
}

// Investigation records metadata about a completed agent investigation.
type Investigation struct {
	ID              string
	NodeID          string
	TriggerType     string
	TriggerAlertID  string
	StartedAt       int64
	CompletedAt     int64
	DurationMs      int64
	Intent          string
	DAGMode         string
	NodesCount      int
	LLMCalls        int
	ReflectionCount int
	ReplanCount     int
	Verdict         string
	Severity        string
	Category        string
	Status          string
}

// Action records a side-effect action taken during investigation.
type Action struct {
	ID              string
	NodeID          string
	Timestamp       int64
	ActionType      string
	Params          string
	Result          string
	InvestigationID string
	Intent          int
	Impact          int
}
