package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
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
	Resources() wingwire.HostResources
	Release() error
	Watch(func(wingwire.Evicted), func(wingwire.Cancel)) error
}

// LocalAdmission holds CPU and memory under sealed settings.
type LocalAdmission struct {
	Daemon     Admitter
	Controller string
	Queued     func(context.Context, controllerwire.NodeRef, wingwire.Queued)
}

type localRunLease struct {
	runID, token string
	resources    bool
}

// Run keeps the parent admission lease until scheduling and every child have joined.
func (a *LocalAdmission) Run(ctx context.Context, accepted AcceptedRun, body func(context.Context) (RunCompletion, error)) (completion RunCompletion, err error) {
	spec, err := admissionSpec(accepted)
	if err != nil {
		return completion, err
	}
	options := spec.Resolved.Settings.Options
	resources := localResources(options.Resources.Value)
	if spec.Request.Request.Burst != nil {
		resources = wingwire.HostResources{}
	}
	parent := localRunLease{runID: accepted.RunID, resources: resources.Cores > 0 || resources.MemoryBytes > 0}
	request := wingwire.AdmissionRequest{
		RunID: wingwire.HolderRef(a.holderRunID(accepted.RunID)), Pipeline: spec.Request.Request.Pipeline,
		DisplayRunID:   accepted.RunID,
		SemaphoresOnly: !parent.resources, Resources: resources,
		Priority: options.Priority.Value, Class: options.AdmissionClass.Value,
	}
	err = a.hold(ctx, controllerwire.NodeRef{RunID: accepted.RunID}, request, func(ctx context.Context, held Lease) error {
		parent.token = held.Token()
		completion, err = body(context.WithValue(ctx, localRunLease{}, parent))
		return err
	})
	return completion, err
}

// Node admits an unclaimed local job before release and holds its lease through cleanup.
func (a *LocalAdmission) Node(ctx context.Context, accepted AcceptedRun, row store.NodeRow, body func(context.Context) error) error {
	return a.node(ctx, accepted, row, true, body)
}

// ClaimedNode holds machine capacity for one independently awarded job.
func (a *LocalAdmission) ClaimedNode(ctx context.Context, accepted AcceptedRun, row store.NodeRow, body func(context.Context) error) error {
	spec, err := admissionSpec(accepted)
	if err != nil {
		return err
	}
	if row.Ref.RunID != accepted.RunID || row.Ref.NodeID == "" {
		return errors.New("claimed job admission requires its accepted run and job")
	}
	options := spec.Resolved.Settings.Options
	request := wingwire.AdmissionRequest{
		RunID:        wingwire.NodeHostParticipant(a.holderRunID(accepted.RunID), row.Ref.NodeID),
		DisplayRunID: accepted.RunID + "/" + row.Ref.NodeID, Pipeline: spec.Request.Request.Pipeline,
		Resources: localResources(row.Execution.Resources.Value),
		Priority:  options.Priority.Value, Class: options.AdmissionClass.Value,
	}
	return a.hold(ctx, row.Ref, request, func(ctx context.Context, _ Lease) error { return body(ctx) })
}

func (a *LocalAdmission) node(ctx context.Context, accepted AcceptedRun, row store.NodeRow, local bool, body func(context.Context) error) error {
	if err := context.Cause(ctx); err != nil {
		return err
	}
	spec, err := admissionSpec(accepted)
	if err != nil {
		return err
	}
	parent, ok := ctx.Value(localRunLease{}).(localRunLease)
	if !ok || parent.runID != accepted.RunID || parent.token == "" || row.Ref.RunID != accepted.RunID || row.Ref.NodeID == "" {
		return errors.New("local job admission requires its live parent run lease")
	}
	if parent.resources || !local {
		return body(ctx)
	}
	options := spec.Resolved.Settings.Options
	request := wingwire.AdmissionRequest{
		OwnerRunID: a.holderRunID(accepted.RunID), OwnerLeaseToken: parent.token, SubLease: true,
		DisplayRunID: accepted.RunID + "/" + row.Ref.NodeID, Pipeline: spec.Request.Request.Pipeline,
		Resources: localResources(row.Execution.Resources.Value), Priority: options.Priority.Value,
		Class: options.AdmissionClass.Value,
	}
	request.RunID = wingwire.NodeHostParticipant(a.holderRunID(accepted.RunID), row.Ref.NodeID)
	return a.hold(ctx, row.Ref, request, func(ctx context.Context, _ Lease) error { return body(ctx) })
}

func admissionSpec(accepted AcceptedRun) (runspec.Spec, error) {
	var spec runspec.Spec
	if accepted.RunID == "" || accepted.SpecHash == "" || accepted.PlanHash == "" || json.Unmarshal(accepted.Spec, &spec) != nil || string(spec.Hash) != accepted.SpecHash || string(spec.PlanHash) != accepted.PlanHash {
		return spec, errors.New("local admission requires the accepted run seal")
	}
	return spec, nil
}

func localResources(resources runspec.Resources) wingwire.HostResources {
	return wingwire.HostResources{Cores: resources.Cores.Value, MemoryBytes: resources.MemoryBytes.Value}
}

func (a *LocalAdmission) holderRunID(run string) string {
	if a.Controller == "" {
		return run
	}
	return string(wingwire.RemoteRunID(a.Controller, run))
}

func (a *LocalAdmission) hold(parent context.Context, ref controllerwire.NodeRef, request wingwire.AdmissionRequest, body func(context.Context, Lease) error) (err error) {
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
		return localAdmissionFailure(errors.Join(context.Cause(ctx), err))
	}
	joined := make(chan error, 1)
	go func() {
		err := held.Watch(func(evicted wingwire.Evicted) {
			cancel(&AdmissionFailure{Outcome: store.NodeSuperseded, Err: &lease.CancelError{Reason: fmt.Sprintf("admission on %q superseded by %s", evicted.Key, evicted.SupersededBy)}})
		}, func(stopped wingwire.Cancel) {
			cancel(&lease.CancelError{Reason: stopped.Reason})
		})
		if err != nil {
			if errors.Is(err, wingdclient.ErrReattachRejected) {
				cancel(&AdmissionFailure{Outcome: store.NodeSuperseded, Err: &lease.CancelError{Reason: "local admission lease lost"}})
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
			return errors.Join(&AdmissionFailure{Outcome: store.NodeSkipped, Err: err}, waitFailure(err, refused))
		case wingwire.PolicyFail:
			return errors.Join(&AdmissionFailure{Outcome: store.NodeFailed, Err: err}, waitFailure(err, refused))
		}
	}
	return err
}
