package nodehost

import (
	"context"
	"errors"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// NativeAdmission orders logical plan capacity, job capacity and optional compute.
type NativeAdmission struct {
	capacity *storeAdmission
	compute  *LocalAdmission
	required bool
}

// ComposeNativeAdmission binds store-owned concurrency independently of compute.
// Requesters require compute; runners may omit it without losing concurrency.
func ComposeNativeAdmission(capacity CapacityStore, compute *LocalAdmission, required bool) NativeAdmission {
	admission := NativeAdmission{required: required}
	if compute != nil {
		copy := *compute
		admission.compute = &copy
	}
	if capacity != nil {
		admission.capacity = &storeAdmission{Store: capacity}
	}
	return admission
}

type admissionContext struct{}

type computeReservation struct {
	ref       controllerwire.NodeRef
	resources wingwire.HostResources
}

func configureNativeAdmission(owner *OwnerHost, local *LocalAdmission, capacity *storeAdmission, required bool) {
	a := NativeAdmission{capacity: capacity, compute: local, required: required}
	owner.retainRemote = local != nil
	owner.executionAdmission = a
	owner.bindAdmission = func(bound *OwnerHost, actor store.Actor) {
		if capacity != nil {
			copy := *capacity
			copy.Owner = actor
			configureNativeAdmission(bound, local, &copy, required)
		}
	}
	if owner.AdmissionPolicy == nil {
		owner.AdmissionPolicy = func(_ context.Context, _ EvaluationInput, row store.NodeRow, _ StorageBinding) (OwnerLayerResult, error) {
			return OwnerLayerResult{}, a.check(row.Execution.Concurrency)
		}
	}
	owner.Around = func(ctx context.Context, accepted AcceptedRun, body func(context.Context) (RunCompletion, error)) (RunCompletion, error) {
		spec, err := admissionSpec(accepted)
		if err != nil {
			return RunCompletion{}, err
		}
		if err := a.check(spec.Resolved.Settings.Concurrency); err != nil {
			return RunCompletion{}, err
		}
		ctx = context.WithValue(ctx, admissionContext{}, a)
		if capacity != nil {
			return capacity.Run(ctx, accepted, body)
		}
		return body(ctx)
	}
	// safety: readiness reserves no compute: a claimant may already hold the job's capacity.
	owner.Admit = func(ctx context.Context, accepted AcceptedRun, row store.NodeRow, body func(context.Context) error) error {
		if err := a.check(row.Execution.Concurrency); err != nil {
			return err
		}
		if capacity != nil && row.ReadyAt == nil && row.ClaimedBy == "" {
			if err := capacity.plan(ctx, accepted); err != nil {
				return err
			}
		}
		return body(ctx)
	}
}

func hasLocalConcurrency(groups []runspec.Concurrency) bool {
	return slices.ContainsFunc(groups, func(group runspec.Concurrency) bool { return group.Scope == "run" || group.Scope == "box" })
}

func (a NativeAdmission) check(groups []runspec.Concurrency) error {
	if a.required && a.compute == nil {
		return errors.New("local execution requires machine admission")
	}
	if a.capacity == nil && len(groups) != 0 {
		return errors.New("concurrency admission requires a capacity store")
	}
	return nil
}

// safety: logical capacity always precedes compute, including during burst reservation.
func (a NativeAdmission) withNode(ctx context.Context, accepted AcceptedRun, row store.NodeRow, actor store.Actor, body func(context.Context, wingwire.HostResources) error) (err error) {
	defer func() { err = errors.Join(err, context.Cause(ctx)) }()
	if err := context.Cause(ctx); err != nil {
		return err
	}
	spec, err := admissionSpec(accepted)
	if err != nil {
		return err
	}
	groups := append(slices.Clone(spec.Resolved.Settings.Concurrency), row.Execution.Concurrency...)
	if err := a.check(groups); err != nil {
		return err
	}
	// safety: a burst reservation encloses reconciliation and execution of the same job.
	// Reuse it before touching logical admission again, so compute never encloses a wait.
	if held, ok := ctx.Value(computeReservation{}).(computeReservation); ok && held.ref == row.Ref {
		return body(ctx, held.resources)
	}
	work := func(ctx context.Context) error {
		if a.compute == nil {
			return body(ctx, wingwire.HostResources{})
		}
		return a.compute.node(ctx, accepted, row, func(ctx context.Context) error {
			held := ctx.Value(computeReservation{}).(computeReservation)
			return body(ctx, held.resources)
		})
	}
	if a.capacity != nil {
		capacity := *a.capacity
		capacity.Owner = actor
		return capacity.Node(ctx, accepted, row, work)
	}
	return work(ctx)
}

func (a NativeAdmission) claimedNode(ctx context.Context, input NodeInput, body func(context.Context) error) error {
	row := store.NodeRow{Ref: controllerwire.NodeRef{RunID: input.Accepted.RunID, NodeID: input.NodeID}, Generation: input.Generation, Execution: input.Execution}
	return a.withNode(ctx, input.Accepted, row, executorActor(input), func(ctx context.Context, _ wingwire.HostResources) error { return body(ctx) })
}
