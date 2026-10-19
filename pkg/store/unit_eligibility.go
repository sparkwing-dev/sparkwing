package store

import (
	"context"
	"errors"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

const (
	stepKinds    = "kinds"
	stepTrust    = "trust"
	stepTier     = "tier"
	stepAccess   = "access"
	stepMatch    = "match"
	stepCapacity = "capacity"
	stepBilling  = "billing"
)

// safety: a named claim leaves billing to award issuance, which reports the exact credit standing. Only the
// claimant's own claim turns an unpriced unit into an error; judging another claimant marks it ineligible.
type eligibilityChecks struct{ lock, fit, billing, claim bool }

type eligibility struct {
	profile    match.Profile
	demand     match.Demand
	chain      []authorityNode
	launcher   bool
	metered    bool
	price      CreditRate
	score      int
	tier       string
	failed     string
	refused    error
	unobserved bool
}

func (e eligibility) ok() bool { return e.failed == "" }

// safety: claims, ranking holds and notices all judge a claimant here, so they cannot disagree about eligibility.
func unitEligibilityTx(ctx context.Context, tx *storeTx, team Team, unit UnitRef, claimant UnitDemand, checks eligibilityChecks) (eligibility, error) {
	var e eligibility
	chain, err := authorityChainTx(ctx, tx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claimant.CapabilityPrefix}, false)
	if unavailableRunner(err) || refusal(err) {
		e.failed, e.refused = stepAccess, err
		return e, nil
	}
	if err != nil {
		return e, err
	}
	e.chain = chain
	c := chain[0].principal.Capability
	e.launcher, e.metered = launcherClaimant(c), meteredPlacement(c, chain[len(chain)-1].principal.Capability)
	if _, err := claimKinds(c, UnitDemand{Target: &unit}); err != nil || len(claimant.Kinds) > 0 && !slices.Contains(claimant.Kinds, unit.Kind) {
		e.failed, e.refused = stepKinds, errors.Join(err, authorityError(apierr.Forbidden, "claimant does not take this kind of unit"))
		return e, nil
	}
	if trusted, err := claimantTrustsRunTx(ctx, tx, team, unit.RunID, chain[len(chain)-1].principal.Capability, claimant.Trust); err != nil || !trusted {
		e.failed, e.refused = stepTrust, authorityError(apierr.Forbidden, "claimant takes only verified default-branch work")
		return e, err
	}
	e.profile, e.demand, err = readMatchingInputsTx(ctx, tx, claimant, unit, checks.lock)
	switch {
	case errors.Is(err, errTierOutsideRanking):
		seal, err := sealedRequestTx(ctx, tx, team, unit.RunID)
		if err != nil {
			return e, err
		}
		e.tier, err = claimTierTx(ctx, tx, chain, seal.Request)
		e.failed, e.refused = stepTier, errTierOutsideRanking
		return e, err
	case refusal(err):
		e.failed, e.refused = stepAccess, err
		return e, nil
	case err != nil:
		return e, err
	}
	if e.metered {
		if e.price, err = unitBillingPriceTx(ctx, tx, e.demand.Request); err != nil {
			var unpriced *UnpricedCPUClassError
			if errors.As(err, &unpriced) && !checks.claim {
				e.failed, e.refused = stepBilling, err
				return e, nil
			}
			if errors.As(err, &unpriced) {
				unpriced.RunID, unpriced.NodeID = unit.RunID, unit.NodeID
			}
			return e, err
		}
	}
	verdict := match.Evaluate(e.profile, e.demand)
	if !verdict.OK() {
		e.failed, e.refused = stepMatch, authorityError(apierr.Forbidden, "work does not match claimant: "+verdict.String())
		return e, nil
	}
	e.score = verdict.PreferenceScore
	if checks.fit && !match.Fits(e.profile.Capacity, claimant.Available, e.demand.Request).OK() {
		e.failed, e.refused = stepCapacity, authorityError(apierr.LockHeld, "claimant capacity is unavailable")
		return e, nil
	}
	if e.metered && checks.billing {
		funded, err := eligibleUnitBillingTx(ctx, tx, team, unit, e.price, e.launcher)
		if err != nil {
			return e, err
		}
		if !funded {
			e.failed, e.refused = stepBilling, ErrInsufficientCredits
		}
	}
	return e, nil
}

func refusal(err error) bool {
	var denied *apierr.Error
	return errors.As(err, &denied) && (denied.Code == apierr.Forbidden || denied.Code == apierr.Unauthenticated)
}

func rankedObservable(c capability.Capability) bool {
	return registeredClaimant(c) || actionsClaimant(c) || launcherClaimant(c) || localClaimant(c)
}

func localClaimant(c capability.Capability) bool {
	return c.Subject.Kind == capability.SubjectLocal && c.Audience == capability.Controller && c.Binding.Kind == capability.BindingNone && (slices.Contains(c.Grants, capability.NodesClaim) || slices.Contains(c.Grants, capability.TriggersClaim))
}

// RunnerTrust is the runs a claimant accepts work from. The empty value takes the
// server default: verified default-branch work only when the claimant holds a
// pool that a secret is reserved to, and any run otherwise.
type RunnerTrust string

const (
	TrustVerifiedDefault RunnerTrust = "verified-default"
	TrustAny             RunnerTrust = "any"
)

// Valid reports whether t is the server default or a named class.
func (t RunnerTrust) Valid() bool { return t == "" || t == TrustVerifiedDefault || t == TrustAny }

// safety: a claimant that runs one job after another shares host state between them,
// so work from a run anyone can push must not reach a host that later holds a pool's
// reserved secrets.
func claimantTrustsRunTx(ctx context.Context, tx *storeTx, team Team, run string, root capability.Capability, trust RunnerTrust) (bool, error) {
	if trust == TrustAny {
		return true, nil
	}
	if trust == "" {
		reserved, err := poolsHoldSecretsTx(ctx, tx, team, root.ReservedPools)
		if err != nil || !reserved {
			return true, err
		}
	}
	seal, err := sealedRequestTx(ctx, tx, team, run)
	if err != nil {
		return false, err
	}
	return verifiedDefaultBranch(seal.Request.Git), nil
}

func poolsHoldSecretsTx(ctx context.Context, tx *storeTx, team Team, pools []string) (bool, error) {
	for _, pool := range pools {
		var held int
		err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM secrets WHERE team=? AND only_on=?`, team, pool).Scan(&held)
		if err != nil || held > 0 {
			return held > 0, err
		}
	}
	return false, nil
}

// safety: facts a child or retry carried from another push must not vouch for a different ref or commit.
func verifiedDefaultBranch(git runspec.Git) bool {
	app := git.App.Value
	return git.App.Set && app.DefaultBranchPush && app.DefaultBranch != "" && app.Ref == "refs/heads/"+app.DefaultBranch && app.Ref == git.Ref && app.SHA == git.SHA
}
