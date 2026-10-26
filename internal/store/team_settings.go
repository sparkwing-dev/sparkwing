package store

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

// TeamSettings reads the team's enrollment, placement and child-run settings.
func (t *Tenant) TeamSettings(ctx context.Context) (TeamSettings, error) {
	var out TeamSettings
	err := t.s.inTx(ctx, func(tx *storeTx) error {
		var err error
		out, err = teamSettingsTx(ctx, tx, t.team)
		return err
	})
	return out, err
}

func teamSettingsTx(ctx context.Context, tx *storeTx, team Team) (TeamSettings, error) {
	var out TeamSettings
	if err := tx.QueryRowContext(ctx, `SELECT members_can_add_machines,max_children_per_root FROM teams WHERE name=?`, team).Scan(&out.MembersCanAddMachines, &out.MaxChildrenPerRoot); err != nil {
		return out, err
	}
	ranking, err := claimRankingTx(ctx, tx, team)
	out.ClaimRanking = append([]string{}, ranking...)
	return out, err
}

// SetAuthorizedTeamSettings commits a policy change and its audit under current administration.
func (t *Tenant) SetAuthorizedTeamSettings(ctx context.Context, a capability.Authorized, update TeamSettingsUpdate) error {
	if update.MaxChildrenPerRoot != nil && (*update.MaxChildrenPerRoot <= 0 || *update.MaxChildrenPerRoot > limits.MaxChildrenPerRoot) {
		return &apierr.Error{Code: apierr.BadRequest, Message: "max_children_per_root must be an integer from 1 to 1000"}
	}
	reset := update.ClaimRanking != nil && slices.Equal(*update.ClaimRanking, []string{ClaimRankingDefault})
	if update.ClaimRanking != nil && !reset {
		if err := validateClaimRanking(*update.ClaimRanking); err != nil {
			return err
		}
	}
	return t.teamMutationTx(ctx, a, "SetAuthorizedTeamSettings", string(t.team), func(tx *storeTx, p capability.Principal) error {
		previous, err := teamSettingsTx(ctx, tx, t.team)
		if err != nil {
			return err
		}
		next := previous
		if update.MaxChildrenPerRoot != nil {
			next.MaxChildrenPerRoot = *update.MaxChildrenPerRoot
		}
		if update.MembersCanAddMachines != nil {
			next.MembersCanAddMachines = *update.MembersCanAddMachines
		}
		if update.ClaimRanking != nil && !reset {
			next.ClaimRanking = append([]string{}, *update.ClaimRanking...)
		}
		var stored string
		if err := tx.QueryRowContext(ctx, `SELECT claim_ranking FROM teams WHERE name=?`, t.team).Scan(&stored); err != nil {
			return err
		}
		ranking, err := json.Marshal(next.ClaimRanking)
		if err != nil {
			return err
		}
		switch {
		case update.ClaimRanking == nil:
			ranking = []byte(stored)
		case reset:
			next.ClaimRanking, ranking = append([]string{}, defaultClaimRanking...), nil
		}
		if next.MembersCanAddMachines == previous.MembersCanAddMachines && next.MaxChildrenPerRoot == previous.MaxChildrenPerRoot && string(ranking) == stored {
			return nil
		}
		closed := previous.MembersCanAddMachines && !next.MembersCanAddMachines
		if _, err := tx.execContext(ctx, `UPDATE teams SET members_can_add_machines=?,member_machines_epoch=member_machines_epoch+?,claim_ranking=?,max_children_per_root=?,updated_at=? WHERE name=?`, boolInt(next.MembersCanAddMachines), boolInt(closed), string(ranking), next.MaxChildrenPerRoot, tx.now(ctx).UnixNano(), t.team); err != nil {
			return err
		}
		id, err := newIdentityID()
		if err != nil {
			return err
		}
		toAuditRanking := func(ranking []string) []auditClaimTier {
			out := make([]auditClaimTier, len(ranking))
			for i, tier := range ranking {
				out[i] = auditClaimTier(tier)
			}
			return out
		}
		return tx.operator.recordAudit(ctx, tx, auditContext{At: tx.now(ctx), Team: t.team, Actor: auditPrincipalActor(p)}, auditTeamSettings{ReceiptID: id, MembersCanAddMachines: next.MembersCanAddMachines, PreviousMembersCanAddMachines: previous.MembersCanAddMachines, ClaimRanking: toAuditRanking(next.ClaimRanking), PreviousClaimRanking: toAuditRanking(previous.ClaimRanking), MaxChildrenPerRoot: next.MaxChildrenPerRoot, PreviousMaxChildrenPerRoot: previous.MaxChildrenPerRoot})
	})
}
