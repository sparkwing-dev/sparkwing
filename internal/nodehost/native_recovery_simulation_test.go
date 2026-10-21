//go:build unix

package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync/atomic"
	"testing"

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

// The ws12j #1 seed covers both a capacity-policy skip and a true predicate.
// Both settle as skips; evaluation can only run inside its actual job award.
func TestNativePredicateAdmissionSkipKeepsOutcomeKnownWS12J(t *testing.T) {
	for _, compute := range []bool{false, true} {
		for _, scope := range []string{"global", "run", "box"} {
			for _, policySkip := range []bool{false, true} {
				if policySkip && scope == "run" {
					continue
				}
				t.Run(fmt.Sprintf("compute=%t/scope=%s/policy_skip=%t", compute, scope, policySkip), func(t *testing.T) {
					plan := capacityPlan("skip", 0, "job")
					plan.Jobs[0].Concurrency[0].Scope = new(scope)
					plan.Jobs[0].Closures.SkipIf = []string{"guard"}
					f := newStoreHostFixture(t, plan)
					var other *storeHostFixture
					if policySkip {
						other = newStoreHostFixtureOn(t, f.Store, plan, runspec.Options{})
						releaseRemote(t, other, "job")
						_, holder := claimRemote(t, other, "job")
						if _, err := other.Tenant.AdmitCapacity(t.Context(), store.CapacityBinding{RunID: holder.Accepted.RunID, NodeID: holder.NodeID, Generation: holder.Generation}, executorActor(holder)); err != nil {
							t.Fatal(err)
						}
					}
					config, _, _, _ := nativeHostFixture(t)
					config.Store, config.Owner = f.State, ownerOperationsFixture(f.Host)
					config.Evaluation.Bind = func(context.Context, EvaluationInput) (ProcessGrant, error) {
						t.Error("coordinator evaluated an executable predicate")
						return ProcessGrant{}, errors.New("coordinator predicate")
					}
					daemon := newFakeDaemon()
					var machine *LocalAdmission
					if compute {
						machine = &LocalAdmission{Daemon: daemon}
					}
					config.Admission = ComposeNativeAdmission(f.Tenant, machine, false)
					owner, _, _, err := NewNativeHost(config)
					if err != nil {
						t.Fatal(err)
					}
					_, _, deps, _ := executorFixture(t)
					deps.Store, deps.Bind = f.State, nil
					deps.EnsureEngine = func(_ context.Context, identity engine.Identity, _ releaseasset.Target) (engine.Resolved, error) {
						return engine.Resolved{Identity: identity}, nil
					}
					evaluated := 0
					deps.Start = func(ctx context.Context, input NodeInput, _ ExecutorActivity) (ExecutorProcess, error) {
						row := capacityRow(t, f, input.NodeID)
						if row.ClaimedBy != input.Authority.Holder || row.CapabilityPrefix != input.Authority.CapabilityPrefix || row.Attempt != input.Attempt || row.Status != store.StatusRunning {
							return nil, errors.New("predicate lacks actual running job award")
						}
						return &executorProcess{plan: plan, answer: func(ctx context.Context, request nodeproto.Request) (any, error) {
							if request.Op != nodeproto.OpEval {
								return nil, nil
							}
							if request.Closure != "guard" {
								return nil, errors.New("unexpected predicate")
							}
							key := fixtureJobCapacityKey("db", scope, f.Input.RunID, f.Actor.Owner.Holder)
							state, err := f.Tenant.GetConcurrencyState(ctx, key)
							if err != nil || state == nil || len(state.Holders) != 1 || state.Holders[0].RunID != f.Input.RunID || state.Holders[0].NodeID != input.NodeID {
								return nil, fmt.Errorf("predicate escaped its job groups: %+v %v", state, err)
							}
							want := 0
							if compute {
								want = 1
							}
							if live, _, _ := daemon.counts(); live != want {
								return nil, fmt.Errorf("predicate compute=%d want %d", live, want)
							}
							evaluated++
							return nodeproto.EvalResult{Value: json.RawMessage("true")}, nil
						}}, nil
					}
					deps.PrepareArtifacts = func(context.Context, NodeInput, nodeproto.JobSpec) (ExecutorFinalizer, error) {
						t.Error("skip prepared artifacts")
						return nil, errors.New("skip entered body layers")
					}
					owner.Executor, err = NewNodeExecutor(deps)
					if err != nil {
						t.Fatal(err)
					}
					completion, err := owner.Run(t.Context(), f.Input)
					row := capacityRow(t, f, "job")
					wantEvaluated := 1
					if policySkip {
						wantEvaluated = 0
					}
					if err != nil || completion.Outcome != "success" || row.Outcome != store.NodeSkipped || evaluated != wantEvaluated {
						t.Fatalf("completion=%+v error=%v row=%+v evaluations=%d", completion, err, row, evaluated)
					}
					key := fixtureJobCapacityKey("db", scope, f.Input.RunID, f.Actor.Owner.Holder)
					state, err := f.Tenant.GetConcurrencyState(t.Context(), key)
					if err != nil {
						t.Fatal(err)
					}
					wantHolders := 0
					if other != nil {
						wantHolders = 1
					}
					if state == nil || len(state.Holders) != wantHolders || len(state.Waiters) != 0 {
						t.Fatalf("skip leaked or disturbed capacity: %+v", state)
					}
					if live, granted, released := daemon.counts(); live != 0 || granted != released {
						t.Fatalf("skip leaked compute: %d/%d/%d", live, granted, released)
					}
				})
			}
		}
	}
}
