package nodehost

import (
	"context"
	"errors"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

const (
	waiterPoll        = 100 * time.Millisecond
	storeOutageBudget = 5 * time.Minute
)

// CapacityStore supplies tenant-scoped admission and continuous adoption.
type CapacityStore interface {
	AdmitCapacity(context.Context, store.CapacityBinding, store.Actor) (store.AcquireSlotResponse, error)
}

// storeAdmission binds sealed concurrency groups to their live run or job.
type storeAdmission struct {
	Store  CapacityStore
	Owner  store.Actor
	Queued func(context.Context, controllerwire.NodeRef, store.AcquireSlotResponse) error
}

// AdmissionFailure preserves a refusal's terminal outcome before execution starts.
type AdmissionFailure struct {
	Outcome store.NodeState
	Reason  string
	Err     error
}

func (e *AdmissionFailure) Error() string { return e.Err.Error() }
func (e *AdmissionFailure) Unwrap() error { return e.Err }

// Run retains plan capacity under the unit lease, including after an owner exits.
func (a *storeAdmission) Run(ctx context.Context, accepted AcceptedRun, body func(context.Context) (RunCompletion, error)) (RunCompletion, error) {
	spec, err := admissionSpec(accepted)
	if err != nil {
		return RunCompletion{}, err
	}
	err = a.acquire(ctx, accepted.RunID, "", a.Owner.Owner.Generation, spec.Resolved.Settings.Concurrency)
	if err != nil {
		return RunCompletion{}, err
	}
	return body(ctx)
}

// Node admits before release; terminal transitions release its capacity atomically.
func (a *storeAdmission) Node(ctx context.Context, accepted AcceptedRun, row store.NodeRow, body func(context.Context) error) error {
	if _, err := admissionSpec(accepted); err != nil {
		return err
	}
	if row.Ref.RunID != accepted.RunID || row.Ref.NodeID == "" || row.Generation < 0 {
		return errors.New("store admission requires the accepted job identity")
	}
	if err := a.acquire(ctx, accepted.RunID, row.Ref.NodeID, row.Generation, row.Execution.Concurrency); err != nil {
		return err
	}
	return body(ctx)
}

func (a *storeAdmission) acquire(ctx context.Context, runID, jobID string, generation int64, groups []runspec.Concurrency) error {
	if err := context.Cause(ctx); err != nil {
		return err
	}
	if a.Store == nil || a.Owner.Kind != store.ActorRunHost || a.Owner.Owner.RunID != runID || a.Owner.Owner.Holder == "" || a.Owner.Owner.Generation < 1 {
		return errors.New("store admission requires the current run owner")
	}
	if len(groups) == 0 {
		return nil
	}
	binding := store.CapacityBinding{RunID: runID, NodeID: jobID, Generation: generation}
	response, err := a.Store.AdmitCapacity(ctx, binding, a.Owner)
	if err != nil {
		return err
	}
	if response.Kind == store.AcquireQueued && a.Queued != nil {
		if err := a.Queued(ctx, controllerwire.NodeRef{RunID: runID, NodeID: jobID}, response); err != nil {
			return err
		}
	}
	tick := time.NewTicker(waiterPoll)
	defer tick.Stop()
	var failingSince time.Time
	for {
		switch response.Kind {
		case store.AcquireGranted, store.AcquireCancellingOthers:
			return nil
		case store.AcquireSkipped:
			return &AdmissionFailure{Outcome: store.NodeSkipped, Err: errors.New("concurrency capacity is full")}
		case store.AcquireFailed:
			if response.FailureReason == store.FailureQueueTimeout {
				return &AdmissionFailure{Outcome: store.NodeFailed, Reason: store.FailureQueueTimeout, Err: errQueueTimeout}
			}
			return &AdmissionFailure{Outcome: store.NodeFailed, Err: errors.New("concurrency capacity is full")}
		case store.AcquireQueued:
		default:
			return errors.New("capacity acquisition returned an unknown decision")
		}
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-tick.C:
		}
		next, err := a.Store.AdmitCapacity(ctx, binding, a.Owner)
		if err != nil {
			if failingSince.IsZero() {
				failingSince = time.Now()
			}
			if store.IsProtocolErr(err) || time.Since(failingSince) >= storeOutageBudget {
				return err
			}
			continue
		}
		failingSince = time.Time{}
		response = next
	}
}

var _ CapacityStore = (*store.Tenant)(nil)
