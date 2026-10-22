package store_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

type storeRecoveryFixture struct {
	plan          store.PlanAcceptance
	s             *store.Store
	tenant        *store.Tenant
	owner, holder store.Actor
	lease         store.SingletonLease
	ref           controllerwire.NodeRef
	target        *storetest.Target
	seq           int64
	holderBearer  string
}

func newStoreRecoveryFixture(t *testing.T, started bool) *storeRecoveryFixture {
	t.Helper()
	return newStoreRecoveryFixtureAt(t, started, storetest.NewSQLite(t))
}

func newStoreRecoveryFixtureAt(t *testing.T, started bool, target *storetest.Target, edits ...func(*nodeproto.PlanDoc)) *storeRecoveryFixture {
	return newStoreRecoveryFixtureForAt(t, started, target, string(acceptanceTeam), edits...)
}

func newStoreRecoveryFixtureForAt(t *testing.T, started bool, target *storetest.Target, identity string, edits ...func(*nodeproto.PlanDoc)) *storeRecoveryFixture {
	t.Helper()
	ctx := t.Context()
	f := &storeRecoveryFixture{target: target}
	var err error
	f.s, err = f.target.TryOpen(store.WithPolicies(sealStorePolicies))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.s.Close(); err != nil {
			t.Error(err)
		}
	})
	now := time.Now().UTC()
	f.s.SetTestClock(t, func() time.Time { return now })
	var plan store.PlanAcceptance
	f.tenant, plan, err = storePlanSealRequestFor(ctx, f.s, identity, func(_ *store.IntakeRequest, doc *nodeproto.PlanDoc) {
		doc.Jobs = []nodeproto.JobSpec{{ID: "job", Steps: []nodeproto.StepSpec{{ID: "body"}}, Retry: &nodeproto.RetrySpec{Attempts: 2}}}
		for _, edit := range edits {
			edit(doc)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.tenant.AcceptPlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	f.plan = plan
	f.owner, f.ref = plan.Actor, controllerwire.NodeRef{RunID: runspec.RunID(plan.RunID), NodeID: "job"}
	if _, err := f.tenant.TransitionNode(ctx, store.ReadyNode{Ref: f.ref, Actor: f.owner}); err != nil {
		t.Fatal(err)
	}
	authority := plan.Seal.Request.AuthorityRef
	issued, err := f.s.AsOperator().ProvisionCapability(ctx, capability.ProvisionSpec{Kind: capability.ProvisionMachine, Authority: &authority, Team: string(f.tenant.Team()), RegistrationID: "runner:" + identity + ":runner", Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "runner"}, Grants: []capability.Grant{capability.NodesClaim}, Binding: capability.Binding{Kind: capability.BindingNone}, Repositories: []string{"github.com/acme/*"}})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := f.s.AsOperator().ClaimUnit(ctx, store.UnitDemand{CapabilityPrefix: issued.Capability.Prefix, Target: &store.UnitRef{Kind: capability.UnitNode, RunID: f.ref.RunID, NodeID: f.ref.NodeID}, Available: match.Resources{Cores: 8}})
	if err != nil {
		t.Fatal(err)
	}
	f.holderBearer = string(claim.Bearer)
	f.holder = store.Actor{Kind: store.ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix}, CapabilityPrefix: claim.Award.CapabilityPrefix, NodeFence: runspec.NodeFence{RunID: runspec.RunID(claim.RunID), NodeID: runspec.NodeID(claim.NodeID), Holder: claim.Award.Holder, Generation: claim.Award.Generation}}
	row, err := f.tenant.ReadNode(ctx, f.ref)
	if err != nil {
		t.Fatal(err)
	}
	f.seq = row.StateSeq
	f.lease, err = f.s.AsOperator().AcquireSingletonLease(ctx, "controller.reaper", "recovery", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if started {
		if admitted, err := f.tenant.MayStart(ctx, store.CapacityBinding{RunID: string(f.ref.RunID), NodeID: string(f.ref.NodeID), Generation: int64(f.holder.Generation)}, f.holder); err != nil || admitted.Kind != store.AcquireGranted {
			t.Fatalf("fixture admission: %+v, %v", admitted, err)
		}
		if _, err := f.tenant.TransitionNode(ctx, store.StartNode{Ref: f.ref, Actor: f.holder, SourceAttempt: acceptanceAttempt(0)}); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f storeRecoveryFixture) expireHolder(ctx context.Context) error {
	now, err := f.s.TestOnlyDatabaseTime(ctx)
	if err != nil {
		return err
	}
	_, err = f.s.DB().ExecContext(ctx, storetest.Rebind(f.s, `UPDATE nodes SET lease_expires_at=? WHERE team=? AND run_id=? AND node_id=?`), now.UnixNano(), f.tenant.Team(), f.ref.RunID, f.ref.NodeID)
	return err
}

func (f *storeRecoveryFixture) reopen(ctx context.Context) error {
	if err := f.s.Close(); err != nil {
		return err
	}
	var err error
	f.s, err = f.target.TryOpen(store.WithPolicies(sealStorePolicies))
	if err != nil {
		return err
	}
	f.tenant, err = f.s.ForTeam(ctx, acceptanceTeam)
	return err
}

func (f storeRecoveryFixture) refuseLateReports(ctx context.Context) error {
	before, err := f.tenant.ReadNode(ctx, f.ref)
	if err != nil {
		return err
	}
	for _, actor := range []store.Actor{f.owner, f.holder} {
		change := store.ReportNode{Ref: f.ref, Actor: actor, Outcome: store.NodeSuccess}
		if actor.Kind == store.ActorHolder {
			change.SourceAttempt = acceptanceAttempt(before.Attempt)
		}
		if _, err := f.tenant.TransitionNode(ctx, change); err == nil {
			return errors.New("late holder or adopting host overwrote the job")
		}
	}
	after, err := f.tenant.ReadNode(ctx, f.ref)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(before, after) {
		return errors.New("refused late report changed persisted job")
	}
	return nil
}

func checkRecoveryEvents(ctx context.Context, f *storeRecoveryFixture, want []string) error {
	events, err := f.tenant.ReadStateEvents(ctx, string(f.ref.RunID), 0, 100)
	if err != nil {
		return err
	}
	var kinds []string
	for _, event := range events {
		if runspec.NodeID(event.NodeID) != f.ref.NodeID || event.StateSeq <= f.seq {
			continue
		}
		kinds = append(kinds, event.Kind)
		if event.StateSeq != f.seq+int64(len(kinds)) {
			return fmt.Errorf("job event sequence=%d at %s", event.StateSeq, event.Kind)
		}
	}
	if !reflect.DeepEqual(kinds, want) {
		return fmt.Errorf("job events=%v, want %v", kinds, want)
	}
	return nil
}

func checkRecoveryUnstarted(t *testing.T) error {
	ctx := t.Context()
	f := newStoreRecoveryFixture(t, false)
	ref := f.ref
	var kinds []string
	for range 5 {
		before, err := f.tenant.ReadNode(ctx, ref)
		if err != nil {
			return err
		}
		if err := f.expireHolder(ctx); err != nil {
			return err
		}
		checkStoreQueueCounts(t, f.s, map[string]int{store.QueueStateClaimed: 1})
		units, err := f.s.AsOperator().ReapUnits(ctx, f.lease, 10)
		if err != nil {
			return err
		}
		if len(units) != 1 || !units[0].Requeued || units[0].Disruptions != 0 || runspec.ClaimGeneration(units[0].Generation) != before.Generation {
			return fmt.Errorf("unstarted lapse consumed recovery budget: %+v", units)
		}
		kinds = append(kinds, "job_requeued")
		if err := f.reopen(ctx); err != nil {
			return err
		}
		checkStoreQueueCounts(t, f.s, map[string]int{store.QueueStateWaiting: 1})
		pending, err := f.tenant.ReadNode(ctx, ref)
		if err != nil {
			return err
		}
		if pending.Status != store.StatusPending || pending.ClaimedBy != "" || pending.CapabilityPrefix != "" || pending.LeaseExpiresAt != nil || pending.StartedAt != nil || pending.Attempt != 0 || pending.Disruptions != 0 || pending.ApplicationRetries != 0 || pending.Generation != before.Generation {
			return fmt.Errorf("unstarted requeue lost its execution history: %+v", pending)
		}
		if err := f.refuseLateReports(ctx); err != nil {
			return err
		}
		next, err := f.tenant.ReconcileNode(ctx, store.ReconcileRequest{Ref: ref, Actor: f.owner, Policy: store.SourceResume})
		if err != nil {
			return err
		}
		if next.Kind != store.Execute || next.Claim == nil || len(next.Bearer) == 0 || next.Node.Attempt != 0 || next.Claim.Generation != before.Generation+1 {
			return fmt.Errorf("unstarted work did not reclaim without an attempt: %+v", next)
		}
		kinds = append(kinds, "job_ready", "job_claimed")
		f.holder = store.Actor{Kind: store.ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: next.Claim.CapabilityPrefix}, CapabilityPrefix: next.Claim.CapabilityPrefix, NodeFence: runspec.NodeFence{RunID: next.Node.Ref.RunID, NodeID: next.Node.Ref.NodeID, Holder: next.Claim.Holder, Generation: next.Claim.Generation}}
	}
	if admitted, err := f.tenant.MayStart(ctx, store.CapacityBinding{RunID: string(ref.RunID), NodeID: string(ref.NodeID), Generation: int64(f.holder.Generation)}, f.holder); err != nil || admitted.Kind != store.AcquireGranted {
		return fmt.Errorf("execution admission: %+v, %v", admitted, err)
	}
	applied, err := f.tenant.TransitionNode(ctx, store.StartNode{Ref: ref, Actor: f.holder, SourceAttempt: acceptanceAttempt(0)})
	if err != nil {
		return err
	}
	if applied.Attempt != 1 || applied.Generation != f.holder.Generation {
		return fmt.Errorf("first execution after free lapses has ordinal %d generation %d", applied.Attempt, applied.Generation)
	}
	var attempts, runs int
	if err := f.s.DB().QueryRowContext(ctx, storetest.Rebind(f.s, `SELECT COUNT(*) FROM node_attempts WHERE team=? AND run_id=? AND node_id='job'`), f.tenant.Team(), f.ref.RunID).Scan(&attempts); err != nil {
		return err
	}
	if err := f.s.DB().QueryRowContext(ctx, storetest.Rebind(f.s, `SELECT COUNT(*) FROM runs WHERE team=?`), f.tenant.Team()).Scan(&runs); err != nil {
		return err
	}
	if attempts != 1 || runs != 1 {
		return fmt.Errorf("unstarted lapses created attempts=%d runs=%d", attempts, runs)
	}
	return checkRecoveryEvents(ctx, f, append(kinds, "job_started"))
}

func checkRecoveryStartedCap(t *testing.T) error {
	ctx := t.Context()
	f := newStoreRecoveryFixture(t, false)
	ref := f.ref
	var kinds []string
	for ordinal := 1; ordinal <= 3; ordinal++ {
		if admitted, err := f.tenant.MayStart(ctx, store.CapacityBinding{RunID: string(ref.RunID), NodeID: string(ref.NodeID), Generation: int64(f.holder.Generation)}, f.holder); err != nil || admitted.Kind != store.AcquireGranted {
			return fmt.Errorf("execution admission: %+v, %v", admitted, err)
		}
		started, err := f.tenant.TransitionNode(ctx, store.StartNode{Ref: ref, Actor: f.holder, SourceAttempt: acceptanceAttempt(int64(ordinal - 1))})
		if err != nil {
			return err
		}
		if int64(started.Attempt) != int64(ordinal) || started.Generation != f.holder.Generation {
			return fmt.Errorf("execution ordinal or generation reset: %+v", started)
		}
		kinds = append(kinds, "job_started")
		if err := f.expireHolder(ctx); err != nil {
			return err
		}
		units, err := f.s.AsOperator().ReapUnits(ctx, f.lease, 10)
		if err != nil {
			return err
		}
		if len(units) != 1 || units[0].Disruptions != ordinal || units[0].Requeued != (ordinal < 3) || units[0].Failed != (ordinal == 3) {
			return fmt.Errorf("started lapse policy: %+v", units)
		}
		if err := f.reopen(ctx); err != nil {
			return err
		}
		if err := f.refuseLateReports(ctx); err != nil {
			return err
		}
		row, err := f.tenant.ReadNode(ctx, ref)
		if err != nil {
			return err
		}
		if row.Attempt != runspec.AttemptOrdinal(ordinal) || row.ApplicationRetries != 0 || row.Disruptions != ordinal || row.ClaimedBy != "" || row.CapabilityPrefix != "" || row.LeaseExpiresAt != nil || row.Generation != started.Generation {
			return fmt.Errorf("lapse changed execution history or kept a claim: %+v", row)
		}
		var outcome, reason string
		var finished int64
		if err := f.s.DB().QueryRowContext(ctx, storetest.Rebind(f.s, `SELECT outcome,failure_reason,finished_at FROM node_attempts WHERE team=? AND run_id=? AND node_id='job' AND attempt=? AND generation=?`), f.tenant.Team(), f.ref.RunID, ordinal, started.Generation).Scan(&outcome, &reason, &finished); err != nil {
			return err
		}
		if outcome != "failed" || reason != "agent_lost" || finished <= 0 {
			return fmt.Errorf("lost attempt %d: %s/%s at %d", ordinal, outcome, reason, finished)
		}
		next, err := f.tenant.ReconcileNode(ctx, store.ReconcileRequest{Ref: ref, Actor: f.owner, Policy: store.SourceResume})
		if err != nil {
			return err
		}
		if ordinal == 3 {
			if next.Kind != store.KeepTerminal || next.Claim != nil || len(next.Bearer) != 0 || row.Outcome != store.NodeFailed || row.FinishedAt == nil {
				return fmt.Errorf("disruption cap allowed another execution: %+v", next)
			}
			kinds = append(kinds, "job_failed")
			continue
		}
		if next.Kind != store.Execute || next.Claim == nil || len(next.Bearer) == 0 || next.Claim.Generation != started.Generation+1 || next.Node.Attempt != runspec.AttemptOrdinal(ordinal) {
			return fmt.Errorf("started lapse did not reclaim its next generation: %+v", next)
		}
		kinds = append(kinds, "job_requeued", "job_ready", "job_claimed")
		f.holder = store.Actor{Kind: store.ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: next.Claim.CapabilityPrefix}, CapabilityPrefix: next.Claim.CapabilityPrefix, NodeFence: runspec.NodeFence{RunID: next.Node.Ref.RunID, NodeID: next.Node.Ref.NodeID, Holder: next.Claim.Holder, Generation: next.Claim.Generation}}
	}
	var attempts, runs int
	if err := f.s.DB().QueryRowContext(ctx, storetest.Rebind(f.s, `SELECT COUNT(*) FROM node_attempts WHERE team=?`), f.tenant.Team(), f.ref.RunID).Scan(&attempts); err != nil {
		return err
	}
	if err := f.s.DB().QueryRowContext(ctx, storetest.Rebind(f.s, `SELECT COUNT(*) FROM runs WHERE team=?`), f.tenant.Team()).Scan(&runs); err != nil {
		return err
	}
	if attempts != 3 || runs != 1 {
		return fmt.Errorf("started lapses created attempts=%d runs=%d", attempts, runs)
	}
	return checkRecoveryEvents(ctx, f, kinds)
}

