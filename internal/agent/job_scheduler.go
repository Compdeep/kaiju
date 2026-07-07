package agent

import (
	"container/heap"
	"context"
	"errors"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

// Priority orders jobs in the scheduler — lower runs first.
type Priority int

const (
	PriorityChat       Priority = 0  // interactive, operator-facing
	PriorityBackground Priority = 10 // fire-and-forget / non-interactive triggers
)

const (
	// defaultConcurrency is the fallback worker-pool size when the config
	// (agent.MaxConcurrentInvestigations / dag.max_concurrent) does not set one.
	// The per-Graph state work (parallelism spine) makes >1 safe, and jobs are
	// keyed by session so distinct callers run on distinct workers; a reserved
	// chat lane keeps one worker free for interactive callers under background
	// load. Explicit per-principal fairness caps remain a Phase-2 follow-up.
	defaultConcurrency = 3
	// maxQueueDepth bounds the pending queue; beyond it new background jobs are
	// dropped (chat is exempt — see enqueue).
	maxQueueDepth = 100
)

// schedulerWorkers resolves a configured concurrency to a worker count, falling
// back to defaultConcurrency when unset or invalid (<1).
func schedulerWorkers(configured int) int {
	if configured < 1 {
		return defaultConcurrency
	}
	return configured
}

// ErrPreempted is returned to a chat caller whose in-flight (or queued) job was
// replaced by a newer message in the same session.
var ErrPreempted = errors.New("query interjected by user")

// ErrQueueFull is returned when the pending queue is at capacity.
var ErrQueueFull = errors.New("scheduler queue full")

// interjectBuffer bounds how many un-read steering messages a running query
// holds. Beyond it, further steers are dropped (and logged) rather than queued
// — no human types faster than the query reads, so this is just a safety valve.
const interjectBuffer = 8

// Job is one unit of scheduled work.
type Job struct {
	trigger   Trigger
	priority  Priority
	session   string
	ctx       context.Context
	cancel    context.CancelFunc
	resultCh  chan invResult // non-nil for synchronous callers (chat)
	interject chan string    // per-query steering channel; read by the running investigation
	seq       uint64
	preempted atomic.Bool
	index     int // heap index, maintained by jobHeap

	// Heartbeat fields. startedAt is set once by the worker when the job begins;
	// stuckCount is owned solely by the kernel heartbeat (its only writer), so no
	// scheduler code touches it.
	startedAt  time.Time
	stuckCount int
}

// jobHeap is a min-heap ordered by (priority, seq): higher priority first, FIFO
// within a priority class.
type jobHeap []*Job

func (h jobHeap) Len() int { return len(h) }
func (h jobHeap) Less(i, j int) bool {
	if h[i].priority != h[j].priority {
		return h[i].priority < h[j].priority
	}
	return h[i].seq < h[j].seq
}
func (h jobHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index, h[j].index = i, j
}
func (h *jobHeap) Push(x any) {
	j := x.(*Job)
	j.index = len(*h)
	*h = append(*h, j)
}
func (h *jobHeap) Pop() any {
	old := *h
	n := len(old)
	j := old[n-1]
	old[n-1] = nil
	j.index = -1
	*h = old[:n-1]
	return j
}

// Scheduler is the single front door for investigations: one priority queue
// feeding a worker pool. Chat outranks background triggers; a new chat message
// preempts the running job for the SAME session only; a job replaces a
// still-queued one with the same session key (chat: newest wins; background:
// dedupe a repeat trigger). It is the receive-side complement that removes the
// old single-flight (the kernel's activeInv + the global investigating flag).
type Scheduler struct {
	exec     func(context.Context, Trigger) (*SyncResult, error)
	workers  int
	maxDepth int

	// maxBackground caps how many background jobs run at once: workers-1 (min 1),
	// so when there are 2+ workers one is always free for an interactive chat
	// query even under a flood of background triggers. At workers=1 it is 1 (no
	// reservation possible — a lone worker must still run background).
	maxBackground int

	mu          sync.Mutex
	cond        *sync.Cond
	pq          jobHeap
	queued      map[string]*Job // session → pending job (supersede / dedupe)
	running     map[string]*Job // session → in-flight job (preempt + status)
	runningBg   int             // background jobs currently executing (for the chat reserve)
	liveWorkers int             // worker goroutines actually running (for live resize; workers is the target)
	seq         uint64
	closed      bool
}

// newScheduler builds a scheduler. exec is the investigation executor
// (a.RunDAGSync). workers < 1 is treated as 1.
func newScheduler(exec func(context.Context, Trigger) (*SyncResult, error), workers, maxDepth int) *Scheduler {
	if workers < 1 {
		workers = 1
	}
	maxBg := workers - 1
	if maxBg < 1 {
		maxBg = 1
	}
	s := &Scheduler{
		exec: exec, workers: workers, maxDepth: maxDepth, maxBackground: maxBg,
		queued: map[string]*Job{}, running: map[string]*Job{},
	}
	s.cond = sync.NewCond(&s.mu)
	return s
}

// Start launches the worker pool; workers exit once ctx is cancelled and the
// queue has drained.
func (s *Scheduler) Start(ctx context.Context) {
	go func() {
		<-ctx.Done()
		s.mu.Lock()
		s.closed = true
		s.cond.Broadcast()
		s.mu.Unlock()
	}()
	s.mu.Lock()
	n := s.workers
	s.liveWorkers = n
	s.mu.Unlock()
	for i := 0; i < n; i++ {
		go s.worker()
	}
}

// Submit enqueues a fire-and-forget job. Non-blocking.
func (s *Scheduler) Submit(trigger Trigger, priority Priority, session string) {
	s.enqueue(context.Background(), trigger, priority, session, nil)
}

// SubmitSync enqueues a job and blocks until it completes, is preempted by a
// newer same-session message (ErrPreempted), or ctx is cancelled. Used by chat.
func (s *Scheduler) SubmitSync(ctx context.Context, trigger Trigger, priority Priority, session string) (*SyncResult, error) {
	resultCh := make(chan invResult, 1)
	if !s.enqueue(ctx, trigger, priority, session, resultCh) {
		return nil, ErrQueueFull
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-resultCh:
		return r.Result, r.Err
	}
}

// Interject routes a steering message into the query already running for this
// session. It returns true if a query is running for the session (the message
// was handled as a steer — queued onto that query, or dropped-and-logged if its
// buffer is full); false if no query is running, in which case the caller should
// start a new query instead. A steer to a running query is never turned into a
// second query, however many arrive.
func (s *Scheduler) Interject(session, msg string) bool {
	s.mu.Lock()
	job := s.running[session]
	s.mu.Unlock()
	if job == nil {
		return false
	}
	select {
	case job.interject <- msg:
	default:
		log.Printf("WARNING - DROPPING INTERJECTION: buffer full (%d) for session=%s", interjectBuffer, session)
	}
	return true
}

// SetConcurrency live-resizes the worker pool. Growing spawns workers
// immediately; shrinking retires idle or just-finished workers down to n (jobs
// in flight are never interrupted). n < 1 is clamped to 1. Also retunes the
// reserved chat lane (max background = n-1).
func (s *Scheduler) SetConcurrency(n int) {
	if n < 1 {
		n = 1
	}
	s.mu.Lock()
	s.workers = n
	s.maxBackground = n - 1
	if s.maxBackground < 1 {
		s.maxBackground = 1
	}
	spawn := 0
	if n > s.liveWorkers {
		spawn = n - s.liveWorkers
		s.liveWorkers = n
	}
	s.cond.Broadcast() // wake idle workers: take up new capacity, or retire if over target
	s.mu.Unlock()
	for i := 0; i < spawn; i++ {
		go s.worker()
	}
}

// Concurrency returns the current target worker count.
func (s *Scheduler) Concurrency() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.workers
}

