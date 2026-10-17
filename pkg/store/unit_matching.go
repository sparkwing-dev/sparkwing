package store

import (
	"context"
	"encoding/json"
	"math"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func resolveMatchingInputsTx(ctx context.Context, tx *storeTx, request UnitDemand, unit UnitRef) (match.Profile, match.Demand, error) {
	return readMatchingInputsTx(ctx, tx, request, unit, true)
}

func readMatchingInputsTx(ctx context.Context, tx *storeTx, request UnitDemand, unit UnitRef, lock bool) (match.Profile, match.Demand, error) {
	chain, err := authorityChainTx(ctx, tx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: request.CapabilityPrefix}, lock)
	if err != nil {
		return match.Profile{}, match.Demand{}, err
	}
	p := chain[0].principal
	here := false
	for _, ancestor := range chain {
		if ancestor.principal.Capability.Purpose == capability.PurposeHere && ancestor.principal.Capability.Binding.RunID == unit.RunID {
			here = true
		}
	}
	var profile match.Profile
	if tx.store.claimProfiles != nil {
		profile, _, err = tx.store.claimProfiles.ResolveClaimProfile(ctx, p)
		if err != nil {
			return profile, match.Demand{}, err
		}
	}
	profile = authenticatedClaimProfile(p.Capability, profile, here)
	profile.Labels, profile.OS, profile.Arch = slices.Clone(request.Labels), request.OS, request.Arch
	if tx.store.claimProfiles == nil || here {
		profile.Capacity = request.Capacity
	}
	var team, repo, display string
	var requestBody, specBody, pools []byte
	err = tx.QueryRowContext(ctx, `SELECT team,repo_key,repo_display,request_json,spec_json,allowed_pools FROM runs WHERE id=?`, unit.RunID).Scan(&team, &repo, &display, &requestBody, &specBody, &pools)
	if err != nil {
		return profile, match.Demand{}, err
	}
	if !launcherClaimant(p.Capability) && p.Capability.Team != team {
		return profile, match.Demand{}, authorityError(apierr.Forbidden, "claimant belongs to another team")
	}
	var requestSeal runspec.SealedRequest
	if err = json.Unmarshal(requestBody, &requestSeal); err != nil {
		return profile, match.Demand{}, err
	}
	if display == "" {
		display = requestSeal.Request.Git.Repository.Display
	}
	if err := checkRepositoryClaimScope(chain, capability.RepositoryIdentity{Key: repo, Display: display}); err != nil {
		return profile, match.Demand{}, err
	}
	git := requestSeal.Request.Git
	if err := checkPushClaimScope(chain, string(git.Repository.Key), git.Ref, git.SHA); err != nil {
		return profile, match.Demand{}, err
	}
	if err := checkPersonalMachineTx(ctx, tx, chain, unit.RunID); err != nil {
		return profile, match.Demand{}, err
	}
	options := requestSeal.Request.Options
	if options.Runner.Value.Kind == runspec.RunnerHere && !here && (requestSeal.Request.Burst == nil || unit.Kind != capability.UnitNode) {
		return profile, match.Demand{}, authorityError(apierr.Forbidden, "work is reserved for its here claimant")
	}
	if len(specBody) > 0 {
		var sealed runspec.Spec
		if err = json.Unmarshal(specBody, &sealed); err != nil {
			return profile, match.Demand{}, err
		}
		if (unit.Kind == capability.UnitNode || unit.Kind == capability.UnitEval) && runGuardRefusal(RunRow{Spec: &sealed}) != "" {
			return profile, match.Demand{}, authorityError(apierr.Forbidden, "run guard refused execution")
		}
		options = sealed.Resolved.Settings.Options
	}
	demand := match.Demand{Selector: slices.Clone(options.Requires.Value), Prefers: slices.Clone(options.Prefers.Value), Request: matchingResources(options.Resources.Value), Trigger: unit.Kind == capability.UnitTrigger || unit.Kind == capability.UnitPlan, Repo: &match.Repository{URL: display}}
	if demand.Repo.URL == "" {
		demand.Repo.URL = requestSeal.Request.Git.Repository.Display
	}
	if err = json.Unmarshal(pools, &demand.AllowedPools); err != nil {
		return profile, demand, err
	}
	if unit.Kind == capability.UnitNode {
		var body []byte
		if err = tx.QueryRowContext(ctx, `SELECT execution_json FROM nodes WHERE team=? AND run_id=? AND node_id=?`, team, unit.RunID, unit.NodeID).Scan(&body); err != nil {
			return profile, demand, err
		}
		var execution runspec.JobExecutionSettings
		if err = json.Unmarshal(body, &execution); err != nil {
			return profile, demand, err
		}
		demand.Selector, demand.Prefers, demand.Request = slices.Clone(execution.Requires.Value), slices.Clone(execution.Prefers.Value), matchingResources(execution.Resources.Value)
		if demand.Request == (match.Resources{}) {
			demand.Request.Cores = admission.DefaultChargeCores
		}
	}
	switch selected := requestSeal.Request.Options.Runner.Value; selected.Kind {
	case runspec.RunnerName:
		if selected.Value == "" {
			return profile, demand, authorityError(apierr.IntegrityFailure, "sealed runner name is empty")
		}
		if profile.Name != selected.Value {
			return profile, demand, authorityError(apierr.Forbidden, "work selects another runner")
		}
	case runspec.RunnerLabel:
		if selected.Value == "" {
			return profile, demand, authorityError(apierr.IntegrityFailure, "sealed runner label is empty")
		}
		demand.Selector = append(demand.Selector, selected.Value)
	}
	if !validMatchingResources(profile.Capacity) || !validMatchingResources(request.Available) || !validMatchingResources(demand.Request) {
		return profile, demand, authorityError(apierr.BadRequest, "invalid claim resources")
	}
	return profile, demand, nil
}

