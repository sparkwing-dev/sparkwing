package controller

import (
	"context"
	"net/http"
	"strconv"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func runSelectorTarget(r *http.Request) (capability.RequestEvidence, error) {
	return capability.RequestEvidence{Target: capability.Target{RunID: r.PathValue("run"), NodeID: r.PathValue("job")}}, nil
}

func (s *EngineController) resolveRunSelector(ctx context.Context, principal capability.Principal, target capability.Target) (capability.Target, error) {
	target.Team = principal.Capability.Team
	if target.Team == "" || target.RunID == "" {
		return target, engineLookupError(store.ErrNotFound)
	}
	number, err := strconv.ParseInt(target.RunID, 10, 64)
	numeric := err == nil && number > 0 && strconv.FormatInt(number, 10) == target.RunID
	if !numeric {
		tenant, err := s.runTransactions(ctx, target.Team)
		if err != nil {
			return target, engineLookupError(err)
		}
		_, err = tenant.ReadRun(ctx, target.RunID)
		return target, engineLookupError(err)
	}
	if s.Store == nil {
		return target, missingEndpointPolicy()
	}
	tenant, err := s.tenantForTeam(ctx, store.Team(target.Team))
	if err != nil {
		return target, engineLookupError(err)
	}
	views, err := tenant.ListRunViews(ctx, number-1, number, 1)
	if err != nil {
		return target, engineLookupError(err)
	}
	if len(views) != 1 || views[0].Snapshot.Run.Team != store.Team(target.Team) || views[0].Snapshot.Run.Number != number {
		return target, engineLookupError(store.ErrNotFound)
	}
	target.RunID = views[0].Snapshot.Run.ID
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
	ref := controllerwire.NodeRef{RunID: target.RunID, NodeID: target.NodeID}
	node, err := tenant.ReadNode(ctx, ref)
	if err != nil {
		return target, engineLookupError(err)
	}
	if node.Ref != ref || node.Generation <= 0 {
		return target, missingEndpointPolicy()
	}
	target.UnitSourceNodeID, target.UnitSourceGeneration = node.Ref.NodeID, node.Generation
	target.SourceGeneration = node.Generation
	return target, nil
}
