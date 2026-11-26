package controller

import (
	"context"
	"errors"
	"net/http"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/routepolicy"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

// safety: the run lookup reads before any route handler runs, so a client can repeat the request without
// repeating a write.
var errRunLookupBusy = &apierr.Error{Code: hosterr.Unavailable, Message: "the run lookup found the store busy; retry the request", Details: map[string]any{"retry_after_ms": 1000}}

func runSelectorTarget(r *http.Request) (capability.RequestEvidence, error) {
	return capability.RequestEvidence{Target: capability.Target{RunID: r.PathValue("run"), NodeID: r.PathValue("job")}}, nil
}

func (s *EngineController) resolveRunSelector(ctx context.Context, principal capability.Principal, target capability.Target) (capability.Target, error) {
	target.Team = principal.Capability.Team
	if target.Team == "" || target.RunID == "" {
		return target, engineLookupError(store.ErrNotFound)
	}
	tenant, err := s.runTransactions(ctx, target.Team)
	if err != nil {
		return target, engineLookupError(err)
	}
	id, err := tenant.ResolveRunID(ctx, target.RunID)
	if store.IsRetryableErr(err) {
		return target, errors.Join(errRunLookupBusy, err)
	}
	if err != nil {
		return target, engineLookupError(err)
	}
	target.RunID = id
	return target, nil
}

func (s *EngineController) resolveExpansionTarget(ctx context.Context, principal capability.Principal, target capability.Target) (capability.Target, error) {
	target, err := s.resolveRunSelector(ctx, principal, target)
	if err != nil {
		return target, err
	}
	tenant, err := s.runTransactions(ctx, target.Team)
	if err != nil {
		return target, err
	}
	ref := runspec.NodeRef{RunID: runspec.RunID(target.RunID), NodeID: runspec.NodeID(target.NodeID)}
	node, err := tenant.ReadNode(ctx, ref)
	if err != nil {
		return target, engineLookupError(err)
	}
	if node.Ref != ref || node.Generation <= 0 {
		return target, routepolicy.MissingEndpointPolicy()
	}
	target.UnitSourceNodeID, target.UnitSourceGeneration = string(node.Ref.NodeID), int64(node.Generation)
	target.SourceGeneration = int64(node.Generation)
	return target, nil
}
