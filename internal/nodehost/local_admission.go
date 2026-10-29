package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// Admitter acquires machine capacity under the local controller.
type Admitter interface {
	Acquire(context.Context, wingwire.AdmissionRequest, func(wingwire.Queued)) (Lease, error)
}

// Lease holds machine capacity until Release and joins ownership watching.
type Lease interface {
	Token() string
	Release() error
	Watch(func(wingwire.Evicted), func(wingwire.Cancel)) error
}

// LocalAdmission holds machine capacity under sealed settings.
type LocalAdmission struct {
	Daemon     Admitter
	Controller string
	Queued     func(context.Context, runspec.NodeRef, wingwire.Queued)
}

var errComputeBusy = errors.New("machine compute is busy")

func (a *LocalAdmission) node(ctx context.Context, accepted AcceptedRun, row runstate.NodeRow, body func(context.Context) error) error {
	return a.machine(ctx, accepted, row, false, body)
}

func (a *LocalAdmission) tryNode(ctx context.Context, accepted AcceptedRun, row runstate.NodeRow, body func(context.Context) error) error {
	return a.machine(ctx, accepted, row, true, body)
}

func (a *LocalAdmission) machine(ctx context.Context, accepted AcceptedRun, row runstate.NodeRow, nonblocking bool, body func(context.Context) error) error {
	spec, err := admissionSpec(accepted)
	if err != nil {
		return err
	}
	if row.Ref.RunID != runspec.RunID(accepted.RunID) || row.Ref.NodeID == "" {
		return errors.New("claimed job admission requires its accepted run and job")
	}
	options := spec.Resolved.Settings.Options
	request := wingwire.AdmissionRequest{
		RunID:        wingwire.NodeHostParticipant(a.holderRunID(accepted.RunID), string(row.Ref.NodeID)),
		DisplayRunID: accepted.RunID + "/" + string(row.Ref.NodeID), Pipeline: spec.Request.Request.Pipeline,
		Priority: options.Priority.Value, Class: options.AdmissionClass.Value, NonBlocking: nonblocking,
	}
	return a.hold(ctx, row.Ref, request, func(ctx context.Context, _ Lease) error { return body(ctx) })
}

func admissionSpec(accepted AcceptedRun) (runspec.Spec, error) {
	var spec runspec.Spec
	if accepted.RunID == "" || accepted.SpecHash == "" || accepted.PlanHash == "" || json.Unmarshal(accepted.Spec, &spec) != nil || string(spec.Hash) != accepted.SpecHash || string(spec.PlanHash) != accepted.PlanHash {
		return spec, errors.New("local admission requires the accepted run seal")
	}
	return spec, nil
}

func (a *LocalAdmission) holderRunID(run string) string {
	if a.Controller == "" {
		return run
	}
	return string(wingwire.RemoteRunID(a.Controller, run))
}

func (a *LocalAdmission) hold(parent context.Context, ref runspec.NodeRef, request wingwire.AdmissionRequest, body func(context.Context, Lease) error) (err error) {
	if err := context.Cause(parent); err != nil {
		return err
	}
	if a.Daemon == nil {
		return errors.New("local admission requires the controller admission client")
	}
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)
	held, err := a.Daemon.Acquire(ctx, request, func(queued wingwire.Queued) {
		if a.Queued != nil {
			a.Queued(ctx, ref, queued)
		}
	})
	if held == nil {
		if err == nil {
			err = errors.New("local admission returned no lease")
		}
		if request.NonBlocking {
			var refused *wingdclient.AdmissionError
			if errors.As(err, &refused) && refused.Key == "capacity" {
				return errComputeBusy
			}
		}
		return localAdmissionFailure(errors.Join(context.Cause(ctx), err))
	}
	joined := make(chan error, 1)
	go func() {
		err := held.Watch(func(evicted wingwire.Evicted) {
			cancel(&admissionFailure{Outcome: runstate.NodeSuperseded, Err: &lease.CancelError{Reason: fmt.Sprintf("admission on %q superseded by %s", evicted.Key, evicted.SupersededBy)}})
		}, func(stopped wingwire.Cancel) {
			cancel(&lease.CancelError{Reason: stopped.Reason})
		})
		if err != nil {
			if errors.Is(err, wingdclient.ErrReattachRejected) {
				cancel(&admissionFailure{Outcome: runstate.NodeSuperseded, Err: &lease.CancelError{Reason: "local admission lease lost"}})
			} else {
				cancel(err)
			}
		}
		joined <- err
	}()
	defer func() {
		releaseErr := held.Release()
		watchErr := <-joined
		err = errors.Join(err, localAdmissionFailure(context.Cause(ctx)), releaseErr, watchErr)
	}()
	if err == nil && context.Cause(ctx) == nil {
		err = body(ctx, held)
	}
	return err
}

func localAdmissionFailure(err error) error {
	var refused *wingdclient.AdmissionError
	if errors.As(err, &refused) {
		switch refused.Policy {
		case wingwire.PolicySkip:
			return errors.Join(&admissionFailure{Outcome: runstate.NodeSkipped, Err: err}, waitFailure(err, refused))
		case wingwire.PolicyFail:
			return errors.Join(&admissionFailure{Outcome: runstate.NodeFailed, Err: err}, waitFailure(err, refused))
		}
	}
	return err
}
