package agent

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

// ── Module Interface ────────────────────────────────────────────────────────

// Module is the interface every kernel module implements.
type Module interface {
	Name() string
	Start(k *Kernel) error
	Stop() error
}

// ── Kernel Events ───────────────────────────────────────────────────────────

// KernelEvent is a message on the kernel's internal event bus.
type KernelEvent struct {
	Type string
	Data interface{}
}

// invResult is the outcome of an investigation.
type invResult struct {
	Result *SyncResult
	Err    error
}

// ── Kernel ──────────────────────────────────────────────────────────────────

// Kernel is the core runtime. Everything else — the scheduler, heartbeat,
// executive, and future subsystems — are owned and managed by the kernel, which
// provides the front door (Submit/SubmitSync), the event loop, and module
// lifecycle. The scheduler is the kernel's work-dispatch subsystem: a priority
// queue + worker pool that replaced the old single-flight activeInv.
type Kernel struct {
	agent     *Agent
	modules   map[string]Module
	events    chan KernelEvent
	scheduler *Scheduler // work-dispatch subsystem (priority queue + worker pool)
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.RWMutex
}

// NewKernel creates a kernel bound to an agent, with its scheduler wired to
// a.RunDAGSync. Modules are registered by the caller (Agent.InitKernel) before
// Run. Worker-pool size comes from cfg.MaxConcurrentInvestigations (0 => 1).
func NewKernel(agent *Agent) *Kernel {
	return &Kernel{
		agent:     agent,
		modules:   make(map[string]Module),
		events:    make(chan KernelEvent, 64),
		scheduler: newScheduler(agent.RunDAGSync, schedulerWorkers(agent.cfg.MaxConcurrentInvestigations), maxQueueDepth),
	}
}

// Register adds a module to the kernel. Must be called before Run.
func (k *Kernel) Register(m Module) {
	k.modules[m.Name()] = m
}

// Run starts the kernel's main event loop. Blocks until ctx is cancelled.
func (k *Kernel) Run(ctx context.Context) {
	k.ctx, k.cancel = context.WithCancel(ctx)

	// Start the work-dispatch subsystem, then the modules.
	k.scheduler.Start(k.ctx)

	for name, m := range k.modules {
		if err := m.Start(k); err != nil {
			log.Printf("[kernel] module %s failed to start: %v", name, err)
		} else {
			log.Printf("[kernel] module %s started", name)
		}
	}

	log.Printf("[kernel] running (%d modules, scheduler workers=%d)", len(k.modules), k.scheduler.workers)

	// Main event loop
	for {
		select {
		case <-k.ctx.Done():
			k.shutdown()
			return
		case ev := <-k.events:
			k.dispatch(ev)
		}
	}
}

// Emit sends an event to the kernel's event bus. Non-blocking — drops if full.
func (k *Kernel) Emit(ev KernelEvent) {
	select {
	case k.events <- ev:
	default:
		log.Printf("[kernel] event dropped (bus full): %s", ev.Type)
	}
}

// Submit queues a fire-and-forget investigation through the scheduler.
func (k *Kernel) Submit(trigger Trigger) {
	prio, session := schedulePolicy(trigger)
	k.scheduler.Submit(trigger, prio, session)
}

// SubmitSync queues an investigation through the scheduler and blocks until it
// completes, is preempted by a newer same-session message (ErrPreempted), or ctx
// is cancelled. Used by the chat path.
func (k *Kernel) SubmitSync(ctx context.Context, trigger Trigger) (*SyncResult, error) {
	prio, session := schedulePolicy(trigger)
	return k.scheduler.SubmitSync(ctx, trigger, prio, session)
}

// InFlight reports whether any investigation is currently executing.
func (k *Kernel) InFlight() bool { return k.scheduler.AnyRunning() }

// Interject steers the query running for a session via the scheduler. Returns
// true if a query was running for the session, false if none was.
func (k *Kernel) Interject(session, msg string) bool {
	return k.scheduler.Interject(session, msg)
}

