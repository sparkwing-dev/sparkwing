package launcher

import (
	"context"
	"errors"
	"math"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	runners "github.com/sparkwing-dev/sparkwing/internal/runners/award"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
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
	resources := c.resources(unit)
	if resources == nil {
		return nil, errors.New("Cloud award is missing its committed allocation")
	}
	award := &CloudJobAward{LeaseExpiresAt: unit.LeaseExpiresAt, Binding: binding, Bootstrap: []byte(unit.Capability), Engine: unit.Engine, CPUCores: resources.Cores, Memory: resources.MemoryBytes}
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

func (c *ControllerControl) resources(unit controllerwire.ClaimedUnit) *match.Resources {
	if unit.CloudResources == nil || unit.Runner.Type != "runner" {
		return unit.CloudResources
	}
	resources := *unit.CloudResources
	// Self-hosted work without a pin uses the operator's configured pod budget.
	if resources.Cores == 0 {
		resources.Cores = c.demand.AvailableCores
	}
	if resources.MemoryBytes == 0 {
		resources.MemoryBytes = c.demand.AvailableMemoryBytes
	}
	return &resources
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
	if err := runners.ValidateAward(unit, delivery); err != nil {
		return CloudJobBinding{}, err
	}
	fence := unit.Binding
	binding := CloudJobBinding{UnitID: unit.UnitID, Team: unit.Request.Request.Team, RunID: unit.RunID, NodeID: unit.JobID, Holder: fence.Holder, Generation: fence.Generation, Attempt: fence.Attempt, OwnerDeadline: deadlineValue(unit.OwnerDeadline), AttemptDeadline: deadlineValue(unit.AttemptDeadline), ParentCapabilityExpiry: deadlineValue(unit.ParentCapabilityExpiry)}
	switch unit.Unit {
	case capability.UnitPlan:
		binding.Kind = CloudPlan
	case capability.UnitNode:
		binding.Kind = CloudWork
	case capability.UnitEval:
		binding.Kind, binding.SourceNodeID, binding.SourceGeneration = CloudEval, unit.JobID, unit.SourceGeneration
		binding.Generation, binding.Holder = fence.Expansion.Generation, fence.Expansion.Holder
	default:
		return CloudJobBinding{}, errors.New("controller returned a non-Cloud unit")
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
	byJob, byAward := make(map[string]int), make(map[capability.LeaseRef]int)
	for i, observation := range observations {
		if observation.Binding.UnitID == "" || observation.JobName != jobName(observation.Binding) {
			return nil, errors.New("Cloud observation is missing its persisted unit, Job or node identity")
		}
		if _, ok := byJob[observation.JobName]; ok {
			return nil, errors.New("duplicate Cloud Job observation")
		}
		if observation.NodeUID == "" || observation.ReservationID == "" && (observation.JobNamespace == "" || observation.JobUID == "") || observation.ReservationID != "" && (observation.JobNamespace != "" || observation.JobUID != "") {
			return nil, errors.New("Cloud observation requires its local Job or reservation identity")
		}
		observed := controllerwire.LauncherObservation{Award: observation.Binding.LeaseRef(), Reservation: observation.ReservationID != "", Rejected: observation.Rejected}
		if err := observed.Validate(); err != nil {
			return nil, err
		}
		if _, ok := byAward[observed.Award]; ok {
			return nil, errors.New("duplicate Cloud award observation")
		}
		byJob[observation.JobName], byAward[observed.Award] = i, i
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
	if len(response.Deferred)+len(response.Awards)+len(response.Retired) != len(observations) {
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
			return nil, errAwardMismatch
		}
		states[i].Binding, states[i].Resources, states[i].LeaseExpiresAt = observations[i].Binding, c.resources(unit), unit.LeaseExpiresAt
	}
	for _, fence := range response.Deferred {
		i, ok := byAward[fence]
		if !ok || states[i].Binding.UnitID != "" {
			return nil, errAwardMismatch
		}
		states[i] = CloudJobState{Binding: observations[i].Binding, Deferred: true}
	}
	for _, fence := range response.Retired {
		i, ok := byAward[fence]
		if !ok || states[i].Binding.UnitID != "" {
			return nil, errors.New("controller sync returned unknown or contradictory award retirement")
		}
		states[i] = CloudJobState{Binding: observations[i].Binding, Delete: true}
	}
	return states, nil
}

var _ CloudControl = (*ControllerControl)(nil)
