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

// syncSubmission carries a trigger + a result channel for synchronous callers.
type syncSubmission struct {
	Trigger  Trigger
	ResultCh chan invResult
}

// invResult is the outcome of an investigation.
type invResult struct {
	Result *SyncResult
	Err    error
}

// ── Investigation ───────────────────────────────────────────────────────────

// Investigation is the kernel's view of a running investigation.
type Investigation struct {
	Trigger   Trigger
	StartedAt time.Time
	Cancel    context.CancelFunc
	ResultCh  chan invResult // nil for async, set for sync callers
}

// ── Kernel ──────────────────────────────────────────────────────────────────

// Kernel is the core runtime. Everything else — executive, scheduler,
// reflector, tools — are modules that plug into it. The kernel provides
// the main event loop, module lifecycle, and message passing.
type Kernel struct {
	agent   *Agent
	modules map[string]Module
	events  chan KernelEvent
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.RWMutex

	// Current investigation (nil when idle). One at a time.
	activeInv *Investigation
}

// NewKernel creates a kernel bound to an agent.
func NewKernel(agent *Agent) *Kernel {
	return &Kernel{
		agent:   agent,
		modules: make(map[string]Module),
		events:  make(chan KernelEvent, 64),
	}
}

// Register adds a module to the kernel. Must be called before Run.
func (k *Kernel) Register(m Module) {
	k.modules[m.Name()] = m
}

// Run starts the kernel's main event loop. Blocks until ctx is cancelled.
func (k *Kernel) Run(ctx context.Context) {
	k.ctx, k.cancel = context.WithCancel(ctx)

	// Start all modules
	for name, m := range k.modules {
		if err := m.Start(k); err != nil {
			log.Printf("[kernel] module %s failed to start: %v", name, err)
		} else {
			log.Printf("[kernel] module %s started", name)
		}
	}

	log.Printf("[kernel] running (%d modules)", len(k.modules))

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

// Submit fires an async investigation (fire and forget).
func (k *Kernel) Submit(trigger Trigger) {
	k.Emit(KernelEvent{Type: "investigation.submit", Data: trigger})
}

// SubmitSync submits an investigation and blocks until the result is ready.
// Used by the API handler and CLI.
func (k *Kernel) SubmitSync(ctx context.Context, trigger Trigger) (*SyncResult, error) {
	resultCh := make(chan invResult, 1)
	k.Emit(KernelEvent{
		Type: "investigation.submit.sync",
		Data: syncSubmission{Trigger: trigger, ResultCh: resultCh},
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-resultCh:
		return r.Result, r.Err
	}
}

// Agent returns the underlying agent for modules that need shared resources.
func (k *Kernel) Agent() *Agent {
	return k.agent
}

// ActiveInvestigation returns the current investigation, or nil.
func (k *Kernel) ActiveInvestigation() *Investigation {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return k.activeInv
}

// ── Internal ────────────────────────────────────────────────────────────────

func (k *Kernel) dispatch(ev KernelEvent) {
	switch ev.Type {
	case "investigation.submit":
		trigger, ok := ev.Data.(Trigger)
		if !ok {
			log.Printf("[kernel] invalid trigger data for investigation.submit")
			return
		}
		k.startInvestigation(trigger, nil)

	case "investigation.submit.sync":
		sub, ok := ev.Data.(syncSubmission)
		if !ok {
			log.Printf("[kernel] invalid data for investigation.submit.sync")
			return
		}
		k.startInvestigation(sub.Trigger, sub.ResultCh)

	case "investigation.complete":
		result, ok := ev.Data.(invResult)
		if !ok {
			log.Printf("[kernel] invalid data for investigation.complete")
			return
		}
		k.completeInvestigation(result)

	case "heartbeat.progress":
		k.handleProgressCheck()
	}
}

func (k *Kernel) startInvestigation(trigger Trigger, resultCh chan invResult) {
	k.mu.Lock()
	if k.activeInv != nil {
		k.mu.Unlock()
		if resultCh != nil {
			resultCh <- invResult{Err: fmt.Errorf("investigation already running")}
		}
		return
	}

	invCtx, invCancel := context.WithCancel(k.ctx)

	k.activeInv = &Investigation{
		Trigger:   trigger,
		StartedAt: time.Now(),
		Cancel:    invCancel,
		ResultCh:  resultCh,
	}
	k.mu.Unlock()

	log.Printf("[kernel] investigation started: %s", trigger.AlertID)

	go func() {
		result, err := k.agent.RunDAGSync(invCtx, trigger)
		k.Emit(KernelEvent{
			Type: "investigation.complete",
			Data: invResult{Result: result, Err: err},
		})
	}()
}

func (k *Kernel) completeInvestigation(result invResult) {
	k.mu.Lock()
	inv := k.activeInv
	k.activeInv = nil
	k.mu.Unlock()

	if inv == nil {
		return
	}

	elapsed := time.Since(inv.StartedAt)
	log.Printf("[kernel] investigation complete in %s", elapsed.Round(time.Millisecond))

	// Return result to sync caller if present
	if inv.ResultCh != nil {
		inv.ResultCh <- result
	}
}

func (k *Kernel) handleProgressCheck() {
	k.mu.RLock()
	inv := k.activeInv
	k.mu.RUnlock()

	if inv == nil {
		return
	}

	elapsed := time.Since(inv.StartedAt)

	// Only check after 90 seconds
	if elapsed < 90*time.Second {
		return
	}

	// Read last few worklog entries from the active investigation's session.
	sid := ""
	k.agent.dagMu.Lock()
	if k.agent.dagGraph != nil {
		sid = k.agent.dagGraph.SessionID
	}
	k.agent.dagMu.Unlock()
	worklog := readWorklog(k.agent.cfg.MetadataDir, sid, 5)
	if worklog == "" {
		return
	}

	// Check if stuck
	lines := strings.Split(strings.TrimSpace(worklog), "\n")
	failCount := 0
	for _, l := range lines {
		if strings.Contains(l, "VALIDATION_FAIL") || strings.Contains(l, "BASH_ERROR") || strings.Contains(l, "RETRIES_EXHAUSTED") {
			failCount++
		}
	}

	if failCount >= 3 {
		log.Printf("[kernel] heartbeat: investigation stuck (%d failures in last 5 entries, %s elapsed), injecting progress check",
			failCount, elapsed.Round(time.Second))
		k.agent.Interject(fmt.Sprintf(
			"Progress check (%s elapsed): The last several steps have failed with the same errors. "+
				"Are you making progress or stuck in a loop? "+
				"If stuck, conclude with current status and explain what failed. "+
				"If making progress, continue.",
			elapsed.Round(time.Second)))
	}
}

func (k *Kernel) shutdown() {
	log.Printf("[kernel] shutting down")

	// Cancel active investigation
	k.mu.Lock()
	if k.activeInv != nil {
		k.activeInv.Cancel()
		if k.activeInv.ResultCh != nil {
			k.activeInv.ResultCh <- invResult{Err: fmt.Errorf("kernel shutting down")}
		}
		k.activeInv = nil
	}
	k.mu.Unlock()

	// Stop all modules
	for name, m := range k.modules {
		if err := m.Stop(); err != nil {
			log.Printf("[kernel] module %s stop error: %v", name, err)
		}
	}
}
