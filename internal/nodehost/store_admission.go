package nodehost

import (
	"context"
	"errors"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
)

const waiterPoll = 100 * time.Millisecond

var errQueueTimeout = errors.New("queue timeout")

// CapacityStore returns durable start decisions for authenticated job awards.
type CapacityStore interface {
	MayStart(context.Context, store.CapacityBinding, store.Actor) (store.AcquireSlotResponse, error)
	RelinquishStart(context.Context, store.CapacityBinding, store.Actor) error
}

// admissionFailure carries a refusal committed by the store before execution.
type admissionFailure struct {
	Outcome   store.NodeState
	Reason    string
	Err       error
	committed bool
}

func (e *admissionFailure) Error() string { return e.Err.Error() }
func (e *admissionFailure) Unwrap() error { return e.Err }

func awaitStart(ctx context.Context, state CapacityStore, binding store.CapacityBinding, actor store.Actor, queued func(store.AcquireSlotResponse) error) error {
	tick := time.NewTicker(waiterPoll)
	defer tick.Stop()
	for {
		if err := context.Cause(ctx); err != nil {
			return err
		}
		response, err := state.MayStart(ctx, binding, actor)
		if err != nil {
			return err
		}
		switch response.Kind {
		case store.AcquireGranted, store.AcquireCancellingOthers:
			return nil
		case store.AcquireSkipped:
			return &admissionFailure{Outcome: store.NodeSkipped, Err: errors.New("concurrency capacity is full"), committed: true}
		case store.AcquireFailed:
			failure := errors.New("concurrency capacity is full")
			if response.FailureReason == store.FailureQueueTimeout {
				failure = errQueueTimeout
			}
			return &admissionFailure{Outcome: store.NodeFailed, Reason: string(response.FailureReason), Err: failure, committed: true}
		case store.AcquireQueued:
			if queued != nil {
				if err := queued(response); err != nil {
					return err
				}
				queued = nil
			}
		default:
			return errors.New("start decision is unknown")
		}
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-tick.C:
		}
	}
}

var _ CapacityStore = (*store.Tenant)(nil)
