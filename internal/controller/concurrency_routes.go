package controller

import (
	"context"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// ConcurrencyStore reads groups under current authority.
type ConcurrencyStore interface {
	Team() store.Team
	ReadAuthorizedGroup(context.Context, capability.Authorized, string) (*store.ConcurrencyState, error)
}

func (s *Server) concurrencyStore(ctx context.Context, team string) (ConcurrencyStore, error) {
	return tenantPort[ConcurrencyStore](ctx, s, store.Team(team))
}

func (s *Server) concurrencyEndpoints() []controllerwire.Endpoint {
	return []controllerwire.Endpoint{
		{Method: "GET", Pattern: "/api/v1/queue/groups/{group}", Authentication: capability.AuthCapability, Decode: concurrencyTarget, ResolveTarget: resolveConcurrencyTarget, Handle: s.handleConcurrencyState},
	}
}

func resolveConcurrencyTarget(_ context.Context, p capability.Principal, target capability.Target) (capability.Target, error) {
	if target.SlotRunID == "" {
		target.SlotRunID, target.SlotNodeID = p.Capability.Binding.RunID, p.Capability.Binding.NodeID
	}
	target.SlotGeneration = p.Capability.Binding.Generation
	return target, nil
}

func concurrencyTarget(r *http.Request) (capability.RequestEvidence, error) {
	e := capability.RequestEvidence{Target: capability.Target{SlotKey: r.PathValue("group")}}
	if err := controllerwire.ValidateQueueGroupName(e.Target.SlotKey); err != nil {
		return e, invalidEngineBody("invalid concurrency group")
	}
	return e, nil
}
