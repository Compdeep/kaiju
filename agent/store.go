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
	ID string
	// NodeID is the machine that ran it, not the machine it was about — see
	// Source and Target below.
	NodeID string

	// TriggerType is the application's own word for what started this —
	// a chat message, an event, a schedule. Opaque to the engine.
	TriggerType string
	// CorrelationID ties this run back to whatever caused it, so an
	// application can group runs by their originating event.
	CorrelationID string

	// Source and Target are the trigger's routing, carried through exactly as
	// it set them and never interpreted here.
	//
	// They are here because NodeID answers a different question. NodeID is the
	// machine that did the work, which is the only machine this package can
	// name truthfully. An application often wants the machine the work was
	// ABOUT — the host an event came from, or the one a command was aimed at —
	// and the rule for choosing between them is the application's. Without
	// these it cannot apply that rule, because nothing else on this record says
	// where the trigger came from.
	Source string
	Target string

	StartedAt   int64
	CompletedAt int64
	DurationMs  int64

	Intent  string // IGX intent the run was gated at
	DAGMode string

	NodesCount int
	LLMCalls   int
	// ReflectionCount is how many points the run stopped to reconsider —
	// reflection and interjection nodes alike.
	ReflectionCount int
	// FollowUpCount is how many of those went on to graft further work rather
	// than concluding. It was called ReplanCount, after the column an
	// application happened to persist it in, while "replan" already meant
	// something else here — see ReplanRecords and MaxReplans. An application
	// may still store it in whatever column it likes.
	FollowUpCount int

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
