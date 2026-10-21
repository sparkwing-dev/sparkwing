//go:build unix

package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"sync/atomic"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestNativeAdmissionSeededRecovery(t *testing.T) {
	for seed := uint64(1); seed <= 4; seed++ {
		for _, compute := range []bool{false, true} {
			t.Run(fmt.Sprintf("seed=%d/compute=%t", seed, compute), func(t *testing.T) {
				random := rand.New(rand.NewPCG(seed, 17))
				for _, i := range random.Perm(4) {
					kind := []string{"settle", "ready", "approval", "expansion"}[i]
					t.Run(kind, func(t *testing.T) { simulateNativeRecovery(t, random, compute, kind) })
				}
			})
		}
	}
}

func simulateNativeRecovery(t *testing.T, random *rand.Rand, compute bool, kind string) {
	t.Helper()
	plan := planGrouped("global", "queue", "released")
	if kind != "settle" {
		fresh := nodeproto.JobSpec{ID: "fresh", Steps: []nodeproto.StepSpec{{ID: "body"}}}
		if kind == "approval" {
			fresh.Approval, fresh.Steps = &nodeproto.ApprovalSpec{Message: "ship?"}, nil
		}
		if kind == "expansion" {
			fresh.Closures.Generate = []string{"children"}
		}
		plan.Jobs = append(plan.Jobs, fresh)
	}
	f := newStoreHostFixture(t, plan)
	if err := (&storeAdmission{Store: f.Tenant, Owner: f.Actor}).plan(t.Context(), f.Accepted); err != nil {
		t.Fatal(err)
	}
	releaseRemote(t, f, "released")
	_, running := claimRemote(t, f, "released")
	if kind == "expansion" {
		releaseRemote(t, f, "fresh")
		_, generator := claimRemote(t, f, "fresh")
		if err := f.report(t.Context(), generator, store.NodeSuccess); err != nil {
			t.Fatal(err)
		}
	}
	// Recovery starts after capacity has expired and another run has acquired it.
	if _, err := teststore.SQL(t, f.Store).ExecContext(t.Context(), `UPDATE concurrency_holders SET lease_expires_at=1 WHERE run_id=?`, f.Input.RunID); err != nil {
		t.Fatal(err)
	}
	other := newStoreHostFixtureOn(t, f.Store, planGrouped("global", "queue", "hold"), runspec.Options{})
	if err := (&storeAdmission{Store: other.Tenant, Owner: other.Actor}).plan(t.Context(), other.Accepted); err != nil {
		t.Fatal(err)
	}
	config, _, _, _ := nativeHostFixture(t)
	config.Store, config.Owner = f.State, ownerOperationsFixture(f.Host)
	daemon := newFakeDaemon()
	var machine *LocalAdmission
	if compute {
		machine = &LocalAdmission{Daemon: daemon}
	}
	config.Admission = ComposeNativeAdmission(f.Tenant, machine, false)
	blocked := errors.New("simulation reached blocked release")
	var projected, queued atomic.Bool
	config.Admission.capacity.Queued = func(ctx context.Context, ref controllerwire.NodeRef, _ store.AcquireSlotResponse) error {
		queued.Store(true)
		if !projected.Load() {
			return errors.New("plan admission blocked reconciliation entry")
		}
		if kind == "settle" {
			return errors.New("settlement attempted plan reacquisition")
		}
		if ref.NodeID != "" {
			return fmt.Errorf("plan release gate used job award: %+v", ref)
		}
		row, err := f.Tenant.ReadNode(ctx, controllerwire.NodeRef{RunID: f.Input.RunID, NodeID: "fresh"})
		if err != nil {
			return err
		}
		if kind == "expansion" {
			if row.Expansion != store.ExpansionExpected {
				return fmt.Errorf("expansion bypassed plan gate: %+v", row)
			}
		} else if row.Status != store.StatusPending || row.ReadyAt != nil || row.ClaimedBy != "" {
			return fmt.Errorf("%s bypassed plan gate: %+v", kind, row)
		}
		if err := f.report(ctx, running, store.NodeSuccess); err != nil {
			return err
		}
		// Stop at the persisted queue boundary, before admission's polling timer.
		return blocked
	}
	owner, _, _, err := NewNativeHost(config)
	if err != nil {
		t.Fatal(err)
	}
	subscription, err := f.State.subscribe(t.Context(), f.Input.RunID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = subscription.Close() }()
	// Subscribe before simulated reports so waiting needs no catch-up SQL that
	// cancellation of a completed release could interrupt.
	owner.Wait = func(ctx context.Context, _ store.RunSnapshot) error {
		select {
		case <-subscription.Changed:
			return nil
		case <-ctx.Done():
			return context.Cause(ctx)
		}
	}
	finishBeforeRecovery := random.IntN(2) == 0
	if kind == "settle" && finishBeforeRecovery {
		if err := f.report(t.Context(), running, store.NodeSuccess); err != nil {
			t.Fatal(err)
		}
	}
	owner.Project = func(snapshot store.RunSnapshot) (store.ScheduleSnapshot, error) {
		for _, row := range snapshot.Nodes {
			if row.Ref.NodeID == "fresh" && kind != "expansion" && (row.Status != store.StatusPending || row.ReadyAt != nil || row.ClaimedBy != "") {
				return store.ScheduleSnapshot{}, fmt.Errorf("%s released while plan capacity was held by another run", kind)
			}
		}
		if !projected.Swap(true) && kind == "settle" && !finishBeforeRecovery {
			if err := f.report(t.Context(), running, store.NodeSuccess); err != nil {
				return store.ScheduleSnapshot{}, err
			}
		}
		return snapshot.Scheduling()
	}
	owner.Executor = ownerNodeFunc(func(context.Context, NodeInput) error { return errors.New("fresh execution bypassed plan gate") })
	owner.Expand = func(context.Context, EvaluationInput, store.NodeRow, StorageBinding) (json.RawMessage, *store.ExpansionFailure, error) {
		return nil, nil, errors.New("fresh expansion bypassed plan gate")
	}
	completion, err := owner.Run(t.Context(), f.Input)
	if kind == "settle" {
		if err != nil || completion.Outcome != "success" || queued.Load() {
			t.Fatalf("settlement=%+v queued=%t error=%v", completion, queued.Load(), err)
		}
	} else if !errors.Is(err, blocked) || !queued.Load() {
		t.Fatalf("%s gate: completion=%+v queued=%t error=%v", kind, completion, queued.Load(), err)
	}
	if row := capacityRow(t, f, "released"); row.Outcome != store.NodeSuccess {
		t.Fatalf("released work did not settle: %+v", row)
	}
	state, err := f.Tenant.GetConcurrencyState(t.Context(), "plan:global:deploys")
	if err != nil || state == nil || len(state.Holders) != 1 || state.Holders[0].RunID != other.Input.RunID {
		t.Fatalf("recovery changed another run's plan holder: %+v error=%v", state, err)
	}
	if live, granted, released := daemon.counts(); live != 0 || granted != 0 || released != 0 {
		t.Fatalf("recovery acquired compute: %d/%d/%d", live, granted, released)
	}
}

