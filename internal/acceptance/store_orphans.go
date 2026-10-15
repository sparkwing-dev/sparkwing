package acceptance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func checkRecoveryLocalOrphans(ctx context.Context, root string) error {
	for _, started := range []bool{false, true} {
		if err := checkRecoveryLocalOrphan(ctx, root, started); err != nil {
			return fmt.Errorf("started=%t: %w", started, err)
		}
	}
	return nil
}

func checkRecoveryLocalOrphan(ctx context.Context, root string, started bool) (err error) {
	path := filepath.Join(root, fmt.Sprintf("local-%t.db", started))
	f, err := newStoreRecoveryFixture(ctx, path, false)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.s.Close()) }()
	if _, err := f.s.DB().ExecContext(ctx, `DELETE FROM nodes WHERE team=? AND run_id='run'`, store.DefaultTeam); err != nil {
		return err
	}
	if _, err := f.s.DB().ExecContext(ctx, `UPDATE runs SET request_json=? WHERE team=? AND id='run'`, []byte(`{"version":1,"request":{"origin":"local"}}`), store.DefaultTeam); err != nil {
		return err
	}
	var start any
	if started {
		start = int64(1)
	}
	if _, err := f.s.DB().ExecContext(ctx, `UPDATE triggers SET started_at=?,lease_expires_at=1 WHERE team=? AND id='run'`, start, store.DefaultTeam); err != nil {
		return err
	}
	for pass := range 2 {
		units, err := f.s.ReapUnits(ctx, f.lease, 10)
		if err != nil {
			return err
		}
		if pass == 1 {
			if len(units) != 0 {
				return fmt.Errorf("repeated orphan pass changed work: %+v", units)
			}
			continue
		}
		if len(units) != 1 || units[0].RunID != "run" || units[0].Kind != "trigger" || units[0].Generation != 1 || units[0].Failed != started || units[0].Requeued == started || units[0].Cancelled {
			return fmt.Errorf("orphan recovery: %+v", units)
		}
	}
	if err := f.reopen(ctx, path); err != nil {
		return err
	}
	return checkLocalOrphanState(ctx, f, started)
}

func checkLocalOrphanState(ctx context.Context, f storeRecoveryFixture, started bool) error {
	run, err := f.tenant.ReadRun(ctx, "run")
	if err != nil {
		return err
	}
	wantState, wantOwner, wantEvent, disruptions := store.RunRunning, "pending", "run_requeued", 0
	if started {
		wantState, wantOwner, wantEvent, disruptions = store.RunFailed, "failed", "run_failed", 1
	}
	if run.State != wantState || (run.FinishedAt != nil) != started || run.Number != 1 {
		return fmt.Errorf("reopened local run: state=%s number=%d finished=%v", run.State, run.Number, run.FinishedAt)
	}
	var status string
	var lost int
	var generation int64
	var holder sql.NullString
	var lease, execution sql.NullInt64
	if err := f.s.DB().QueryRowContext(ctx, `SELECT status,generation,disruptions,claimed_by,lease_expires_at,started_at FROM triggers WHERE team=? AND id='run'`, store.DefaultTeam).Scan(&status, &generation, &lost, &holder, &lease, &execution); err != nil {
		return err
	}
	if status != wantOwner || generation != 1 || lost != disruptions || holder.Valid || lease.Valid || execution.Valid != started {
		return fmt.Errorf("reopened owner: status=%s generation=%d disruptions=%d holder=%v lease=%v started=%v", status, generation, lost, holder, lease, execution)
	}
	events, err := f.tenant.ReadStateEvents(ctx, "run", 0, 100)
	if err != nil {
		return err
	}
	if len(events) != 1 || events[0].Kind != wantEvent || events[0].NodeID != "" || events[0].StateSeq != run.StateSeq {
		return fmt.Errorf("orphan events: %+v", events)
	}
	for _, table := range []string{"runs", "triggers"} {
		var count int
		if err := f.s.DB().QueryRowContext(ctx, `SELECT count(*) FROM `+table+` WHERE team=?`, store.DefaultTeam).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			return fmt.Errorf("%s count=%d, want one original row", table, count)
		}
	}
	return nil
}
