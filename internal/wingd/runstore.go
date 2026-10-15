package wingd

// RunStore supplies concurrent admission lookups and cancellation from the owned runs store.
type RunStore interface {
	IsRunTerminal(runID string) (bool, error)
	FinalizeRun(runID string)
	FinalizeCancelledRuns(runIDs []string, reason string) error
	Ready() error
}
