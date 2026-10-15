package acceptance

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func checkRecoveryCapacityMaintenance(ctx context.Context, root string) (err error) {
	path := filepath.Join(root, "capacity-maintenance.db")
	f, err := newStoreRecoveryFixture(ctx, path, true)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.s.Close()) }()
	if err := seedCapacityCompetitor(ctx, f.s); err != nil {
		return err
	}
	request := store.AcquireSlotRequest{Key: "deploy", HolderID: "original", RunID: "run", NodeID: "job", Capacity: 1, Cost: 1, Lease: time.Hour, Policy: store.OnLimitQueue}
	if got, err := acquireRecoveryCapacity(ctx, f, request); err != nil || got.Kind != store.AcquireGranted {
		return fmt.Errorf("original capacity: %+v, %v", got, err)
	}
	request.HolderID, request.RunID = "waiting", "competitor"
	if got, err := acquireRecoveryCapacity(ctx, f, request); err != nil || got.Kind != store.AcquireQueued {
		return fmt.Errorf("competing capacity: %+v, %v", got, err)
	}
	if _, err := f.s.DB().ExecContext(ctx, `UPDATE concurrency_holders SET lease_expires_at=1 WHERE team=? AND key='deploy' AND holder_id='original'`, store.DefaultTeam); err != nil {
		return err
	}
	forged := f.lease
	forged.Generation++
	if _, err := f.s.AsOperator().MaintainConcurrency(ctx, forged, store.ConcurrencyMaintenanceOptions{}); err == nil {
		return errors.New("forged reaper swept capacity")
	}
	result, err := f.s.AsOperator().MaintainConcurrency(ctx, f.lease, store.ConcurrencyMaintenanceOptions{})
	if err != nil || len(result.StaleHolders) != 1 || result.Promoted != 1 {
		return fmt.Errorf("elected capacity recovery: %+v, %v", result, err)
	}
	if err := f.reopen(ctx, path); err != nil {
		return err
	}
	adopted, err := f.tenant.AdoptCapacity(ctx, "deploy", "original", store.CapacityBinding{RunID: "run", NodeID: "job", Generation: 2}, f.owner)
	if err != nil || adopted.Kind != store.AcquireAgain {
		return fmt.Errorf("expired adoption: %+v, %v", adopted, err)
	}
	request.HolderID, request.RunID = "original", "run"
	if got, err := acquireRecoveryCapacity(ctx, f, request); err != nil || got.Kind != store.AcquireQueued {
		return fmt.Errorf("returning arrival: %+v, %v", got, err)
	}
	if repeated, err := f.s.AsOperator().MaintainConcurrency(ctx, f.lease, store.ConcurrencyMaintenanceOptions{}); err != nil || !reflect.DeepEqual(repeated, store.ConcurrencyMaintenanceResult{}) {
		return fmt.Errorf("repeated capacity pass: %+v, %v", repeated, err)
	}
	state, err := f.tenant.GetConcurrencyState(ctx, "deploy")
	if err != nil || state.UsedCost != 1 || len(state.Holders) != 1 || state.Holders[0].RunID != "competitor" || len(state.Waiters) != 1 || state.Waiters[0].RunID != "run" {
		return fmt.Errorf("capacity after reopening: %+v, %v", state, err)
	}
	return nil
}