// AnyRunning reports whether any job is currently executing (status shim).
func (s *Scheduler) AnyRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.running) > 0
}

// RunningJobs returns a snapshot of the jobs currently executing. The kernel
// heartbeat reads these to detect stuck investigations. The *Job pointers are
// live; only the heartbeat mutates job.stuckCount, so it is that field's sole
// writer.
func (s *Scheduler) RunningJobs() []*Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Job, 0, len(s.running))
	for _, j := range s.running {
		out = append(out, j)
	}
	return out
}

func (s *Scheduler) enqueue(parent context.Context, trigger Trigger, priority Priority, session string, resultCh chan invResult) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	// The depth cap bounds a flood of BACKGROUND jobs. An interactive chat is
	// exempt: it is operator-driven and session-deduped (a newer message
	// supersedes a queued one, or steers a running one), it outranks background
	// work, and a worker is reserved for it — so it must flow through even when
	// the background queue is at cap, rather than being rejected with "queue full".
	if priority != PriorityChat && len(s.pq) >= s.maxDepth {
		log.Printf("WARNING - DROPPING QUERY: queue at cap (%d): type=%s session=%s", s.maxDepth, trigger.Type, session)
		return false
	}

	jobCtx, cancel := context.WithCancel(parent)
	s.seq++
	job := &Job{trigger: trigger, priority: priority, session: session, ctx: jobCtx, cancel: cancel, resultCh: resultCh, interject: make(chan string, interjectBuffer), seq: s.seq}

	if session != "" {
		// A still-queued job for this session is superseded: chat = newest wins,
		// background = dedupe a repeat trigger. Its caller (if any) gets ErrPreempted.
		// A chat for a session whose query is already RUNNING never reaches here —
		// it is routed into that query as a steer (see Interject) — so the scheduler
		// never cancels a running query on a new chat.
		if old := s.queued[session]; old != nil {
			if old.index >= 0 && old.index < len(s.pq) {
				heap.Remove(&s.pq, old.index)
			}
			old.preempted.Store(true)
			old.cancel()
			deliver(old, invResult{Err: ErrPreempted})
		}
		s.queued[session] = job
	}

	heap.Push(&s.pq, job)
	s.cond.Signal()
	return true
}

