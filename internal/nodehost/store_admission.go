package nodehost

import (
	"context"
	"errors"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

const waiterPoll = 100 * time.Millisecond

type admissionWaitKey struct{}

// WithAdmissionWait installs runner inventory accounting for queued admission.
func WithAdmissionWait(ctx context.Context, suspend func() (resume func(context.Context), stop func())) context.Context {
	return context.WithValue(ctx, admissionWaitKey{}, suspend)
}

// AdmissionWait suspends inventory until admission succeeds or the wait ends.
func AdmissionWait(ctx context.Context) (resume func(context.Context), stop func()) {
	if suspend, ok := ctx.Value(admissionWaitKey{}).(func() (func(context.Context), func())); ok {
		return suspend()
	}
	return AwaitSlot(ctx)(), func() {}
}

var errQueueTimeout = errors.New("queue timeout")

// CapacityStore returns durable start decisions for authenticated job awards.
type CapacityStore interface {
	MayStart(context.Context, runstate.CapacityBinding, runstate.Actor) (runstate.AcquireSlotResponse, error)
	RelinquishStart(context.Context, runstate.CapacityBinding, runstate.Actor) error
}

// admissionFailure carries a refusal committed by the store before execution.
type admissionFailure struct {
	Outcome   runstate.NodeState
	Reason    string
	Err       error
	committed bool
}

func (e *admissionFailure) Error() string { return e.Err.Error() }
func (e *admissionFailure) Unwrap() error { return e.Err }

func awaitStart(ctx context.Context, state CapacityStore, binding runstate.CapacityBinding, actor runstate.Actor, queued func(runstate.AcquireSlotResponse) error) error {
	tick := time.NewTimer(waiterPoll)
	defer tick.Stop()
	delay := waiterPoll
	var resume func(context.Context)
	stop := func() {}
	defer func() { stop() }()
	for {
		if err := context.Cause(ctx); err != nil {
			return err
		}
		response, err := state.MayStart(ctx, binding, actor)
		if err != nil {
			return err
		}
		switch response.Kind {
		case runstate.AcquireGranted, runstate.AcquireCancellingOthers:
			if resume != nil {
				resume(ctx)
			}
			return context.Cause(ctx)
		case runstate.AcquireSkipped:
			return &admissionFailure{Outcome: runstate.NodeSkipped, Err: errors.New("concurrency capacity is full"), committed: true}
		case runstate.AcquireFailed:
			failure := errors.New("concurrency capacity is full")
			if response.FailureReason == store.FailureQueueTimeout {
				failure = errQueueTimeout
			}
			return &admissionFailure{Outcome: runstate.NodeFailed, Reason: response.FailureReason, Err: failure, committed: true}
		case runstate.AcquireQueued:
			if resume == nil {
				resume, stop = AdmissionWait(ctx)
			}
			if queued != nil {
				if err := queued(response); err != nil {
					return err
				}
				queued = nil
			}
		default:
			return errors.New("start decision is unknown")
		}
		tick.Reset(delay)
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-tick.C:
		}
		delay = min(2*delay, time.Second)
	}
}

var _ CapacityStore = (*store.Tenant)(nil)
