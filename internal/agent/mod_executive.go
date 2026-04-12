package agent

// ExecutiveModule is a placeholder for the executive's kernel integration.
// Currently passive — the kernel calls RunDAGSync directly. In the future,
// the executive could listen for events and self-start investigations.
type ExecutiveModule struct {
	kernel *Kernel
}

func (e *ExecutiveModule) Name() string { return "executive" }

func (e *ExecutiveModule) Start(k *Kernel) error {
	e.kernel = k
	return nil
}

func (e *ExecutiveModule) Stop() error { return nil }