func checkRecoveryApplicationRetry(t *testing.T) error {
	ctx := t.Context()
	f := newStoreRecoveryFixture(t, false)
	ref := f.ref
	if admitted, err := f.tenant.MayStart(ctx, store.CapacityBinding{RunID: string(ref.RunID), NodeID: string(ref.NodeID), Generation: int64(f.holder.Generation)}, f.holder); err != nil || admitted.Kind != store.AcquireGranted {
		return fmt.Errorf("execution admission: %+v, %v", admitted, err)
	}
	started, err := f.tenant.TransitionNode(ctx, store.StartNode{Ref: ref, Actor: f.holder, SourceAttempt: acceptanceAttempt(0)})
	if err != nil || started.Attempt != 1 {
		return fmt.Errorf("first execution: %+v %v", started, err)
	}
	before, err := f.tenant.ReadNode(ctx, ref)
	if err != nil {
		return err
	}
	retry := store.RetryNode{Ref: ref, Actor: f.holder, SourceAttempt: acceptanceAttempt(1), Result: &store.NodeResult{Outcome: store.NodeFailed, Reason: "verify", MaskedError: "verification failed"}}
	applied, err := f.tenant.TransitionNode(ctx, retry)
	if err != nil || applied.Attempt != 2 || applied.Generation != started.Generation {
		return fmt.Errorf("application retry: %+v %v", applied, err)
	}
	if err := f.reopen(ctx); err != nil {
		return err
	}
	if replayed, err := f.tenant.TransitionNode(ctx, retry); err != nil || !replayed.Replayed || replayed.Attempt != 2 {
		return fmt.Errorf("retry replay after reopen: %+v %v", replayed, err)
	}
	retry.SourceAttempt = acceptanceAttempt(2)
	applied, err = f.tenant.TransitionNode(ctx, retry)
	if err != nil || applied.Replayed || applied.Attempt != 3 || applied.Generation != started.Generation {
		return fmt.Errorf("second identical failure: %+v %v", applied, err)
	}
	delayed := retry
	delayed.SourceAttempt = acceptanceAttempt(1)
	if _, err := f.tenant.TransitionNode(ctx, delayed); err == nil {
		return errors.New("delayed failure changed a later attempt")
	}
	if err := f.reopen(ctx); err != nil {
		return err
	}
	after, err := f.tenant.ReadNode(ctx, ref)
	if err != nil {
		return err
	}
	if after.Status != store.StatusRunning || after.ClaimedBy != before.ClaimedBy || after.CapabilityPrefix != before.CapabilityPrefix || after.Generation != before.Generation || after.LeaseExpiresAt == nil || before.LeaseExpiresAt == nil || !after.LeaseExpiresAt.Equal(*before.LeaseExpiresAt) || after.Disruptions != 0 || after.ApplicationRetries != 2 || after.Attempt != 3 {
		return fmt.Errorf("retry changed claim continuity: %+v", after)
	}
	result := store.ReportNode{Ref: ref, Actor: f.holder, SourceAttempt: acceptanceAttempt(3), Outcome: store.NodeSuccess, Result: &store.NodeResult{Outcome: store.NodeSuccess}}
	delayedResult := result
	delayedResult.SourceAttempt = acceptanceAttempt(2)
	if _, err := f.tenant.TransitionNode(ctx, delayedResult); err == nil {
		return errors.New("delayed success finished a later attempt")
	}
	for i := range 2 {
		applied, err := f.tenant.TransitionNode(ctx, result)
		if err != nil || applied.Replayed != (i == 1) {
			return fmt.Errorf("terminal result %d: %+v %v", i, applied, err)
		}
	}
	result.Outcome, result.Result.Outcome = store.NodeFailed, store.NodeFailed
	if _, err := f.tenant.TransitionNode(ctx, result); err == nil {
		return errors.New("conflicting result changed a terminal job")
	}
	if err := f.reopen(ctx); err != nil {
		return err
	}
	view, err := f.tenant.ReadRunView(ctx, string(f.ref.RunID))
	if err != nil {
		return err
	}
	if len(view.Attempts) != 3 || len(view.Snapshot.Nodes) != 1 || view.Snapshot.Nodes[0].Outcome != store.NodeSuccess || view.Snapshot.Nodes[0].ClaimedBy != "" || view.Snapshot.Nodes[0].LeaseExpiresAt != nil {
		return fmt.Errorf("retry result did not persist: %+v", view)
	}
	for i, attempt := range view.Attempts {
		want := store.NodeFailed
		if i == 2 {
			want = store.NodeSuccess
		}
		if attempt.Identity.Attempt != int64(i+1) || attempt.Identity.Generation != int64(started.Generation) || attempt.Holder != before.ClaimedBy || attempt.CapabilityPrefix != before.CapabilityPrefix || attempt.Outcome != want || attempt.FinishedAt == nil {
			return fmt.Errorf("retry attempt %d: %+v", i, attempt)
		}
	}
	return checkRecoveryEvents(ctx, f, []string{"job_started", "job_retrying", "job_retrying", "job_succeeded"})
}

