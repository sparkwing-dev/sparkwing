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
	stepFreshPod = "fresh_pod"
	stepTier     = "tier"
	stepAccess   = "access"
	stepMatch    = "match"
	stepBilling  = "billing"
)

type eligibility struct {
	profile    match.Profile
	demand     match.Demand
	chain      []authorityNode
	launcher   bool
	metered    bool
	price      CreditRate
	tier       string
	failed     string
	refused    error
	unobserved bool
}

func (e eligibility) ok() bool { return e.failed == "" }

// explainUnitEligibilityTx supplies refusal details for placement notices; awards use claimSQL.
func explainUnitEligibilityTx(ctx context.Context, tx *storeTx, team Team, unit UnitRef, claimant UnitDemand) (eligibility, error) {
	e, err := explainUnitPlacementTx(ctx, tx, team, unit, claimant)
	if err != nil || !e.ok() {
		return e, err
	}
	return explainUnitFundingTx(ctx, tx, team, unit, e)
}

func explainUnitPlacementTx(ctx context.Context, tx *storeTx, team Team, unit UnitRef, claimant UnitDemand) (eligibility, error) {
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
	facts, err := claimantFactsTx(ctx, tx, chain, runspec.Request{})
	if err != nil {
		return e, err
	}
	if reason, err := claimantTrustRefusalTx(ctx, tx, team, string(unit.RunID), chain[len(chain)-1].principal.Capability, e.launcher, facts.SingleUse); err != nil || reason != "" {
		e.failed, e.refused = stepTrust, authorityError(apierr.Forbidden, reason)
		if reason == freshPodReason {
			e.failed = stepFreshPod
		}
		return e, err
	}
	e.profile, e.demand, err = readMatchingInputsTx(ctx, tx, claimant, unit, false)
	switch {
	case errors.Is(err, errTierOutsideRanking):
		seal, err := sealedRequestTx(ctx, tx, team, string(unit.RunID))
		if err != nil {
			return e, err
		}
		facts, err := claimantFactsTx(ctx, tx, chain, seal.Request)
		e.tier = facts.Tier
		e.failed, e.refused = stepTier, errTierOutsideRanking
		return e, err
	case refusal(err):
		e.failed, e.refused = stepAccess, err
		return e, nil
	case err != nil:
		return e, err
	}
	verdict := match.Evaluate(e.profile, e.demand)
	if !verdict.OK() {
		e.failed, e.refused = stepMatch, authorityError(apierr.Forbidden, "work does not match claimant: "+verdict.String())
		return e, nil
	}
	return e, nil
}

func explainUnitFundingTx(ctx context.Context, tx *storeTx, team Team, unit UnitRef, e eligibility) (eligibility, error) {
	if e.metered {
		var err error
		if e.price, err = unitBillingPriceTx(ctx, tx, e.demand.Selector, e.profile); err != nil {
			var unpriced *UnpricedCPUClassError
			if errors.As(err, &unpriced) {
				unpriced.RunID, unpriced.NodeID = string(unit.RunID), string(unit.NodeID)
				e.failed, e.refused = stepBilling, err
				return e, nil
			}
			return e, err
		}
		if err := checkUnitBillingTx(ctx, tx, team, unit, e.price, capability.CloudLauncher(e.chain[0].principal.Capability)); err != nil {
			var refused *apierr.Error
			if errors.Is(err, ErrInsufficientCredits) || errors.As(err, &refused) && (refused.Code == apierr.BadRequest || refused.Code == apierr.LockHeld) {
				e.failed, e.refused = stepBilling, err
				return e, nil
			}
			return e, err
		}
	}
	return e, nil
}

func refusal(err error) bool {
	var denied *apierr.Error
	return errors.As(err, &denied) && (denied.Code == apierr.Forbidden || denied.Code == apierr.Unauthenticated)
}

func localClaimant(c capability.Capability) bool {
	return c.Subject.Kind == capability.SubjectLocal && c.Audience == capability.Controller && c.Binding.Kind == capability.BindingNone && (slices.Contains(c.Grants, capability.NodesClaim) || slices.Contains(c.Grants, capability.TriggersClaim))
}

const freshPodReason = "verified default-branch work runs only on fresh pods; add a launcher pool or use Sparkwing Cloud"

func claimantTrustRefusalTx(ctx context.Context, tx *storeTx, team Team, run string, root capability.Capability, launcher, singleUse bool) (string, error) {
	q := claimSQL{dialect: tx.dialect}
	reasonParam := q.param(freshPodReason)
	pools, err := q.jsonParam(root.ReservedPools)
	if err != nil {
		return "", err
	}
	provenance := q.at(q.blob("r.request_json"), "provenance")
	// A relation names each argument once even when the rule references it twice.
	query := `SELECT CASE WHEN NOT ` + q.freshPod("c.single_use", provenance) + ` THEN ` + reasonParam + ` WHEN NOT ` + q.poolTrust("r.team", "c.pools", "c.trust", "c.launcher", provenance) + ` THEN 'claimant trust requires verified default-branch work and launcher mode for reserved pools' ELSE '' END FROM runs r CROSS JOIN (SELECT ` + pools + ` AS pools,` + q.param(string(root.Trust)) + ` AS trust,CAST(` + q.param(boolInt(launcher)) + ` AS BIGINT) AS launcher,CAST(` + q.param(boolInt(singleUse)) + ` AS BIGINT) AS single_use) c WHERE r.team=? AND r.id=?`
	var reason string
	err = tx.QueryRowContext(ctx, query, append(q.args, team, run)...).Scan(&reason)
	return reason, err
}
