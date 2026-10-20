package controller

import (
	"context"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	runners "github.com/sparkwing-dev/sparkwing/internal/runners/award"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// LauncherSyncReader rechecks launcher ancestry and resolves current awards.
type LauncherSyncReader interface {
	ReadLauncherSync(context.Context, capability.AuthorityRef, []controllerwire.LauncherObservation, []capability.LeaseRef) (controllerwire.LauncherSyncResponse, error)
}

var _ LauncherSyncReader = storeLauncherSync{}

func (s *EngineController) syncLauncher(ctx context.Context, a capability.Authorized, body controllerwire.LauncherSyncRequest) (controllerwire.LauncherSyncResponse, error) {
	if err := validateLauncherObservations(body.Observed); err != nil {
		return controllerwire.LauncherSyncResponse{}, err
	}
	reader := s.Launcher
	if reader == nil {
		return controllerwire.LauncherSyncResponse{}, missingEndpointPolicy()
	}
	response, err := reader.ReadLauncherSync(ctx, a.Principal.Authority, slices.Clone(body.Observed), slices.Clone(body.Preparing))
	if err != nil {
		return controllerwire.LauncherSyncResponse{}, err
	}
	response.Awards = append([]controllerwire.ClaimedUnit{}, response.Awards...)
	for i := range response.Awards {
		response.Awards[i].Capability = ""
	}
	response.Retired = append([]capability.LeaseRef{}, response.Retired...)
	return response, nil
}

type storeLauncherSync struct {
	store interface {
		ReconcileLauncherAwards(context.Context, capability.AuthorityRef, []capability.LeaseRef, []capability.LeaseRef, []capability.LeaseRef, []capability.LeaseRef) ([]store.LauncherAward, []store.LauncherPreparation, error)
	}
}

func (s storeLauncherSync) ReadLauncherSync(ctx context.Context, ref capability.AuthorityRef, observed []controllerwire.LauncherObservation, preparing []capability.LeaseRef) (controllerwire.LauncherSyncResponse, error) {
	observations := make([]capability.LeaseRef, 0, len(observed))
	var reservations []capability.LeaseRef
	for _, observation := range observed {
		observations = append(observations, observation.Award)
		if observation.Reservation {
			reservations = append(reservations, observation.Award)
		}
	}
	var rejected []capability.LeaseRef
	for _, observation := range observed {
		if observation.Rejected {
			rejected = append(rejected, observation.Award)
		}
	}
	awards, prepared, err := s.store.ReconcileLauncherAwards(ctx, ref, observations, reservations, preparing, rejected)
	if err != nil {
		return controllerwire.LauncherSyncResponse{}, err
	}
	current := make(map[capability.LeaseRef]store.LauncherAward, len(awards))
	for _, award := range awards {
		current[award.Fence] = award
	}
	response := controllerwire.LauncherSyncResponse{Awards: []controllerwire.ClaimedUnit{}, Retired: []capability.LeaseRef{}}
	for _, preparation := range prepared {
		response.Prepared = append(response.Prepared, controllerwire.LauncherPreparation{LeaseRef: preparation.Fence, ExpiresAt: preparation.ExpiresAt})
	}
	for _, observation := range observed {
		award, found := current[observation.Award]
		if found && award.Gone {
			response.Retired = append(response.Retired, observation.Award)
			continue
		}
		if !found || award.Failure != nil {
			response.Deferred = append(response.Deferred, observation.Award)
			continue
		}
		unit := engineReconciledClaim(award.Capability, store.ReconciledNode{Node: award.Node, Claim: &award.Claim.Award}, award.Run)
		unit.RunID, unit.JobID, unit.SourceGeneration = award.Claim.RunID, award.Claim.NodeID, award.Claim.SourceGeneration
		unit.ParentCapabilityExpiry = award.ParentCapabilityExpiry
		unit.Delivery = capability.DeliveryBootstrap
		if award.Claim.Kind != capability.UnitNode {
			unit.Execution = nil
		}
		unit.CloudResources = award.CloudResources
		if err := runners.ValidateAward(*unit, capability.DeliveryBootstrap); err != nil {
			response.Deferred = append(response.Deferred, observation.Award)
			continue
		}
		response.Awards = append(response.Awards, *unit)
	}
	return response, nil
}

func validateLauncherObservations(observed []controllerwire.LauncherObservation) error {
	units := map[capability.LeaseRef]bool{}
	for _, observation := range observed {
		if err := observation.Validate(); err != nil {
			return err
		}
		if units[observation.Award] {
			return invalidEngineBody("launcher observations must name unique awards")
		}
		units[observation.Award] = true
	}
	return nil
}