func checkRecoveryCancellation(t *testing.T) error {
	ctx := t.Context()
	f := newStoreRecoveryFixture(t, true)
	actor := store.Actor{Kind: store.ActorReaper, Owner: store.OwnerLeaseRef{Kind: store.OwnerController, Name: f.lease.Name, EvalFence: runspec.EvalFence{Holder: f.lease.Holder, Generation: runspec.ClaimGeneration(f.lease.Generation)}}}
	request := store.RunTransition{RunID: f.ref.RunID, Actor: actor, To: store.RunCancelled, Cause: store.RunCancel, Reason: "user_cancelled"}
	for range 2 {
		if _, err := f.tenant.TransitionRun(ctx, request); err != nil {
			return err
		}
	}
	row, err := f.tenant.ReadNode(ctx, f.ref)
	if err != nil {
		return err
	}
	if row.Status != store.StatusRunning || row.ClaimedBy != f.holder.Holder || row.CancelRequestedAt == nil || row.FinishedAt != nil || row.CancelReason != "user_cancelled" {
		return fmt.Errorf("cancellation stole a live holder: %+v", row)
	}
	if err := f.expireHolder(ctx); err != nil {
		return err
	}
	units, err := f.s.AsOperator().ReapUnits(ctx, f.lease, 10)
	if err != nil {
		return err
	}
	var cancelled bool
	for _, unit := range units {
		if unit.NodeID == "job" {
			cancelled = unit.Cancelled && !unit.Requeued && !unit.Failed
		}
	}
	row, err = f.tenant.ReadNode(ctx, f.ref)
	if err != nil {
		return err
	}
	if !cancelled || row.Outcome != store.NodeCancelled || row.ClaimedBy != "" || row.LeaseExpiresAt != nil || row.Disruptions != 0 || row.Attempt != 1 {
		return fmt.Errorf("loss overrode cancellation: %+v, %+v", units, row)
	}
	if err := f.reopen(ctx); err != nil {
		return err
	}
	row, err = f.tenant.ReadNode(ctx, f.ref)
	if err != nil {
		return err
	}
	if row.CancelReason != "user_cancelled" {
		return errors.New("reopening lost cancellation intent")
	}
	if err := f.refuseLateReports(ctx); err != nil {
		return err
	}
	if err := checkRecoveryEvents(ctx, f, []string{"job_started", "job_cancel_requested", "job_cancelled"}); err != nil {
		return err
	}
	var runs, attempts int
	if err := f.s.DB().QueryRowContext(ctx, storetest.Rebind(f.s, `SELECT COUNT(*) FROM runs WHERE team=?`), f.tenant.Team()).Scan(&runs); err != nil {
		return err
	}
	if err := f.s.DB().QueryRowContext(ctx, storetest.Rebind(f.s, `SELECT COUNT(*) FROM node_attempts WHERE team=? AND run_id=?`), f.tenant.Team(), f.ref.RunID).Scan(&attempts); err != nil {
		return err
	}
	if runs != 1 || attempts != 1 {
		return fmt.Errorf("cancellation created replacement work: runs=%d attempts=%d", runs, attempts)
	}
	return nil
}