// SetConcurrency live-resizes how many investigations run at once.
func (k *Kernel) SetConcurrency(n int) { k.scheduler.SetConcurrency(n) }

// Concurrency returns the current concurrent-investigation limit.
func (k *Kernel) Concurrency() int { return k.scheduler.Concurrency() }

// Agent returns the underlying agent for modules that need shared resources.
func (k *Kernel) Agent() *Agent {
	return k.agent
}

// schedulePolicy maps a trigger to its scheduler priority and session key. Both
// key on the trigger's SessionID — an opaque conversation/thread identifier the
// host assigns (for makeen, derived from the caller principal). Interactive chat
// outranks background work and a newer message steers the running query;
// everything else is background and a repeat dedupes a still-queued copy. An
// empty SessionID means no dedupe/steer key. The kernel stays ignorant of what a
// session means.
func schedulePolicy(t Trigger) (Priority, string) {
	if t.Type == "chat_query" {
		return PriorityChat, t.SessionID
	}
	return PriorityBackground, t.SessionID
}

// ── Internal ────────────────────────────────────────────────────────────────

func (k *Kernel) dispatch(ev KernelEvent) {
	switch ev.Type {
	case "heartbeat.progress":
		k.handleProgressCheck()
	}
}

// handleProgressCheck nudges any investigation that appears stuck. It reads the
// scheduler's running set, so it covers every in-flight investigation (one per
// worker), not a single global one.
func (k *Kernel) handleProgressCheck() {
	for _, job := range k.scheduler.RunningJobs() {
		k.checkJobProgress(job)
	}
}

// checkJobProgress counts recent failures in a running investigation's worklog
// and, after a bounded number of consecutive stuck ticks, interjects a progress
// check rather than letting it spin on the same failing fix. job.stuckCount is
// written only here (the heartbeat runs single-threaded).
func (k *Kernel) checkJobProgress(job *Job) {
	worklog := readWorklog(k.agent.cfg.MetadataDir, job.trigger.SessionID, 5)
	if worklog == "" {
		return
	}
	failCount := 0
	for _, l := range strings.Split(strings.TrimSpace(worklog), "\n") {
		if strings.Contains(l, "VALIDATION_FAIL") || strings.Contains(l, "BASH_ERROR") || strings.Contains(l, "RETRIES_EXHAUSTED") {
			failCount++
		}
	}
	// Tick up while failures dominate, reset when progress resumes.
	if failCount >= 3 {
		job.stuckCount++
	} else {
		job.stuckCount = 0
	}
	stuck := job.stuckCount
	threshold := job.trigger.HeartbeatThreshold
	if threshold <= 0 {
		threshold = 3 // default ~90s at a 30s tick
	}
	// Interject on first crossing and every `threshold` ticks thereafter —
	// bounded escalation, not an unbounded loop.
	if stuck == 0 || stuck%threshold != 0 {
		return
	}

	elapsed := time.Since(job.startedAt).Round(time.Second)
	log.Printf("[kernel] heartbeat: investigation %s stuck for %d consecutive ticks (%s elapsed, threshold=%d), injecting progress check",
		job.trigger.AlertID, stuck, elapsed, threshold)
	k.Interject(job.trigger.SessionID, fmt.Sprintf(
		"Progress check (%s elapsed, %d consecutive stuck ticks): recent steps keep failing. "+
			"Investigate the root cause via Holmes — don't keep retrying the same fix. "+
			"If Holmes has already run multiple times and can't find a fix, conclude honestly.",
		elapsed, stuck))
}

func (k *Kernel) shutdown() {
	log.Printf("[kernel] shutting down")
	// The scheduler stops via k.ctx — its workers drain and exit on their own.
	for name, m := range k.modules {
		if err := m.Stop(); err != nil {
			log.Printf("[kernel] module %s stop error: %v", name, err)
		}
	}
}