func matchingResources(v runspec.Resources) match.Resources {
	return match.Resources{Cores: v.Cores.Value, MemoryBytes: v.MemoryBytes.Value}
}

func authenticatedClaimProfile(c capability.Capability, profile match.Profile, here bool) match.Profile {
	profile.Name, profile.ReservedPools, profile.Class = c.Subject.ID, slices.Clone(c.ReservedPools), ""
	switch {
	case launcherClaimant(c):
		profile.Class, profile.Location = match.ClassCloud, "cloud"
	case here || c.Subject.Kind == capability.SubjectLocal:
		profile.Class, profile.Location = match.ClassCoordinator, "local"
	case c.Subject.Kind == capability.SubjectMachine:
		profile.Class = match.ClassAgent
	}
	return profile
}

func validMatchingResources(v match.Resources) bool {
	return v.Cores >= 0 && !math.IsNaN(v.Cores) && !math.IsInf(v.Cores, 0) && v.MemoryBytes >= 0
}

func checkRepositoryClaimScope(chain []authorityNode, repository capability.RepositoryIdentity) error {
	for _, ancestor := range chain {
		if !capability.RepositoryAllowed(ancestor.principal.Capability.Repositories, repository) {
			return authorityError(apierr.Forbidden, "claimant repository scope excludes work")
		}
	}
	return nil
}

func checkPushClaimScope(chain []authorityNode, repo, ref, sha string) error {
	for _, node := range chain {
		binding := node.principal.Capability.Binding
		if binding.Kind != capability.BindingPush {
			continue
		}
		push := binding.Push
		if push == nil || push.WorkflowRunID == "" || push.RepositoryKey != repo || push.Ref != ref || push.SHA != sha {
			return authorityError(apierr.Forbidden, "work is outside the Actions push scope")
		}
	}
	return nil
}

func launcherClaimant(c capability.Capability) bool {
	return c.Subject.Kind == capability.SubjectService && c.Binding.Kind == capability.BindingNone && slices.Contains(c.Grants, capability.ClaimsLaunch)
}

func selfClaimNodeTx(ctx context.Context, tx *storeTx, team Team, actor Actor, ref NodeRef) (UnitClaim, error) {
	if err := checkOwnerTx(ctx, tx, team, actor, ref.RunID, tx.now()); err != nil {
		return UnitClaim{}, err
	}
	p, err := tx.ResolveAuthority(ctx, actor.Authority)
	if err != nil {
		return UnitClaim{}, err
	}
	demand, err := ownerClaimDemandTx(ctx, tx, p)
	if err != nil {
		return UnitClaim{}, err
	}
	claim, err := claimUnitTx(ctx, tx, demand, UnitRef{Kind: capability.UnitNode, RunID: ref.RunID, NodeID: ref.NodeID})
	if err != nil {
		return UnitClaim{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE nodes SET owner_capability_prefix=? WHERE team=? AND run_id=? AND node_id=? AND capability_prefix=? AND generation=?`, actor.CapabilityPrefix, team, ref.RunID, ref.NodeID, claim.Award.CapabilityPrefix, claim.Award.Generation)
	if err != nil {
		return UnitClaim{}, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return UnitClaim{}, err
	}
	if count != 1 {
		return UnitClaim{}, holderRefusal()
	}
	return claim, nil
}
