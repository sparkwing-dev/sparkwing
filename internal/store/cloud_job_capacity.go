package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func recordCloudCapacityClaimTx(ctx context.Context, tx *storeTx, team Team, reservation string, cores, claimed int64) error {
	_, err := tx.execContext(ctx, `INSERT INTO cloud_job_capacity(team,reservation_id,requested_class,claimed_at) VALUES (?,?,?,?)`, team, reservation, fmt.Sprintf("sparkwing-%dcpu", cores), claimed)
	return err
}

func recordCloudCapacityGrantTx(ctx context.Context, tx *storeTx, award LauncherAward, grant *capability.CloudCapacityGrant) error {
	c := award.Capability
	if c.Billing == nil || c.Billing.ExecutorClass != TierCloud {
		return nil
	}
	if grant == nil || award.CloudResources == nil || grant.RequestedClass != fmt.Sprintf("sparkwing-%dcpu", int64(award.CloudResources.Cores)) {
		return bindingRefusal()
	}
	var requested string
	var prior sql.NullString
	var waited bool
	err := tx.QueryRowContext(ctx, `SELECT requested_class,granted_class,waited_for_fresh_node FROM cloud_job_capacity WHERE team=? AND reservation_id=?`, c.Team, c.Billing.ReservationID).Scan(&requested, &prior, &waited)
	if errors.Is(err, sql.ErrNoRows) {
		return authorityError(hosterr.IntegrityFailure, "Cloud award has no capacity record")
	}
	if err != nil {
		return err
	}
	if requested != grant.RequestedClass || prior.Valid && prior.String != grant.GrantedClass {
		return bindingRefusal()
	}
	// safety: an operator change cannot invalidate a committed placement while its Job is published.
	if !prior.Valid && grant.GrantedClass != grant.RequestedClass {
		enabled, err := cloudUpsizeEnabledTx(ctx, tx)
		if err != nil {
			return err
		}
		if !enabled {
			return bindingRefusal()
		}
	}
	_, err = tx.execContext(ctx, `UPDATE cloud_job_capacity SET granted_class=?,waited_for_fresh_node=? WHERE team=? AND reservation_id=?`, grant.GrantedClass, boolInt(waited || grant.WaitedForFreshNode), c.Team, c.Billing.ReservationID)
	return err
}

func (s *Store) recordLauncherCapacityWait(ctx context.Context, authority capability.AuthorityRef, fence capability.LeaseRef) error {
	return s.inLauncherBatchTx(ctx, authority, []capability.LeaseRef{fence}, true, func(tx *storeTx) error {
		chain, err := authorityChainTx(ctx, tx, authority, true)
		if err != nil {
			return err
		}
		claimant, root := chain[0].principal, chain[len(chain)-1].principal
		if !capability.CloudLauncher(claimant.Capability) {
			return nil
		}
		row, err := tx.operator.loadAwardRowTx(ctx, tx, capability.AwardSpec{Unit: fence.Kind, UnitID: fence.ID})
		if endedLauncherPreparation(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if row.prefix == "" || row.holder != fence.Holder || row.generation != fence.Generation {
			return nil
		}
		award, live, err := readLauncherAwardTx(ctx, tx, row.prefix, claimant.Capability, root)
		if err != nil || !live {
			return err
		}
		_, err = tx.execContext(ctx, `UPDATE cloud_job_capacity SET waited_for_fresh_node=1 WHERE team=? AND reservation_id=?`, award.Capability.Team, award.Capability.Billing.ReservationID)
		return err
	})
}
