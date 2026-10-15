package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestOwnerHostPreparationFailureFinishesRunAndUnit(t *testing.T) {
	for _, stage := range []string{"acquire", "prepare", "plan"} {
		t.Run(stage, func(t *testing.T) {
			f, h, in := newOwnerFixture(t, false)
			failure := errors.New("private source credential")
			switch stage {
			case "acquire":
				h.Acquire = func(context.Context, RunInput) (*source.Entry, error) { return nil, failure }
			case "prepare":
				h.Prepare = func(context.Context, RunInput, *source.Entry) (OwnerPlanning, error) { return OwnerPlanning{}, failure }
			case "plan":
				prepare := h.Prepare
				h.Prepare = func(ctx context.Context, in RunInput, entry *source.Entry) (OwnerPlanning, error) {
					out, err := prepare(ctx, in, entry)
					out.Functions.Plan = func(context.Context) (json.RawMessage, error) { return nil, failure }
					return out, err
				}
			}
			_, err := h.Run(t.Context(), in)
			if !errors.Is(err, failure) {
				t.Fatalf("error = %v", err)
			}
			if f.finished.Unit != in.Owner.Owner || f.finished.Transition.To != store.RunFailed {
				t.Fatalf("unfinished preparation failure: %+v", f.finished)
			}
			if f.finished.Transition.MaskedError == failure.Error() {
				t.Fatal("persisted unmasked setup error")
			}
			if f.accepted.RunID != "" {
				t.Fatal("accepted a failed preparation")
			}
		})
	}
}

func TestOwnerHostPreparationFinishRefusalIsLoud(t *testing.T) {
	f, h, in := newOwnerFixture(t, false)
	failure, refused := errors.New("source unavailable"), errors.New("finish refused")
	f.finishErr = refused
	h.Acquire = func(context.Context, RunInput) (*source.Entry, error) { return nil, failure }
	_, err := h.Run(t.Context(), in)
	if !errors.Is(err, failure) || !errors.Is(err, refused) {
		t.Fatalf("error = %v", err)
	}
}

func TestOwnerHostLostAuthorityDoesNotFinishPreparation(t *testing.T) {
	for _, cause := range []error{lease.ErrLeaseLost, lease.ErrSilent, context.Canceled} {
		f, h, in := newOwnerFixture(t, false)
		ctx, cancel := context.WithCancelCause(t.Context())
		h.Acquire = func(context.Context, RunInput) (*source.Entry, error) { cancel(cause); return nil, cause }
		_, err := h.Run(ctx, in)
		if !errors.Is(err, cause) {
			t.Fatalf("error = %v", err)
		}
		if f.finished.Transition.RunID != "" {
			t.Fatalf("finished after authority ended: %+v", f.finished)
		}
	}
}

func TestOwnerHostCancelledPreparationFinishesWithCancellation(t *testing.T) {
	f, h, in := newOwnerFixture(t, false)
	ctx, cancel := context.WithCancelCause(t.Context())
	h.Acquire = func(context.Context, RunInput) (*source.Entry, error) {
		cancel(&lease.CancelError{Reason: "cancel_requested"})
		return nil, context.Cause(ctx)
	}
	_, err := h.Run(ctx, in)
	var cancelled *lease.CancelError
	if !errors.As(err, &cancelled) {
		t.Fatalf("error = %v", err)
	}
	if f.finished.Transition.To != store.RunCancelled || f.finished.Transition.Reason != string(apierr.ReasonCancelled) {
		t.Fatalf("finish = %+v", f.finished)
	}
}

func TestOwnerHostResumeAcquisitionFailureLeavesAcceptedWorkRecoverable(t *testing.T) {
	f, h, in := newOwnerFixture(t, true)
	failure := errors.New("source unavailable")
	h.Acquire = func(context.Context, RunInput) (*source.Entry, error) { return nil, failure }
	_, err := h.Run(t.Context(), in)
	if !errors.Is(err, failure) || f.finished.Transition.RunID != "" {
		t.Fatalf("error = %v finish = %+v", err, f.finished)
	}
}

func TestOwnerHostPolicyFailureCannotHideBehindTerminalNode(t *testing.T) {
	f, h, in := newOwnerFixture(t, true)
	f.snapshot.Nodes = []store.NodeRow{{Ref: store.NodeRef{RunID: "run", NodeID: "job"}, Status: store.StatusPending}}
	failure := errors.New("policy write refused")
	h.Decide = func(s store.ScheduleSnapshot) store.ScheduleDecision {
		if s.Nodes[0].Outcome != "" {
			return store.ScheduleDecision{Outcome: "success"}
		}
		return store.ScheduleDecision{Nodes: []store.NodeDecision{{NodeID: "job", Kind: store.DecisionReady}}}
	}
	h.Layer = func(ctx context.Context, _ LayerSpec, _ AcceptedRun, row store.NodeRow) (OwnerLayerResult, error) {
		_, err := f.TransitionNode(ctx, store.NodeTransition{Ref: row.Ref, To: store.NodeSkipped})
		return OwnerLayerResult{}, errors.Join(failure, err)
	}
	_, err := h.Run(t.Context(), in)
	if !errors.Is(err, failure) || f.finished.Transition.RunID != "" {
		t.Fatalf("error = %v finish = %+v", err, f.finished)
	}
}
