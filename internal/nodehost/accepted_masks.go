package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func (r *MaskRegistry) initialize(request runspec.SealedRequest, resolved *runspec.Resolved, plan *nodeproto.PlanDoc, nodes []store.NodeRow) error {
	if r == nil {
		return nil
	}
	inputs := runspec.Resolved{Args: request.Request.RequestedArgs}
	if resolved != nil {
		inputs = *resolved
	}
	projection, err := runspec.BuildSecretRegistration(request.Request, inputs)
	if err != nil {
		return err
	}
	for _, value := range projection.Values {
		if err := r.Register(value); err != nil {
			return err
		}
	}
	if plan == nil {
		return nil
	}
	namespace := *plan
	namespace.Jobs = append([]nodeproto.JobSpec{}, plan.Jobs...)
	for _, row := range nodes {
		var job nodeproto.JobSpec
		if len(row.Spec) == 0 || json.Unmarshal(row.Spec, &job) != nil || runspec.NodeID(job.ID) != row.Ref.NodeID {
			return errors.New("persisted mask namespace has an invalid job")
		}
		namespace.Jobs = append(namespace.Jobs, job)
	}
	return r.Accept(namespace, func() error { return nil })
}

func (r *MaskRegistry) initializeSeal(accepted AcceptedRun) error {
	if r == nil {
		return nil
	}
	var spec runspec.Spec
	if json.Unmarshal(accepted.Spec, &spec) != nil {
		return errors.New("mask initialization requires an accepted seal")
	}
	projection, err := runspec.BuildSecretRegistration(spec.Request.Request, spec.Resolved)
	if err != nil {
		return err
	}
	if !slices.Equal(projection.Values, spec.Resolved.SecretRegistration.Values) {
		return errors.New("accepted masking projection differs from its sealed inputs")
	}
	return r.initialize(spec.Request, &spec.Resolved, nil, nil)
}

func (r *MaskRegistry) initializeAccepted(ctx context.Context, state EvaluationStore, accepted AcceptedRun) error {
	if r == nil {
		return nil
	}
	if err := r.initializeSeal(accepted); err != nil {
		return err
	}
	snapshot, err := state.ReadSnapshot(ctx, accepted.RunID)
	if err != nil {
		return err
	}
	persisted, err := ownerAccepted(snapshot.Run)
	if err != nil {
		return err
	}
	persistedHash, err := runspec.CanonicalHash(persisted)
	if err != nil {
		return err
	}
	acceptedHash, err := runspec.CanonicalHash(accepted)
	if err != nil {
		return err
	}
	if persistedHash != acceptedHash {
		return errors.New("mask initialization differs from persisted acceptance")
	}
	return r.initializeSnapshot(snapshot, persisted)
}

func (r *MaskRegistry) initializeSnapshot(snapshot store.RunSnapshot, accepted AcceptedRun) error {
	if r == nil {
		return nil
	}
	if err := r.initializeSeal(accepted); err != nil {
		return err
	}
	for _, expansion := range snapshot.Expansions {
		for _, value := range expansion.Registration.Values {
			if err := r.Register(value); err != nil {
				return err
			}
		}
	}
	return r.initialize(snapshot.Run.Spec.Request, &snapshot.Run.Spec.Resolved, &accepted.Plan, snapshot.Nodes)
}
