package controller

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/routepolicy"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"

	runners "github.com/sparkwing-dev/sparkwing/internal/runners/award"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// LauncherSyncReader rechecks launcher ancestry and resolves current awards.
type LauncherSyncReader interface {
	ReadLauncherSync(context.Context, capability.AuthorityRef, controllerwire.LauncherSyncRequest) (controllerwire.LauncherSyncResponse, error)
}

var _ LauncherSyncReader = storeLauncherSync{}

func (s *EngineController) syncLauncher(ctx context.Context, a capability.Authorized, body controllerwire.LauncherSyncRequest) (controllerwire.LauncherSyncResponse, error) {
	if _, err := validateLauncherSync(body); err != nil {
		return controllerwire.LauncherSyncResponse{}, err
	}
	reader := s.Launcher
	if reader == nil {
		return controllerwire.LauncherSyncResponse{}, routepolicy.MissingEndpointPolicy()
	}
	response, err := reader.ReadLauncherSync(ctx, a.Principal.Authority, body)
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
		ReconcileLauncherAwards(context.Context, capability.AuthorityRef, []capability.LauncherObservation, []capability.LeaseRef, []capability.LeaseRef, []capability.LeaseRef, []capability.LauncherActivation) ([]store.LauncherAward, []store.LauncherPreparation, error)
		RecordLauncherSyncFailure(context.Context, capability.AuthorityRef) error
	}
}

func (s storeLauncherSync) ReadLauncherSync(ctx context.Context, ref capability.AuthorityRef, body controllerwire.LauncherSyncRequest) (controllerwire.LauncherSyncResponse, error) {
	observed, err := validateLauncherSync(body)
	if err != nil {
		return controllerwire.LauncherSyncResponse{}, err
	}
	var reservations []capability.LeaseRef
	for _, observation := range observed {
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
	awards, prepared, err := s.store.ReconcileLauncherAwards(ctx, ref, observed, reservations, body.Preparing, rejected, body.Activations)
	var refusal *apierr.Error
	if err != nil && ctx.Err() == nil && (!errors.As(err, &refusal) || apierr.Status(refusal.Code) >= http.StatusInternalServerError) {
		if diagnosticErr := s.store.RecordLauncherSyncFailure(ctx, ref); diagnosticErr != nil {
			slog.ErrorContext(ctx, "record launcher reconciliation diagnostic", "err", diagnosticErr)
		}
	}
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
		unit := engineReconciledClaim(award.Capability, runstate.ReconciledNode{Node: award.Node, Claim: &award.Claim.Award}, award.Run)
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

func validateLauncherSync(body controllerwire.LauncherSyncRequest) ([]capability.LauncherObservation, error) {
	observed := slices.Clone(body.Observed)
	for _, demand := range body.Capacity {
		observed = append(observed, capability.LauncherObservation{Award: demand.Award, Reservation: true, CapacityUnconfigured: demand.Unconfigured})
	}
	activated := map[capability.LeaseRef]bool{}
	for _, activation := range body.Activations {
		if activated[activation.Award] {
			return nil, invalidEngineBody("launcher activations must name unique awards")
		}
		activated[activation.Award] = true
		if err := activation.Validate(); err != nil {
			return nil, err
		}
		i := slices.IndexFunc(body.Observed, func(o capability.LauncherObservation) bool {
			return o.Award == activation.Award && !o.Reservation && !o.Rejected
		})
		if i < 0 {
			return nil, invalidEngineBody("launcher activation requires an execution Job observation")
		}
	}

	units := map[capability.LeaseRef]bool{}
	for _, observation := range observed {
		if err := observation.Validate(); err != nil {
			return nil, err
		}
		if units[observation.Award] {
			return nil, invalidEngineBody("launcher observations must name unique awards")
		}
		units[observation.Award] = true
	}
	return observed, nil
}
