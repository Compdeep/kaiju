package agent

import (
	"time"
)

// HeartbeatModule periodically checks investigation progress and injects
// interjections when the system appears stuck.
type HeartbeatModule struct {
	kernel   *Kernel
	interval time.Duration
	stop     chan struct{}
}

// NewHeartbeatModule creates a heartbeat with the given check interval.
func NewHeartbeatModule(interval time.Duration) *HeartbeatModule {
	return &HeartbeatModule{
		interval: interval,
		stop:     make(chan struct{}),
	}
}

func (h *HeartbeatModule) Name() string { return "heartbeat" }

func (h *HeartbeatModule) Start(k *Kernel) error {
	h.kernel = k
	go h.run()
	return nil
}

func (h *HeartbeatModule) Stop() error {
	close(h.stop)
	return nil
}

func (h *HeartbeatModule) run() {
	ticker := time.NewTicker(h.interval)
	defer ticker.Stop()
	for {
		select {
		case <-h.stop:
			return
		case <-ticker.C:
			h.kernel.Emit(KernelEvent{Type: "heartbeat.progress"})
		}
	}
}
