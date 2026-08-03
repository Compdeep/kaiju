package agent

// Recording what a run did.
//
// The engine keeps no storage of its own. If an application wants a record of
// runs and the actions taken during them, it supplies an EventStore and the
// engine hands it rows as they complete. Nothing is written when none is set,
// and no behaviour depends on the writes succeeding — this is a record, not
// state the engine reads back.

// EventStore receives a record of each completed run and each action taken.
//
// Implemented by the application. Errors are logged and otherwise ignored: a
// failure to record must not fail the work that was recorded.
type EventStore interface {
	InsertRun(Run) error
	InsertAction(Action) error
}

// Run is one completed execution: what triggered it, how it was planned, what
// it cost, and what it concluded.
type Run struct {
	ID     string
	NodeID string // the machine that ran it

	// TriggerType is the application's own word for what started this —
	// a chat message, an event, a schedule. Opaque to the engine.
	TriggerType string
	// CorrelationID ties this run back to whatever caused it, so an
	// application can group runs by their originating event.
	CorrelationID string

	StartedAt   int64
	CompletedAt int64
	DurationMs  int64

	Intent  string // IGX intent the run was gated at
	DAGMode string

	NodesCount      int
	LLMCalls        int
	ReflectionCount int
	ReplanCount     int

	// Verdict, Severity, Category and Status are the application's
	// conclusion. The engine passes through whatever the aggregator produced
	// and attaches no meaning to the values.
	Verdict  string
	Severity string
	Category string
	Status   string
}

// Action is one state-changing tool call made during a run.
type Action struct {
	ID         string
	NodeID     string // the machine it ran on
	Timestamp  int64
	ActionType string // the tool name
	Params     string // JSON
	Result     string
	RunID      string
	Intent     int // IGX intent it was gated at
	Impact     int // the tool's declared impact
}
