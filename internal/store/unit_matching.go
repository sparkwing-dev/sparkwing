package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func resolveMatchingInputsTx(ctx context.Context, tx *storeTx, request UnitDemand, unit UnitRef) (match.Profile, match.Demand, error) {
	return readMatchingInputsTx(ctx, tx, request, unit, true)
}

// perf: matchingRun memoizes run-level matching reads that a snapshot's per-node placement would repeat per node.
// safety: only read-only snapshot transactions reuse it, so no write in the same transaction can stale it.
type matchingRun struct {
	chain   []authorityNode
	here    bool
	profile match.Profile
	team    string
	display string
	request runspec.SealedRequest
	spec    *runspec.Spec
	pools   []byte
	err     error
}

type matchingRunKey struct{ prefix, run string }

func readMatchingRunTx(ctx context.Context, tx *storeTx, prefix, runID string, lock bool) *matchingRun {
	key := matchingRunKey{prefix, runID}
	memo := !lock && tx.snapshotRead
	if memo {
		if cached, ok := tx.matchingRuns[key]; ok {
			return cached
		}
	}
	run := tx.operator.loadMatchingRunTx(ctx, tx, prefix, runID, lock)
	if memo {
		if tx.matchingRuns == nil {
			tx.matchingRuns = map[matchingRunKey]*matchingRun{}
		}
		tx.matchingRuns[key] = run
	}
	return run
}

func (op *Operator) loadMatchingRunTx(ctx context.Context, tx *storeTx, prefix, runID string, lock bool) *matchingRun {
	run := &matchingRun{}
	chain, err := authorityChainTx(ctx, tx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: prefix}, lock)
	if err != nil {
		run.err = err
		return run
	}
	run.chain = chain
	p := chain[0].principal
	for _, ancestor := range chain {
		if ancestor.principal.Capability.Purpose == capability.PurposeHere && ancestor.principal.Capability.Binding.RunID == runID {
			run.here = true
		}
	}
	if tx.store.claimProfiles != nil {
		run.profile, _, err = tx.store.claimProfiles.ResolveClaimProfile(ctx, p)
		if err != nil {
			run.err = err
			return run
		}
	}
	run.profile = authenticatedClaimProfile(p.Capability, run.profile, run.here)
	var repo, display string
	var requestBody, specBody []byte
	err = tx.QueryRowContext(ctx, `SELECT team,repo_key,repo_display,request_json,spec_json,allowed_pools FROM runs WHERE id=?`, runID).Scan(&run.team, &repo, &display, &requestBody, &specBody, &run.pools)
	if errors.Is(err, sql.ErrNoRows) || err == nil && !capability.CloudLauncher(p.Capability) && p.Capability.Team != run.team {
		run.err = bindingRefusal()
		return run
	}
	if err != nil {
		run.err = err
		return run
	}
	if run.err = json.Unmarshal(requestBody, &run.request); run.err != nil {
		return run
	}
	if display == "" {
		display = run.request.Request.Git.Repository.Display
	}
	run.display = display
	if run.err = checkRepositoryClaimScope(chain, capability.RepositoryIdentity{Key: repo, Display: display}); run.err != nil {
		return run
	}
	speaksFor := run.request.Provenance
	if run.err = checkPushClaimScope(chain, string(run.request.Request.Git.Repository.Key), speaksFor.Ref(), speaksFor.SHA()); run.err != nil {
		return run
	}
	if run.err = checkPersonalMachineTx(ctx, tx, chain, runID); run.err != nil {
		return run
	}
	if len(specBody) > 0 {
		run.spec = &runspec.Spec{}
		run.err = json.Unmarshal(specBody, run.spec)
	}
	return run
}

func readMatchingInputsTx(ctx context.Context, tx *storeTx, request UnitDemand, unit UnitRef, lock bool) (match.Profile, match.Demand, error) {
	run := readMatchingRunTx(ctx, tx, request.CapabilityPrefix, unit.RunID, lock)
	profile := run.profile
	profile.Labels, profile.OS, profile.Arch = slices.Clone(request.Labels), request.OS, request.Arch
	if tx.store.claimProfiles == nil || run.here {
		profile.Capacity = request.Capacity
	}
	if run.err != nil {
		return profile, match.Demand{}, run.err
	}
	chain, here, team, display, requestSeal, pools := run.chain, run.here, run.team, run.display, run.request, run.pools
	var err error
	if requestSeal.Request.Burst != nil {
		if err := checkBurstClaimantTx(ctx, tx, Team(team), chain, unit); err != nil {
			return profile, match.Demand{}, err
		}
	}
	options := requestSeal.Request.Options
	if options.Runner.Value.Kind == runspec.RunnerHere && !here && (requestSeal.Request.Burst == nil || unit.Kind != capability.UnitNode) {
		return profile, match.Demand{}, authorityError(apierr.Forbidden, "work is reserved for its here claimant")
	}
	if sealed := run.spec; sealed != nil {
		if (unit.Kind == capability.UnitNode || unit.Kind == capability.UnitEval) && runGuardRefusal(RunRow{Spec: sealed}) != "" {
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
	case runspec.RunnerActions:
		if (unit.Kind == capability.UnitTrigger || unit.Kind == capability.UnitPlan) && !slices.ContainsFunc(chain, func(node authorityNode) bool { return node.principal.Capability.Binding.Kind == capability.BindingPush }) {
			return profile, demand, authorityError(apierr.Forbidden, "work is reserved for a GitHub Actions runner")
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
	if rankedUnit(requestSeal.Request, demand) {
		_, err = claimRankTx(ctx, tx, Team(team), chain, requestSeal.Request)
	}
	return profile, demand, err
}

func matchingResources(v runspec.Resources) match.Resources {
	return match.Resources{Cores: v.Cores.Value, MemoryBytes: v.MemoryBytes.Value}
}

func authenticatedClaimProfile(c capability.Capability, profile match.Profile, here bool) match.Profile {
	profile.Name, profile.ReservedPools, profile.Class = c.Subject.ID, slices.Clone(c.ReservedPools), ""
	switch {
	case capability.CloudLauncher(c):
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
	return capability.Launcher(c)
}

func selfClaimNodeTx(ctx context.Context, tx *storeTx, team Team, actor Actor, ref controllerwire.NodeRef, reserved *match.Resources) (UnitClaim, error) {
	if err := checkOwnerTx(ctx, tx, team, actor, ref.RunID, tx.now()); err != nil {
		return UnitClaim{}, err
	}
	p, err := tx.ResolveAuthority(ctx, actor.Authority)
	if err != nil {
		return UnitClaim{}, err
	}
	demand, err := reservedOwnerDemandTx(ctx, tx, p, reserved)
	if err != nil {
		return UnitClaim{}, err
	}
	claim, err := tx.operator.claimUnitTx(ctx, tx, demand, UnitRef{Kind: capability.UnitNode, RunID: ref.RunID, NodeID: ref.NodeID})
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
