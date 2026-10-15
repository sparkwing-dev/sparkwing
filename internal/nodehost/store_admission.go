package nodehost

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const (
	waiterPoll        = 100 * time.Millisecond
	storeOutageBudget = 5 * time.Minute
)

// CapacityStore supplies tenant-scoped admission and continuous adoption.
type CapacityStore interface {
	AdoptCapacity(context.Context, string, string, store.CapacityBinding, store.Actor) (store.CapacityAdoption, error)
	AcquireConcurrencySlot(context.Context, store.AcquireSlotRequest) (store.AcquireSlotResponse, error)
	ResolveWaiter(context.Context, string, string, string, string, string, string, bool) (store.WaiterResolution, error)
	CancelWaiter(context.Context, string, string, string) (bool, error)
}

// StoreAdmission binds sealed concurrency groups to their live run or job.
type StoreAdmission struct {
	Store  CapacityStore
	Owner  store.Actor
	BoxID  string
	local  bool
	Queued func(context.Context, store.NodeRef, store.AcquireSlotResponse) error
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
func (a *StoreAdmission) Run(ctx context.Context, accepted AcceptedRun, body func(context.Context) (RunCompletion, error)) (RunCompletion, error) {
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
func (a *StoreAdmission) Node(ctx context.Context, accepted AcceptedRun, row store.NodeRow, body func(context.Context) error) error {
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

func (a *StoreAdmission) acquire(ctx context.Context, runID, jobID string, generation int64, groups []runspec.Concurrency) error {
	if err := context.Cause(ctx); err != nil {
		return err
	}
	if a.Store == nil || a.Owner.Kind != store.ActorRunHost || a.Owner.Owner.RunID != runID || a.Owner.Owner.Holder == "" || a.Owner.Owner.Generation < 1 {
		return errors.New("store admission requires the current run owner")
	}
	groups = slices.Clone(groups)
	key := func(group runspec.Concurrency) string {
		return scopedConcurrencyKey(group.Group, group.Scope, runID, a.BoxID)
	}
	slices.SortFunc(groups, func(a, b runspec.Concurrency) int { return strings.Compare(key(a), key(b)) })
	for _, group := range groups {
		if a.local && (group.Scope == "run" || group.Scope == "box") {
			continue
		}
		if group.Group == "" || group.Capacity < 1 || group.Cost < 1 || group.Scope != "global" && group.Scope != "run" && group.Scope != "box" || group.Scope == "box" && a.BoxID == "" {
			return errors.New("store admission requires sealed concurrency settings")
		}
		binding := store.CapacityBinding{RunID: runID, NodeID: jobID, Generation: generation}
		if err := a.group(ctx, key(group), binding, group); err != nil {
			return err
		}
	}
	return context.Cause(ctx)
}

func (a *StoreAdmission) group(ctx context.Context, key string, binding store.CapacityBinding, group runspec.Concurrency) error {
	id := binding.RunID + "/" + binding.NodeID
	if binding.NodeID == "" {
		id += "-"
	}
	adopted, err := a.Store.AdoptCapacity(ctx, key, id, binding, a.Owner)
	if errors.Is(err, store.ErrNotFound) {
		adopted.Kind, err = store.AcquireAgain, nil
	}
	if err != nil {
		return err
	}
	switch adopted.Kind {
	case store.AdoptHolder:
		return checkCapacityHolder(adopted.Holder, key, id, binding, group.Cost)
	case store.AdoptWaiter:
		if adopted.Waiter == nil || adopted.Waiter.Key != key || adopted.Waiter.ID != id || adopted.Waiter.Binding != binding {
			return errors.New("capacity adoption changed its waiter binding")
		}
		return a.wait(ctx, key, id, binding, group, adopted.Waiter.ArrivedAt)
	case store.AcquireAgain:
	default:
		return errors.New("capacity adoption returned an unknown decision")
	}
	response, err := a.Store.AcquireConcurrencySlot(ctx, store.AcquireSlotRequest{
		Key: key, HolderID: id, RunID: binding.RunID, NodeID: binding.NodeID,
		Capacity: group.Capacity, Cost: group.Cost, Policy: group.OnLimit,
		CancelTimeout: time.Duration(value(group.CancelTimeoutMS)) * time.Millisecond,
		Lease:         store.DefaultConcurrencyLease,
	})
	if err != nil {
		return err
	}
	switch response.Kind {
	case store.AcquireGranted, store.AcquireCancellingOthers:
		return a.confirm(ctx, key, id, binding, group.Cost)
	case store.AcquireQueued:
		if a.Queued != nil {
			if err := a.Queued(ctx, store.NodeRef{RunID: binding.RunID, NodeID: binding.NodeID}, response); err != nil {
				return err
			}
		}
		adopted, err := a.Store.AdoptCapacity(ctx, key, id, binding, a.Owner)
		if err != nil {
			return err
		}
		if adopted.Kind == store.AdoptHolder {
			return checkCapacityHolder(adopted.Holder, key, id, binding, group.Cost)
		}
		if adopted.Kind != store.AdoptWaiter || adopted.Waiter == nil || adopted.Waiter.Key != key || adopted.Waiter.ID != id || adopted.Waiter.Binding != binding {
			return errors.New("new capacity waiter lost its continuous binding")
		}
		return a.wait(ctx, key, id, binding, group, adopted.Waiter.ArrivedAt)
	case store.AcquireSkipped:
		return &AdmissionFailure{Outcome: store.NodeSkipped, Err: fmt.Errorf("concurrency group %q is full", key)}
	case store.AcquireFailed:
		return &AdmissionFailure{Outcome: store.NodeFailed, Err: fmt.Errorf("concurrency group %q is full", key)}
	default:
		return errors.New("capacity acquisition returned an unknown decision")
	}
}

func (a *StoreAdmission) confirm(ctx context.Context, key, id string, binding store.CapacityBinding, cost int) error {
	adopted, err := a.Store.AdoptCapacity(ctx, key, id, binding, a.Owner)
	if err != nil {
		return err
	}
	if adopted.Kind != store.AdoptHolder {
		return errors.New("awarded capacity is no longer continuously held")
	}
	return checkCapacityHolder(adopted.Holder, key, id, binding, cost)
}

func checkCapacityHolder(holder *store.CapacityHolder, key, id string, binding store.CapacityBinding, cost int) error {
	if holder == nil || holder.Key != key || holder.ID != id || holder.Binding != binding || holder.Cost != cost || holder.Superseded {
		return errors.New("capacity adoption changed its holder binding")
	}
	return nil
}

func (a *StoreAdmission) wait(ctx context.Context, key, id string, binding store.CapacityBinding, group runspec.Concurrency, arrival time.Time) error {
	if arrival.IsZero() {
		return errors.New("capacity waiter has no persisted arrival time")
	}
	var timeout <-chan time.Time
	if budget := value(group.QueueTimeoutMS); budget > 0 {
		timer := time.NewTimer(time.Until(arrival.Add(time.Duration(budget) * time.Millisecond)))
		defer timer.Stop()
		timeout = timer.C
	}
	tick := time.NewTicker(waiterPoll)
	defer tick.Stop()
	var failingSince time.Time
	for {
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-timeout:
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), limits.HeartbeatTimeout)
			_, err := a.Store.CancelWaiter(cleanup, key, binding.RunID, binding.NodeID)
			cancel()
			return errors.Join(&AdmissionFailure{Outcome: store.NodeFailed, Reason: store.FailureQueueTimeout, Err: errQueueTimeout}, err)
		case <-tick.C:
		}
		response, err := a.Store.ResolveWaiter(ctx, key, binding.RunID, binding.NodeID, "", "", "", false)
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
		switch response.Status {
		case store.WaiterStillWaiting:
		case store.WaiterPromoted:
			if response.HolderID != id {
				return errors.New("promoted capacity changed its holder identity")
			}
			return a.confirm(ctx, key, id, binding, group.Cost)
		case store.WaiterCancelled:
			return &AdmissionFailure{Outcome: store.NodeSuperseded, Err: &lease.CancelError{Reason: "capacity waiter was superseded"}}
		default:
			return errors.New("capacity waiter returned an unknown decision")
		}
	}
}

var _ CapacityStore = (*store.Tenant)(nil)
