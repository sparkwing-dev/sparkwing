package controller

import (
	"context"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// LauncherSyncReader rechecks launcher ancestry and resolves current awards in one transaction.
type LauncherSyncReader interface {
	ReadLauncherSync(context.Context, capability.AuthorityRef, []controllerwire.LauncherObservation) (controllerwire.LauncherSyncResponse, error)
}

var _ LauncherSyncReader = storeLauncherSync{}

func (s *EngineController) syncLauncher(ctx context.Context, a capability.Authorized, body controllerwire.LauncherSyncRequest) (controllerwire.LauncherSyncResponse, error) {
	if err := validateLauncherObservations(body.Observed); err != nil {
		return controllerwire.LauncherSyncResponse{}, err
	}
	reader := s.Launcher
	if reader == nil && s.Store != nil {
		reader = storeLauncherSync{s.Store}
	}
	if reader == nil {
		return controllerwire.LauncherSyncResponse{}, missingEndpointPolicy()
	}
	var prepared []store.LauncherPreparation
	if len(body.Preparing) > 0 {
		if s.Store == nil {
			return controllerwire.LauncherSyncResponse{}, missingEndpointPolicy()
		}
		var err error
		prepared, err = s.Store.RenewLauncherPreparation(ctx, a.Principal.Authority, slices.Clone(body.Preparing))
		if err != nil {
			return controllerwire.LauncherSyncResponse{}, err
		}
	}
	response, err := reader.ReadLauncherSync(ctx, a.Principal.Authority, slices.Clone(body.Observed))
	if err != nil {
		return controllerwire.LauncherSyncResponse{}, err
	}
	response.Prepared = make([]controllerwire.LauncherPreparation, 0, len(prepared))
	for _, preparation := range prepared {
		response.Prepared = append(response.Prepared, controllerwire.LauncherPreparation{LeaseRef: preparation.Fence, ExpiresAt: preparation.ExpiresAt})
	}
	response.Awards = append([]controllerwire.ClaimedUnit{}, response.Awards...)
	for i := range response.Awards {
		response.Awards[i].Capability = ""
	}
	response.Remove = append([]controllerwire.LauncherJobRef{}, response.Remove...)
	response.RemoveReservations = append([]controllerwire.LauncherReservationRef{}, response.RemoveReservations...)
	return response, nil
}

type storeLauncherSync struct{ store *store.Store }

func (s storeLauncherSync) ReadLauncherSync(ctx context.Context, ref capability.AuthorityRef, observed []controllerwire.LauncherObservation) (controllerwire.LauncherSyncResponse, error) {
	if err := validateLauncherObservations(observed); err != nil {
		return controllerwire.LauncherSyncResponse{}, err
	}
	var reservations []capability.LeaseRef
	for _, observation := range observed {
		if observation.ReservationID != "" {
			reservations = append(reservations, observation.Award)
		}
	}
	var rejected []capability.LeaseRef
	for _, observation := range observed {
		if observation.Rejected {
			rejected = append(rejected, observation.Award)
		}
	}
	if len(rejected) > 0 {
		if err := s.store.RejectLauncherAwards(ctx, ref, rejected); err != nil {
			return controllerwire.LauncherSyncResponse{}, err
		}
	}
	awards, err := s.store.ReadLauncherAwards(ctx, ref, reservations)
	if err != nil {
		return controllerwire.LauncherSyncResponse{}, err
	}
	current := make(map[capability.LeaseRef]store.LauncherAward, len(awards))
	for _, award := range awards {
		fence := capability.LeaseRef{Kind: award.Claim.Kind, ID: award.Capability.UnitID, Holder: award.Claim.Award.Holder, Generation: award.Claim.Award.Generation}
		current[fence] = award
	}
	response := controllerwire.LauncherSyncResponse{Awards: []controllerwire.ClaimedUnit{}, Remove: []controllerwire.LauncherJobRef{}, RemoveReservations: []controllerwire.LauncherReservationRef{}}
	for _, observation := range observed {
		award, found := current[observation.Award]
		if !found {
			if observation.ReservationID != "" {
				response.RemoveReservations = append(response.RemoveReservations, controllerwire.LauncherReservationRef{Award: observation.Award, NodeUID: observation.NodeUID, ReservationID: observation.ReservationID})
			} else {
				response.Remove = append(response.Remove, observation.Job)
			}
			continue
		}
		unit := engineReconciledClaim(award.Capability, store.ReconciledNode{Node: award.Node, Claim: &award.Claim.Award}, award.Run)
		unit.RunID, unit.JobID, unit.SourceGeneration = award.Claim.RunID, award.Claim.NodeID, award.Claim.SourceGeneration
		unit.ParentCapabilityExpiry = award.ParentCapabilityExpiry
		unit.Delivery = capability.DeliveryBootstrap
		if award.Claim.Kind != capability.UnitNode {
			unit.Execution = nil
		}
		if observation.ReservationID != "" {
			unit.CloudResources, err = s.store.CloudAwardResources(ctx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: award.Capability.Prefix})
			if err != nil {
				return controllerwire.LauncherSyncResponse{}, err
			}
		}
		response.Awards = append(response.Awards, *unit)
	}
	return response, nil
}

func validateLauncherObservations(observed []controllerwire.LauncherObservation) error {
	jobs := map[[2]string]bool{}
	units := map[capability.LeaseRef]bool{}
	for _, observation := range observed {
		if err := observation.Validate(); err != nil {
			return err
		}
		job := [2]string{observation.Job.Namespace, observation.Job.Name}
		if observation.ReservationID == "" && jobs[job] || units[observation.Award] {
			return invalidEngineBody("launcher observations must name unique Jobs and awards")
		}
		if observation.ReservationID == "" {
			jobs[job] = true
		}
		units[observation.Award] = true
	}
	return nil
}
