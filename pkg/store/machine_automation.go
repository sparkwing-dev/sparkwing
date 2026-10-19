package store

import (
	"context"
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// SetMachineAutomation lets a personal machine's owner opt it in to, or out of, the owner's automated runs.
func (t *Tenant) SetMachineAutomation(ctx context.Context, authority capability.AuthorityRef, machine string, enabled bool) error {
	if !ValidRunnerName(machine) {
		return authorityError(apierr.BadRequest, "invalid runner name")
	}
	return t.s.inTx(ctx, func(tx *storeTx) error {
		account, err := overflowAccountTx(ctx, tx, t.team, authority, capability.RunsWrite)
		if err != nil {
			return err
		}
		owned, err := ownsPersonalMachineTx(ctx, tx, t.team, account, machine)
		if err != nil {
			return err
		}
		if !owned {
			return authorityError(apierr.Forbidden, "only a personal machine's owner changes its automated runs")
		}
		if enabled {
			_, err = tx.ExecContext(ctx, `INSERT INTO machine_automation(team,machine,owner,created_at) VALUES (?,?,?,?) ON CONFLICT(team,machine) DO UPDATE SET owner=excluded.owner,created_at=excluded.created_at`, t.team, machine, account, tx.now().UnixNano())
		} else {
			_, err = tx.ExecContext(ctx, `DELETE FROM machine_automation WHERE team=? AND machine=?`, t.team, machine)
		}
		if err != nil {
			return err
		}
		id, err := newIdentityID()
		if err != nil {
			return err
		}
		return RecordBusinessEvent(tx, BusinessEvent{At: tx.now(), Team: t.team, Kind: "runner.automation_changed", SubjectID: id, Actor: account, Attrs: map[string]any{"runner": machine, "automated_runs": enabled}})
	})
}

func ownsPersonalMachineTx(ctx context.Context, tx *storeTx, team Team, account, machine string) (bool, error) {
	now := tx.now().UnixNano()
	rows, err := tx.QueryContext(ctx, `SELECT prefix FROM capabilities WHERE team=? AND subject_kind='machine' AND subject_id=? AND bind_kind='none' AND parent_id IS NULL AND created_by=? AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at>?)`, team, machine, account, now)
	if err != nil {
		return false, err
	}
	var prefixes []string
	for rows.Next() {
		var prefix string
		if err := rows.Scan(&prefix); err != nil {
			return false, errors.Join(err, rows.Close())
		}
		prefixes = append(prefixes, prefix)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return false, err
	}
	for _, prefix := range prefixes {
		record, err := loadCapabilityRecordTx(ctx, tx, prefix)
		if err != nil {
			return false, err
		}
		if record.Origin != nil && record.Origin.Personal {
			return true, nil
		}
	}
	return false, nil
}