// pickLocked returns the next job a worker should run, or nil if none is
// currently eligible. Must be called with s.mu held. The heap's top is the
// highest-priority job; chat (priority 0) always sorts above background
// (priority 10), so a background job at the top means no chat is waiting. In
// that case the job is held back while the background concurrency cap is
// reached, keeping a worker free for an incoming chat query. While closed, the
// reserve is dropped so shutdown drains every remaining job.
func (s *Scheduler) pickLocked() *Job {
	if len(s.pq) == 0 {
		return nil
	}
	if top := s.pq[0]; top.priority == PriorityBackground && !s.closed && s.runningBg >= s.maxBackground {
		return nil
	}
	return heap.Pop(&s.pq).(*Job)
}

func (s *Scheduler) worker() {
	for {
		s.mu.Lock()
		var job *Job
		for {
			// Pool shrunk below the live count → this worker retires.
			if s.liveWorkers > s.workers {
				s.liveWorkers--
				s.mu.Unlock()
				return
			}
			job = s.pickLocked()
			if job != nil {
				break
			}
			if s.closed && len(s.pq) == 0 {
				s.liveWorkers--
				s.mu.Unlock()
				return
			}
			s.cond.Wait()
		}
		if job.session != "" && s.queued[job.session] == job {
			delete(s.queued, job.session)
		}
		job.startedAt = time.Now()
		if job.session != "" {
			s.running[job.session] = job
		}
		if job.priority == PriorityBackground {
			s.runningBg++
		}
		s.mu.Unlock()

		var res *SyncResult
		var err error
		if job.ctx.Err() == nil {
			res, err = s.exec(withInterject(job.ctx, job.interject), job.trigger)
		} else {
			err = job.ctx.Err()
		}

		s.mu.Lock()
		if job.priority == PriorityBackground {
			s.runningBg--
		}
		if job.session != "" && s.running[job.session] == job {
			delete(s.running, job.session)
		}
		// A freed background slot may make a held-back background job eligible.
		s.cond.Signal()
		s.mu.Unlock()

		if job.preempted.Load() {
			deliver(job, invResult{Err: ErrPreempted})
		} else {
			deliver(job, invResult{Result: res, Err: err})
		}
		job.cancel()
	}
}

// deliver sends a result to a synchronous caller (no-op for fire-and-forget).
// The result channel is buffered (cap 1), so this never blocks.
func deliver(job *Job, r invResult) {
	if job.resultCh != nil {
		select {
		case job.resultCh <- r:
		default:
		}
	}
}

// interjectKey carries a running query's steering channel through the context
// the scheduler hands to the executor.
type interjectKey struct{}

// withInterject returns a context carrying the query's steering channel.
func withInterject(ctx context.Context, ch chan string) context.Context {
	return context.WithValue(ctx, interjectKey{}, ch)
}

// interjectFrom returns the steering channel carried by ctx, or nil if none.
func interjectFrom(ctx context.Context) chan string {
	if ch, ok := ctx.Value(interjectKey{}).(chan string); ok {
		return ch
	}
	return nil
}
