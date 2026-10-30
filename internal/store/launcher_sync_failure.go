package store

import (
	"context"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

const launcherSyncFailure = "Cloud launcher reconciliation failed; Sparkwing is retrying. Contact support if the run remains pending."

// RecordLauncherSyncFailure exposes a safe, recoverable error on this Cloud launcher's pending plans.
func (o *Operator) RecordLauncherSyncFailure(ctx context.Context, authority capability.AuthorityRef) error {
	return o.s.inTx(ctx, func(tx *storeTx) error {
		chain, err := authorityChainTx(ctx, tx, authority, false)
		if err != nil {
			return err
		}
		claimant := chain[0].principal.Capability
		if authority.Kind != capability.AuthorityCapability || claimant.Audience != capability.Controller || !capability.CloudLauncher(claimant) {
			return nil
		}
		root := chain[len(chain)-1].principal.Authority
		keys, err := o.launcherPrefixesTx(ctx, tx, root, claimant.Subject, nil)
		if err != nil {
			return err
		}
		var plans []capability.Capability
		for _, key := range keys {
			if key.fence.Kind != capability.UnitPlan {
				continue
			}
			c, err := loadCapabilityTx(ctx, tx, key.prefix)
			if err != nil {
				return err
			}
			plans = append(plans, c)
		}
		slices.SortFunc(plans, func(a, b capability.Capability) int {
			if order := strings.Compare(a.Team, b.Team); order != 0 {
				return order
			}
			return strings.Compare(a.Binding.RunID, b.Binding.RunID)
		})
		var teams []Team
		for _, plan := range plans {
			teams = append(teams, Team(plan.Team))
		}
		if err := lockFinancialTeamsTx(ctx, tx, teams...); err != nil {
			return err
		}
		if _, err := authorityChainTx(ctx, tx, authority, true); err != nil {
			return err
		}
		for _, plan := range plans {
			if err := lockStateRunTx(ctx, tx, Team(plan.Team), plan.Binding.RunID); err != nil {
				return err
			}
			if _, err := tx.execContext(ctx, `UPDATE runs SET error=? WHERE team=? AND id=? AND status='pending' AND finished_at IS NULL AND error='' AND EXISTS (SELECT 1 FROM triggers t WHERE t.team=runs.team AND t.id=runs.id AND t.status='claimed' AND t.capability_prefix=? AND t.claimed_by=? AND t.generation=? AND t.lease_expires_at>?)`, launcherSyncFailure, plan.Team, plan.Binding.RunID, plan.Prefix, plan.Binding.Holder, plan.Binding.Generation, tx.now(ctx).UnixNano()); err != nil {
				return err
			}
		}
		return nil
	})
}
