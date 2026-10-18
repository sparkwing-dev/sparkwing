package launcher

import (
	"context"
	"errors"
	"math"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/internal/source"
)

// LauncherController is the shared wire client's claim and reconciliation boundary.
// Its transport must select EngineOptions.Launcher and retain the service credential.
type LauncherController interface {
	Claim(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error)
	SyncLauncher(context.Context, controllerwire.LauncherSyncRequest) (controllerwire.LauncherSyncResponse, error)
}

// ControllerControl projects controller awards without running source or pipeline code.
type ControllerControl struct {
	client LauncherController
	demand controllerwire.ClaimRequest
}

// NewControllerControl captures only observations; the controller supplies each award's identity.
func NewControllerControl(client LauncherController, demand controllerwire.ClaimRequest) (*ControllerControl, error) {
	if client == nil || demand.RunnerVersion == "" || len(demand.Kinds) == 0 || demand.AvailableCores < 0 || math.IsNaN(demand.AvailableCores) || math.IsInf(demand.AvailableCores, 0) || demand.AvailableMemoryBytes < 0 {
		return nil, errors.New("launcher controller requires a client, runner version, unit kinds and finite nonnegative capacity")
	}
	seen := make(map[capability.UnitKind]bool)
	for _, kind := range demand.Kinds {
		if seen[kind] || (kind != capability.UnitPlan && kind != capability.UnitEval && kind != capability.UnitNode) {
			return nil, errors.New("launcher claims require unique plan, eval or node kinds")
		}
		seen[kind] = true
	}
	demand.Kinds, demand.Labels = slices.Clone(demand.Kinds), slices.Clone(demand.Labels)
	return &ControllerControl{client: client, demand: demand}, nil
}

// Claim delivers a bootstrap once; uncertain transport delivery recovers through lease expiry.
func (c *ControllerControl) Claim(ctx context.Context) (*CloudJobAward, error) {
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	demand := c.demand
	demand.Kinds, demand.Labels = slices.Clone(demand.Kinds), slices.Clone(demand.Labels)
	response, err := c.client.Claim(ctx, demand)
	if err != nil || response.Claim == nil {
		return nil, err
	}
	unit := *response.Claim
	binding, err := cloudBinding(unit, capability.DeliveryBootstrap)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(c.demand.Kinds, unit.Unit) || unit.Capability == "" {
		return nil, errors.New("controller returned an unexpected launcher award")
	}
	if unit.CloudResources == nil {
		return nil, errors.New("Cloud award is missing its committed allocation")
	}
	award := &CloudJobAward{Binding: binding, Bootstrap: []byte(unit.Capability), Engine: unit.Engine, CPUCores: unit.CloudResources.Cores, Memory: unit.CloudResources.MemoryBytes}
	if err := validateAward(*award); err != nil {
		return nil, err
	}
	award.Source, err = cloudSource(unit)
	if err != nil {
		return nil, err
	}
	captured, err := captureAward(*award)
	if err != nil {
		return nil, err
	}
	return &captured, nil
}

func cloudSource(unit controllerwire.ClaimedUnit) (source.Spec, error) {
	if unit.Unit == capability.UnitPlan {
		return source.ForRequest(unit.Request)
	}
	if unit.Accepted == nil || unit.Engine == nil || *unit.Engine != unit.Accepted.Spec.Resolved.Engine {
		return source.Spec{}, errors.New("Cloud execution award does not match its accepted engine and run")
	}
	return source.ForExecution(unit.Accepted.Spec)
}

func cloudBinding(unit controllerwire.ClaimedUnit, delivery capability.DeliveryPurpose) (CloudJobBinding, error) {
	if err := runners.ValidateAwardIdentity(unit); err != nil {
		return CloudJobBinding{}, err
	}
	fence := unit.Binding
	if unit.Delivery != delivery || unit.UnitID == "" || fence.Generation < 0 || unit.Unit != capability.UnitEval && fence.Generation == 0 || fence.Attempt < 0 {
		return CloudJobBinding{}, errors.New("controller returned an invalid Cloud award binding")
	}
	binding := CloudJobBinding{UnitID: unit.UnitID, Team: unit.Request.Request.Team, RunID: unit.RunID, NodeID: unit.JobID, Holder: fence.Holder, Generation: fence.Generation, Attempt: fence.Attempt, OwnerDeadline: deadlineValue(unit.OwnerDeadline), AttemptDeadline: deadlineValue(unit.AttemptDeadline), ParentCapabilityExpiry: deadlineValue(unit.ParentCapabilityExpiry)}
	switch unit.Unit {
	case capability.UnitPlan:
		if unit.Purpose != capability.PurposePlan || fence.Kind != capability.BindingRun || unit.JobID != "" || fence.NodeID != "" || unit.SourceGeneration != 0 {
			return CloudJobBinding{}, errors.New("controller returned an invalid Cloud plan binding")
		}
		binding.Kind = CloudPlan
	case capability.UnitEval, capability.UnitNode:
		if fence.Kind != capability.BindingNode || unit.JobID == "" || fence.NodeID != unit.JobID {
			return CloudJobBinding{}, errors.New("controller returned an invalid Cloud job binding")
		}
		if unit.Unit == capability.UnitEval {
			if unit.Purpose != capability.PurposeEval || unit.SourceGeneration < 0 || unit.SourceGeneration != fence.Generation {
				return CloudJobBinding{}, errors.New("controller returned an invalid Cloud eval source")
			}
			lease := fence.Expansion
			if lease == nil || lease.Kind != capability.UnitEval || lease.ID != unit.UnitID || lease.Generation <= 0 {
				return CloudJobBinding{}, errors.New("controller returned an invalid Cloud eval lease")
			}
			// safety: the top-level generation fences the terminal source, not the live eval lease.
			binding.Generation, binding.Holder = lease.Generation, lease.Holder
			binding.Kind, binding.SourceNodeID, binding.SourceGeneration = CloudEval, unit.JobID, unit.SourceGeneration
		} else {
			if unit.Purpose != capability.PurposeWork || unit.SourceGeneration != 0 {
				return CloudJobBinding{}, errors.New("controller returned an invalid Cloud work binding")
			}
			binding.Kind = CloudWork
		}
	default:
		return CloudJobBinding{}, errors.New("controller returned a non-Cloud unit")
	}
	if binding.Holder == "" {
		return CloudJobBinding{}, errors.New("controller returned a Cloud award without its claim holder")
	}
	return binding, nil
}

