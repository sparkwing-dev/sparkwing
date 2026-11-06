package wingd

import "github.com/sparkwing-dev/sparkwing/pkg/wingwire"

// RunStore supplies concurrent admission lookups and cancellation from the owned runs store.
type RunStore interface {
	IsRunTerminal(runID wingwire.RunID) (bool, error)
	FinalizeCancelledRuns(runIDs []string, reason string) error
	Ready() error
}
