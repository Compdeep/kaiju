package agent

import (
	"encoding/json"
	"sync"
	"time"
)

// What each stage of a run was given, and what it produced.
//
// A stage's output is scattered today: Evidence() for the prose, Payload() for
// the fields, a NodeInfo for the trace, a worklog line for the timeline. Four
// renderings, written in four places, and nothing that says "this is what stage
// X produced". They can disagree — a process_list that matched nothing rendered
// as ok in one, a bare header in another, and a truncated string in a third, and
// the stages downstream believed the third.
//
// So each stage records once, here, as it finishes. Then a test can ask the
// question no test could ask before: this value existed at step 2 — which later
// stages actually received it?
//
// WRITE-ONLY. Nothing in the engine reads a record back, and no record ever
// reaches a model. That is the whole safety argument: a mistake in this file
// gives a test a wrong answer, and cannot give a RUN a wrong answer. It observes
// the run; it is not part of it.

// DebugRecord is one stage's turn, as data.
type DebugRecord struct {
	Seq    int             `json:"seq"`             // order within the run
	ID     string          `json:"id"`              // node id, or a name for a stage that is not a node
	Kind   string          `json:"kind"`            // tool, compute, reflection, executive, aggregator, edge, preflight
	Label  string          `json:"label,omitempty"` // the step's name, or the reader an edge serves
	Round  int             `json:"round"`           // which arc
	Tool   string          `json:"tool,omitempty"`
	In     []string        `json:"in,omitempty"`     // ids of the stages whose output this was given
	Params map[string]any  `json:"params,omitempty"` // what it was called with
	Out    json.RawMessage `json:"out,omitempty"`    // what it produced, as fields
	Text   string          `json:"text,omitempty"`   // what it produced, as prose
	Err    string          `json:"err,omitempty"`
	Ms     int64           `json:"ms,omitempty"`

	// For a stage that called a model. These are what makes the per-run files
	// under debugLogDir worth reading — the prompts as sent and the reply as
	// returned — and a record without them would be a worse version of a thing
	// that already works. Empty for a stage that called no model.
	Model     string            `json:"model,omitempty"`
	System    string            `json:"system,omitempty"` // the system prompt, as sent
	User      string            `json:"user,omitempty"`   // the user prompt, as sent
	Reply     string            `json:"reply,omitempty"`  // the model's output, before any parsing
	Gate      map[string]string `json:"gate,omitempty"`   // what the context gate returned, by source
	TokensIn  int               `json:"tokens_in,omitempty"`
	TokensOut int               `json:"tokens_out,omitempty"`
}

// debugLog is a run's records. Its own lock, like capAccount: a record is
// written while a stage holds the graph's lock, and taking that lock here would
// deadlock.
type debugLog struct {
	mu      sync.Mutex
	records []DebugRecord
	seq     int
}

/*
 * recordStage notes what one stage produced.
 * desc: Called as a stage finishes, from wherever knows both what it was given
 *       and what came back. Records are appended in completion order, which is
 *       the order a reader wants: it is what the run actually did, rather than
 *       what the plan said it would.
 *
 *       A nil graph is a no-op — the ReAct loop builds no graph, and a run
 *       without one simply records nothing.
 * param: r - the record. Seq is assigned here, so a caller never sets it.
 */
func (g *Graph) recordStage(r DebugRecord) {
	if g == nil {
		return
	}
	d := &g.debug
	d.mu.Lock()
	defer d.mu.Unlock()
	d.seq++
	r.Seq = d.seq
	d.records = append(d.records, r)
}

/*
 * DebugRecords returns what each stage of this run produced, in the order they
 * finished.
 * return: a copy, so a reader cannot disturb the run.
 */
func (g *Graph) DebugRecords() []DebugRecord {
	if g == nil {
		return nil
	}
	d := &g.debug
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]DebugRecord, len(d.records))
	copy(out, d.records)
	return out
}

/*
 * recordNode records a node that has reached a terminal state.
 * desc: The node knows everything the record needs, so this is the one call
 *       site for every stage that IS a node. Stages that are not nodes — an
 *       edge, the executive, the aggregator — record themselves.
 *
 *       Called with the graph lock held, by the setters that resolve a node.
 * param: n - the node, already in its final state.
 */
func (g *Graph) recordNodeLocked(n *Node) {
	if g == nil || n == nil {
		return
	}
	r := DebugRecord{
		ID:     n.ID,
		Kind:   n.Type.String(),
		Label:  n.Tag,
		Round:  n.Round,
		Tool:   n.ToolName,
		In:     append([]string(nil), n.DependsOn...),
		Params: n.Params,
	}
	if n.SpawnedBy != "" {
		r.In = append(r.In, n.SpawnedBy)
	}
	if n.Body != nil {
		r.Out = n.Body.Payload()
		r.Text = n.Body.Evidence()
	} else {
		r.Text = n.Result
	}
	if n.Error != nil {
		r.Err = n.Error.Error()
	}
	if !n.StartedAt.IsZero() && !n.EndedAt.IsZero() {
		r.Ms = n.EndedAt.Sub(n.StartedAt).Milliseconds()
	} else if !n.StartedAt.IsZero() {
		r.Ms = time.Since(n.StartedAt).Milliseconds()
	}

	// Appended without the graph lock: recordStage takes the debug lock only,
	// and this runs while the caller holds the graph's. Taking them in this
	// order everywhere is what keeps that safe.
	d := &g.debug
	d.mu.Lock()
	d.seq++
	r.Seq = d.seq
	d.records = append(d.records, r)
	d.mu.Unlock()
}
