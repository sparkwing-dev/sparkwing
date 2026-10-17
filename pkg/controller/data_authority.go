package controller

import (
	"context"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func (s *DataController) persistedDataAuthority(ctx context.Context, a capability.Authorized) (blob.Authority, error) {
	c := a.Principal.Capability
	if s.Store == nil || a.Principal.Authority.Kind != capability.AuthorityCapability || c.Prefix == "" || a.Principal.Authority.ID != c.Prefix || c.Team == "" {
		return blob.Authority{}, missingEndpointPolicy()
	}
	tenant, err := s.Store.ForTeam(ctx, store.Team(c.Team))
	if err != nil {
		return blob.Authority{}, err
	}
	selector := blob.Authority{CapabilityRef: c.Prefix}
	if target := a.Target.Data; c.Data != nil && c.Data.Use == capability.DataPublish && target != nil && target.NodeID != "" && target.Attempt > 0 && target.Generation > 0 {
		selector.Attempt = runspec.Optional[runspec.AttemptIdentity]{Set: true, Value: runspec.AttemptIdentity{Team: c.Team, RunID: target.RunID, NodeID: target.NodeID, Attempt: target.Attempt, Generation: target.Generation}}
	}
	grant := capability.DataRead
	if slices.Contains(a.Access.Grants, capability.DataWrite) {
		grant = capability.DataWrite
	} else if a.Access.Binding == capability.BindSourceBundle && slices.Contains(a.Access.Grants, capability.SourceRead) {
		grant = capability.SourceRead
	}
	return tenant.ResolveBlobAuthority(ctx, selector, grant)
}

func dataForbidden() *apierr.Error {
	return apierr.Authorization(apierr.ReasonBinding, "data authority does not match the authenticated scope")
}

func validateDataAuthority(a capability.Authorized, authority blob.Authority) error {
	c := a.Principal.Capability
	scope := c.Data
	if scope == nil || scope.RepositoryKey == "" || c.Audience != capability.Controller || c.Team == "" || a.Target.Team != c.Team ||
		a.Principal.Authority.ID == "" || authority.CapabilityRef == "" || authority.Principal == "" ||
		string(authority.Repo) != scope.RepositoryKey || a.Access.Binding != capability.BindData && a.Access.Binding != capability.BindSourceBundle || a.ResultReplay || a.LogTailAttempt != 0 {
		return dataForbidden()
	}
	if a.Access.Binding == capability.BindSourceBundle && (c.Delivery != capability.DeliveryBootstrap || scope.Use != capability.DataClaim && !(scope.Use == capability.DataExpansion && c.Unit == capability.UnitEval) || authority.Attempt.Set || authority.Upload.Set || a.Target.Data == nil || a.Target.Data.Operation != capability.DataDownload) {
		return dataForbidden()
	}
	if authority.Attempt.Set && !dataAttemptMatches(authority.Attempt.Value, c.Team, scope) {
		return dataForbidden()
	}
	if scope.Use == capability.DataRemoteCache && authority.Attempt.Set {
		return dataForbidden()
	}
	if !authority.Upload.Set {
		return nil
	}
	i := authority.Upload.Value
	if scope.Use == capability.DataRemoteCache || scope.Use == capability.DataExpansion || i.Team != c.Team || i.RunID == "" ||
		i.RunID != scope.RunID || i.Principal != authority.Principal || i.CapabilityRef != authority.CapabilityRef ||
		scope.WriteRef == "" || authority.Ref.Scope.CapabilityScope() != scope.WriteRef || c.Delivery == capability.DeliveryBootstrap {
		return dataForbidden()
	}
	switch i.Kind {
	case blob.UploadJob:
		if scope.Use != capability.DataClaim || c.Unit != capability.UnitNode || !i.Attempt.Set || i.Unit.Set || i.Attempt != authority.Attempt ||
			!dataAttemptMatches(i.Attempt.Value, c.Team, scope) || a.Binding.Kind != capability.BindingNode ||
			a.Binding.RunID != i.RunID || a.Binding.NodeID != i.Attempt.Value.NodeID || a.Binding.Attempt != i.Attempt.Value.Attempt || a.Binding.Generation != i.Attempt.Value.Generation {
			return dataForbidden()
		}
	case blob.UploadOwnerUnit:
		unit := i.Unit.Value
		if scope.Use != capability.DataClaim || i.Attempt.Set || authority.Attempt.Set || !i.Unit.Set || unit.ID == "" ||
			unit.ID != c.UnitID || unit.Generation <= 0 || unit.Generation != a.Binding.Generation || a.Binding.RunID != i.RunID ||
			!dataOwnerKind(c.Unit, unit.Kind) {
			return dataForbidden()
		}
	case blob.UploadPublication:
		if scope.Use != capability.DataPublish || i.Unit.Set || i.Attempt != authority.Attempt || c.Binding.Kind != capability.BindingRun || c.Binding.RunID != i.RunID ||
			i.Attempt.Set && !dataAttemptMatches(i.Attempt.Value, c.Team, scope) {
			return dataForbidden()
		}
	default:
		return dataForbidden()
	}
	return nil
}

func dataAttemptMatches(attempt runspec.AttemptIdentity, team string, scope *capability.DataScope) bool {
	return attempt.Team == team && attempt.RunID != "" && attempt.RunID == scope.RunID && attempt.NodeID != "" && attempt.Attempt > 0 && attempt.Generation > 0 &&
		(scope.NodeID == "" || attempt.NodeID == scope.NodeID) && (scope.Attempt == 0 || attempt.Attempt == scope.Attempt) &&
		(scope.Generation == 0 || attempt.Generation == scope.Generation)
}

func dataOwnerKind(unit capability.UnitKind, upload blob.UploadUnitKind) bool {
	return unit == capability.UnitTrigger && upload == blob.UploadTriggerUnit || unit == capability.UnitPlan && upload == blob.UploadPlanUnit ||
		unit == capability.UnitEval && upload == blob.UploadEvalUnit
}