func deadlineValue(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return *value
}

// Sync reconstructs decisions from persisted identity, without recovering or minting a bootstrap.
func (c *ControllerControl) Sync(ctx context.Context, observations []CloudJobObservation) ([]CloudJobState, error) {
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	request := controllerwire.LauncherSyncRequest{}
	jobs := make([]controllerwire.LauncherJobRef, len(observations))
	byJob, byAward := make(map[string]int), make(map[capability.LeaseRef]int)
	for i, observation := range observations {
		if observation.Binding.UnitID == "" || observation.JobName != jobName(observation.Binding) {
			return nil, errors.New("Cloud observation is missing its persisted unit, Job or node identity")
		}
		if _, ok := byJob[observation.JobName]; ok {
			return nil, errors.New("duplicate Cloud Job observation")
		}
		observed := controllerwire.LauncherObservation{
			Award:         observation.Binding.LeaseRef(),
			Job:           controllerwire.LauncherJobRef{Namespace: observation.JobNamespace, Name: observation.JobName, UID: observation.JobUID},
			NodeUID:       observation.NodeUID,
			ReservationID: observation.ReservationID,
			Rejected:      observation.Rejected,
		}
		if observation.ReservationID != "" {
			if observation.JobNamespace != "" || observation.JobUID != "" {
				return nil, errors.New("reservation observation cannot carry a Job instance")
			}
			observed.Job = controllerwire.LauncherJobRef{}
		}
		if err := observed.Validate(); err != nil {
			return nil, err
		}
		if _, ok := byAward[observed.Award]; ok {
			return nil, errors.New("duplicate Cloud award observation")
		}
		byJob[observation.JobName], byAward[observed.Award] = i, i
		jobs[i] = observed.Job
		request.Observed = append(request.Observed, observed)
		if !observation.Unheld {
			request.Preparing = append(request.Preparing, observed.Award)
		}
	}
	if len(observations) == 0 {
		return nil, nil
	}
	response, err := c.client.SyncLauncher(ctx, request)
	if err != nil {
		return nil, err
	}
	if len(response.Awards)+len(response.Remove)+len(response.RemoveReservations) != len(observations) {
		return nil, errors.New("controller sync omitted or added Cloud Job decisions")
	}
	states := make([]CloudJobState, len(observations))
	for _, unit := range response.Awards {
		if unit.Capability != "" {
			return nil, errors.New("controller sync unexpectedly returned credential material")
		}
		binding, err := cloudBinding(unit, capability.DeliveryBootstrap)
		if err != nil {
			return nil, err
		}
		fence := binding.LeaseRef()
		i, ok := byAward[fence]
		if !ok || states[i].Binding.UnitID != "" || !sameBinding(binding, observations[i].Binding) {
			return nil, errors.New("controller sync returned duplicate or changed Cloud bindings")
		}
		states[i].Binding, states[i].Resources = observations[i].Binding, unit.CloudResources
	}
	for _, job := range response.Remove {
		i, ok := byJob[job.Name]
		if !ok || observations[i].ReservationID != "" || states[i].Binding.UnitID != "" || job != jobs[i] {
			return nil, errors.New("controller sync returned unknown or contradictory Cloud cleanup")
		}
		states[i] = CloudJobState{Binding: observations[i].Binding, Delete: true}
	}
	for _, reservation := range response.RemoveReservations {
		i, ok := byAward[reservation.Award]
		if !ok || states[i].Binding.UnitID != "" || observations[i].ReservationID == "" || reservation.NodeUID != observations[i].NodeUID || reservation.ReservationID != observations[i].ReservationID {
			return nil, errors.New("controller sync returned unknown or contradictory reservation cleanup")
		}
		states[i] = CloudJobState{Binding: observations[i].Binding, Delete: true}
	}
	return states, nil
}

var _ CloudControl = (*ControllerControl)(nil)
