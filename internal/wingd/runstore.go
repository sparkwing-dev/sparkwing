package wingd

// RunStore is the daemon's view of the runs store. The daemon reaches the
// store only through this interface, so internal/wingd links no store
// package and the handle's lifetime is the host's business.
//
// Implementations must be safe for concurrent use and must not assume the
// store is open: a daemon whose store is unreadable keeps admitting work and
// reports the reason through Ready.
type RunStore interface {
	// IsRunTerminal reports whether the run has already finished. An error
	// means the store could not answer, and admission evicts the run with
	// that reason rather than admitting it blind.
	IsRunTerminal(runID string) (bool, error)
	// FinalizeRun marks a run cancelled because its admission connection
	// went away without releasing. It reports its own failures.
	FinalizeRun(runID string)
	// FinalizeCancelledRuns marks every named run cancelled with reason.
	FinalizeCancelledRuns(runIDs []string, reason string) error
	// Ready returns nil when the store is open and usable, else the reason
	// it is not. It never blocks on the store.
	Ready() error
}

// FuncRunStore adapts closures to [RunStore] for a host that holds no store
// handle. A nil IsRunTerminal answers "not terminal"; a nil FinalizeRun is a
// no-op; a nil FinalizeCancelledRuns falls back to FinalizeRun per run.
type FuncRunStore struct {
	IsTerminal        func(runID string) (bool, error)
	Finalize          func(runID string)
	FinalizeCancelled func(runIDs []string, reason string) error
	// NotReady is what Ready reports. Nil means ready.
	NotReady error
}

func (f *FuncRunStore) IsRunTerminal(runID string) (bool, error) {
	if f == nil || f.IsTerminal == nil {
		return false, nil
	}
	return f.IsTerminal(runID)
}

func (f *FuncRunStore) FinalizeRun(runID string) {
	if f == nil || f.Finalize == nil {
		return
	}
	f.Finalize(runID)
}

func (f *FuncRunStore) FinalizeCancelledRuns(runIDs []string, reason string) error {
	if f == nil {
		return nil
	}
	if f.FinalizeCancelled != nil {
		return f.FinalizeCancelled(runIDs, reason)
	}
	for _, runID := range runIDs {
		f.FinalizeRun(runID)
	}
	return nil
}

func (f *FuncRunStore) Ready() error {
	if f == nil {
		return nil
	}
	return f.NotReady
}