// ws12j #1 also describes an opposite-host box cycle. Predicate placement is
// reserved for Korey's ruling; this real-store seed pins its skip-to-SDKError path.
func TestNativePredicateAdmissionSkipKeepsOutcomeKnownWS12J(t *testing.T) {
	if os.Getenv("SPARKWING_REPRO_WS12J_PREDICATE") != "1" {
		t.Skip("ws12j #1: pending predicate-placement ruling; set SPARKWING_REPRO_WS12J_PREDICATE=1 for the known failing seed")
	}
	plan := capacityPlan("skip", 0, "job")
	plan.Jobs[0].Closures.SkipIf = []string{"guard"}
	f := newStoreHostFixture(t, plan)
	other := newStoreHostFixtureOn(t, f.Store, capacityPlan("skip", 0, "job"), runspec.Options{})
	releaseRemote(t, other, "job")
	_, holder := claimRemote(t, other, "job")
	row := capacityRow(t, other, "job")
	if err := (&storeAdmission{Store: other.Tenant, Owner: executorActor(holder)}).Node(t.Context(), other.Accepted, row, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	row = capacityRow(t, f, "job")
	decision, err := f.Tenant.AdmitCapacity(t.Context(), store.CapacityBinding{RunID: f.Input.RunID, NodeID: "job", Generation: row.Generation}, f.Actor)
	if err != nil || decision.Kind != store.AcquireSkipped {
		t.Fatalf("seed did not create capacity skip: %+v error=%v", decision, err)
	}
	_, _, deps, _ := evaluationFixture(t)
	deps.Store = f.State
	deps.EnsureEngine = func(_ context.Context, identity engine.Identity, _ releaseasset.Target) (engine.Resolved, error) {
		return engine.Resolved{Identity: identity}, nil
	}
	deps.Start = func(context.Context, EvaluationInput) (ExecutorProcess, error) {
		return &executorProcess{plan: plan, answer: func(_ context.Context, request nodeproto.Request) (any, error) {
			if request.Op == nodeproto.OpEval {
				return nodeproto.EvalResult{Value: json.RawMessage("false")}, nil
			}
			return nil, nil
		}}, nil
	}
	evaluator, err := NewOwnerEvaluator(deps)
	if err != nil {
		t.Fatal(err)
	}
	config, _, _, _ := nativeHostFixture(t)
	config.Store, config.Owner = f.State, ownerOperationsFixture(f.Host)
	config.Admission = ComposeNativeAdmission(f.Tenant, nil, false)
	config.Admission.capacity.Owner = f.Actor
	owner, _, _, err := NewNativeHost(config)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := f.Host.Acquire(t.Context(), f.Input)
	if err != nil {
		t.Fatal(err)
	}
	_, err = owner.Around(t.Context(), f.Accepted, func(ctx context.Context) (RunCompletion, error) {
		_, err := evaluator.SkipIf(ctx, ownerEvaluation(f.Input, f.Accepted, entry, row))
		return RunCompletion{}, err
	})
	var refused *AdmissionFailure
	if !errors.As(err, &refused) || refused.Outcome != store.NodeSkipped {
		var structured *apierr.Error
		var code apierr.Code
		if errors.As(err, &structured) {
			code = structured.Code
		}
		row := capacityRow(t, f, "job")
		t.Fatalf("ws12j #1 seed: admission skip lost through native Around: code=%s error=%v node_status=%s outcome=%s", code, err, row.Status, row.Outcome)
	}
}
