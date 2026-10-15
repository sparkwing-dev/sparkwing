package acceptance

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func checkRecoveryApplicationRetry(ctx context.Context, root string) (err error) {
	path := filepath.Join(root, "retry.db")
	f, err := newStoreRecoveryFixture(ctx, path, false)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.s.Close()) }()
	if _, err := f.s.DB().ExecContext(ctx, `UPDATE nodes SET execution_json=? WHERE team=? AND run_id='run' AND node_id='job'`, []byte(`{"retry":{"set":true,"value":2}}`), store.DefaultTeam); err != nil {
		return err
	}
	ref := store.NodeRef{RunID: "run", NodeID: "job"}
	started, err := f.tenant.TransitionNode(ctx, store.NodeTransition{Ref: ref, Actor: f.holder, To: store.NodeRunning, Cause: store.CauseStart})
	if err != nil || started.Attempt != 1 {
		return fmt.Errorf("first execution: %+v %v", started, err)
	}
	before, err := f.tenant.ReadNode(ctx, ref)
	if err != nil {
		return err
	}
	retry := store.NodeTransition{Ref: ref, Actor: f.holder, To: store.NodeRunning, Cause: store.CauseRetry, Extra: store.TransitionExtra{Result: &store.NodeResult{Outcome: store.NodeFailed, Reason: "verify", MaskedError: "verification failed"}}}
	applied, err := f.tenant.TransitionNode(ctx, retry)
	if err != nil || applied.Attempt != 2 || applied.Generation != started.Generation {
		return fmt.Errorf("application retry: %+v %v", applied, err)
	}
	if err := f.reopen(ctx, path); err != nil {
		return err
	}
	after, err := f.tenant.ReadNode(ctx, ref)
	if err != nil {
		return err
	}
	if after.Status != store.StatusRunning || after.ClaimedBy != before.ClaimedBy || after.CapabilityPrefix != before.CapabilityPrefix || after.Generation != before.Generation || after.LeaseExpiresAt == nil || before.LeaseExpiresAt == nil || !after.LeaseExpiresAt.Equal(*before.LeaseExpiresAt) || after.Disruptions != 0 || after.ApplicationRetries != 1 || after.Attempt != 2 {
		return fmt.Errorf("retry changed claim continuity: %+v", after)
	}
	result := store.NodeTransition{Ref: ref, Actor: f.holder, To: store.NodeSuccess, Cause: store.CauseReport, Extra: store.TransitionExtra{Result: &store.NodeResult{Outcome: store.NodeSuccess}}}
	for i := range 2 {
		applied, err := f.tenant.TransitionNode(ctx, result)
		if err != nil || applied.Replayed != (i == 1) {
			return fmt.Errorf("terminal result %d: %+v %v", i, applied, err)
		}
	}
	result.To, result.Extra.Result.Outcome = store.NodeFailed, store.NodeFailed
	if _, err := f.tenant.TransitionNode(ctx, result); err == nil {
		return errors.New("conflicting result changed a terminal job")
	}
	if err := f.reopen(ctx, path); err != nil {
		return err
	}
	view, err := f.tenant.ReadRunView(ctx, "run")
	if err != nil {
		return err
	}
	if len(view.Attempts) != 2 || len(view.Snapshot.Nodes) != 1 || view.Snapshot.Nodes[0].Outcome != store.NodeSuccess || view.Snapshot.Nodes[0].ClaimedBy != "" || view.Snapshot.Nodes[0].LeaseExpiresAt != nil {
		return fmt.Errorf("retry result did not persist: %+v", view)
	}
	for i, attempt := range view.Attempts {
		want := store.NodeFailed
		if i == 1 {
			want = store.NodeSuccess
		}
		if attempt.Identity.Attempt != int64(i+1) || attempt.Identity.Generation != started.Generation || attempt.Holder != before.ClaimedBy || attempt.CapabilityPrefix != before.CapabilityPrefix || attempt.Outcome != want || attempt.FinishedAt == nil {
			return fmt.Errorf("retry attempt %d: %+v", i, attempt)
		}
	}
	return checkRecoveryEvents(ctx, f, []string{"job_started", "job_retrying", "job_succeeded"})
}
