package controller

import (
	"context"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

// LauncherSyncReader rechecks launcher ancestry and resolves current awards in one transaction.
type LauncherSyncReader interface {
	ReadLauncherSync(context.Context, capability.AuthorityRef, []controllerwire.LauncherObservation) (controllerwire.LauncherSyncResponse, error)
}

func (s *EngineController) syncLauncher(ctx context.Context, a capability.Authorized, body controllerwire.LauncherSyncRequest) (controllerwire.LauncherSyncResponse, error) {
	reader := s.Launcher
	if reader == nil && s.Store != nil {
		reader, _ = any(s.Store.AsOperator()).(LauncherSyncReader)
	}
	if reader == nil {
		return controllerwire.LauncherSyncResponse{}, missingEndpointPolicy()
	}
	response, err := reader.ReadLauncherSync(ctx, a.Principal.Authority, slices.Clone(body.Observed))
	if err != nil {
		return controllerwire.LauncherSyncResponse{}, err
	}
	response.Awards = append([]controllerwire.ClaimedUnit{}, response.Awards...)
	for i := range response.Awards {
		response.Awards[i].Capability = ""
	}
	response.Remove = append([]controllerwire.LauncherJobRef{}, response.Remove...)
	return response, nil
}
