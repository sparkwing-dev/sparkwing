package wingd

import "github.com/sparkwing-dev/sparkwing/pkg/wingwire"

// MaxUnsupportedReplies is maxUnsupportedReplies for external tests.
const MaxUnsupportedReplies = maxUnsupportedReplies

// MaxRefusedTypeName is maxRefusedTypeName for external tests.
const MaxRefusedTypeName = maxRefusedTypeName

// FuncRunStore supplies scripted run state to admission tests.
type FuncRunStore struct {
	IsTerminal        func(runID string) (bool, error)
	Finalize          func(runID string)
	FinalizeCancelled func(runIDs []string, reason string) error
	NotReady          error
}

func (f *FuncRunStore) IsRunTerminal(runID wingwire.RunID) (bool, error) {
	if f == nil || f.IsTerminal == nil {
		return false, nil
	}
	return f.IsTerminal(string(runID))
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