func checkRecoveryReconciliation(t *testing.T, order string) error {
	ctx := t.Context()
	f := newStoreRecoveryFixture(t, true)
	request := store.ReconcileRequest{Ref: f.ref, Actor: f.owner, Policy: store.SourceResume}
	for range 2 {
		got, err := f.tenant.ReconcileNode(ctx, request)
		if err != nil {
			return err
		}
		if got.Kind != store.WaitHolder || got.Claim != nil || len(got.Bearer) != 0 || got.Node.Generation != f.holder.Generation {
			return fmt.Errorf("live or unreaped holder was adopted as a result: %+v", got)
		}
		if err := f.expireHolder(ctx); err != nil {
			return err
		}
	}
	if order == "host_first" {
		if err := f.refuseLateReports(ctx); err != nil {
			return err
		}
	}
	units, err := f.s.AsOperator().ReapUnits(ctx, f.lease, 10)
	if err != nil {
		return err
	}
	if len(units) != 1 || !units[0].Requeued || units[0].Disruptions != 1 {
		return fmt.Errorf("reaper did not requeue in the same run: %+v", units)
	}
	if err := f.reopen(ctx); err != nil {
		return err
	}
	if err := f.refuseLateReports(ctx); err != nil {
		return err
	}
	probe := request
	probe.Policy, probe.MaterializeOnly = store.SourceMemo, true
	miss, err := f.tenant.ReconcileNode(ctx, probe)
	if err != nil {
		return err
	}
	if miss.Kind != store.Dispatch || miss.Claim != nil || len(miss.Bearer) != 0 || miss.Node.Status != store.StatusPending || miss.Node.StateSeq != f.seq+2 {
		return fmt.Errorf("missing memo fabricated a result or execution award: %+v", miss)
	}
	var wg sync.WaitGroup
	results := make([]store.ReconciledNode, 2)
	failures := make([]error, 2)
	start := make(chan struct{})
	for i := range results {
		wg.Go(func() {
			<-start
			results[i], failures[i] = f.tenant.ReconcileNode(ctx, request)
		})
	}
	close(start)
	wg.Wait()
	if err := errors.Join(failures...); err != nil {
		return err
	}
	var execute, wait int
	for _, result := range results {
		switch result.Kind {
		case store.Execute:
			execute++
			if result.Claim == nil || len(result.Bearer) == 0 || result.Claim.Generation != f.holder.Generation+1 || result.Node.Disruptions != 1 || result.Node.ApplicationRetries != 0 || result.Node.Attempt != 1 {
				return fmt.Errorf("reclaim lost history or its fence: %+v", result)
			}
		case store.WaitHolder:
			wait++
			if result.Claim != nil || len(result.Bearer) != 0 {
				return errors.New("second reconciliation received execution authority")
			}
		default:
			return fmt.Errorf("requeue reconciliation returned %s", result.Kind)
		}
	}
	if execute != 1 || wait != 1 {
		return fmt.Errorf("racing reconciliation executes=%d waits=%d", execute, wait)
	}
	return checkRecoveryEvents(ctx, f, []string{"job_started", "job_requeued", "job_ready", "job_claimed"})
}

func TestStoreRecoveryUnstartedLapse(t *testing.T) {
	if err := checkRecoveryUnstarted(t); err != nil {
		t.Fatal(err)
	}
}

func TestStoreRecoveryStartedLapseCap(t *testing.T) {
	if err := checkRecoveryStartedCap(t); err != nil {
		t.Fatal(err)
	}
}

func TestStoreRecoveryApplicationRetry(t *testing.T) {
	if err := checkRecoveryApplicationRetry(t); err != nil {
		t.Fatal(err)
	}
}

func TestStoreRecoveryCancellation(t *testing.T) {
	if err := checkRecoveryCancellation(t); err != nil {
		t.Fatal(err)
	}
}

func TestStoreRecoveryReconciliation(t *testing.T) {
	for _, order := range []string{"host_first", "reaper_first"} {
		t.Run(order, func(t *testing.T) {
			if err := checkRecoveryReconciliation(t, order); err != nil {
				t.Fatal(err)
			}
		})
	}
}
