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
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestStartGateSeededRecovery(t *testing.T) {
	for seed := uint64(1); seed <= 8; seed++ {
		for _, compute := range []bool{false, true} {
			t.Run(fmt.Sprintf("seed=%d/compute=%t", seed, compute), func(t *testing.T) {
				simulateNativeRecovery(t, rand.New(rand.NewPCG(seed, 17)), compute)
			})
		}
	}
}

func simulateNativeRecovery(t *testing.T, random *rand.Rand, compute bool) {
	t.Helper()
	plan := planGrouped("global", "queue", "released")
	fresh := []nodeproto.JobSpec{
		{ID: "ready", Steps: []nodeproto.StepSpec{{ID: "body"}}},
		{ID: "approval", Approval: &nodeproto.ApprovalSpec{Message: "ship?"}},
		{ID: "expansion", Steps: []nodeproto.StepSpec{{ID: "body"}}, Closures: nodeproto.ClosureSpec{Generate: []string{"children"}}},
	}
	for _, i := range random.Perm(len(fresh)) {
		plan.Jobs = append(plan.Jobs, fresh[i])
	}
	f := newStoreHostFixture(t, plan)
	releaseRemote(t, f, "released")
	_, running := claimRemote(t, f, "released")
	releaseRemote(t, f, "expansion")
	_, generator := claimRemote(t, f, "expansion")
	if err := f.report(t.Context(), generator, runstate.NodeSuccess); err != nil {
		t.Fatal(err)
	}
	if _, err := teststore.SQL(t, f.Store).ExecContext(t.Context(), `UPDATE concurrency_holders SET lease_expires_at=1 WHERE run_id=? AND node_id=''`, f.Input.RunID); err != nil {
		t.Fatal(err)
	}
	other := newStoreHostFixtureOn(t, f.Store, planGrouped("global", "queue", "hold"), runspec.Options{})
	releaseRemote(t, other, "hold")
	config, _, _, _ := nativeHostFixture(t)
	config.Store, config.Owner = f.State, ownerOperationsFixture(f.Host)
	config.Owner.Local = func(row runstate.NodeRow) bool { return row.Ref.NodeID == "ready" }
	daemon := newFakeDaemon()
	var machine *LocalAdmission
	if compute {
		machine = &LocalAdmission{Daemon: daemon}
	}
	config.Admission = ComposeStartGate(f.Tenant, machine, false)
	owner, _, _, err := NewNativeHost(config)
	if err != nil {
		t.Fatal(err)
	}
	subscription, err := f.State.subscribe(t.Context(), f.Input.RunID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = subscription.Close() }()
	var otherHeld, releasedDone, queued, expanded, approved atomic.Bool
	otherHeld.Store(true)
	if random.IntN(2) == 0 {
		if err := f.report(t.Context(), running, runstate.NodeSuccess); err != nil {
			t.Fatal(err)
		}
		releasedDone.Store(true)
	}
	owner.Project = func(snapshot store.RunSnapshot) (store.ScheduleSnapshot, error) {
		if otherHeld.Load() {
			for _, row := range snapshot.Nodes {
				if row.Ref.NodeID == "ready" || row.Ref.NodeID == "approval" {
					if row.Status != runstate.StatusPending || row.ReadyAt != nil || row.ClaimedBy != "" {
						return store.ScheduleSnapshot{}, fmt.Errorf("fresh work escaped lost plan: %+v", row)
					}
				}
				if row.Ref.NodeID == "expansion" && row.Expansion != runstate.ExpansionExpected {
					return store.ScheduleSnapshot{}, errors.New("expansion escaped lost plan")
				}
			}
			state, err := f.Tenant.GetConcurrencyState(t.Context(), "plan:global:deploys")
			if err != nil {
				return store.ScheduleSnapshot{}, err
			}
			for _, waiter := range state.Waiters {
				if waiter.RunID == f.Input.RunID {
					queued.Store(true)
				}
			}
		}
		return snapshot.Scheduling()
	}
	owner.Wait = func(ctx context.Context, snapshot store.RunSnapshot) error {
		if otherHeld.Load() {
			state, err := f.Tenant.GetConcurrencyState(ctx, "plan:global:deploys")
			if err != nil {
				return err
			}
			for _, waiter := range state.Waiters {
				if waiter.RunID == f.Input.RunID {
					queued.Store(true)
				}
			}
		}
		if !releasedDone.Swap(true) {
			return f.report(t.Context(), running, runstate.NodeSuccess)
		}
		if otherHeld.Load() && queued.Load() {
			if _, err := other.Tenant.TransitionNode(t.Context(), store.CancelNode{Ref: runspec.NodeRef{RunID: runspec.RunID(other.Input.RunID), NodeID: "hold"}, Actor: other.Actor}); err != nil {
				return fmt.Errorf("cancel competitor: %w", err)
			}
			if _, err := other.Tenant.FinishRunAndUnit(t.Context(), runstate.FinishRunRequest{Unit: other.Actor.Owner, Transition: runstate.RunCompletion{RunID: runspec.RunID(other.Input.RunID), Actor: other.Actor, Cause: runstate.RunFinish}}); err != nil {
				return fmt.Errorf("finish competitor: %w", err)
			}
			otherHeld.Store(false)
			return nil
		}
		if row, ok := ownerNode(snapshot, "approval"); ok && row.Status == runstate.StatusApprovalPending {
			if _, err := f.State.TransitionNode(t.Context(), store.ResolveApprovalNode{Ref: row.Ref, Actor: f.Control, Outcome: runstate.NodeSuccess, Approval: &runstate.ApprovalResolution{Decision: store.ApprovalResolutionApproved}}); err != nil {
				return err
			}
			approved.Store(true)
			return nil
		}
		select {
		case <-subscription.Changed:
			return nil
		case <-ctx.Done():
			return context.Cause(ctx)
		}
	}
	owner.Executor = ownerNodeFunc(func(ctx context.Context, input NodeInput) error {
		if otherHeld.Load() {
			return errors.New("execution bypassed plan reacquisition")
		}
		return f.report(ctx, input, runstate.NodeSuccess)
	})
	owner.Expand = func(context.Context, EvaluationInput, runstate.NodeRow, StorageBinding) (json.RawMessage, runspec.SecretRegistration, *store.ExpansionFailure, error) {
		if otherHeld.Load() {
			return nil, runspec.SecretRegistration{}, nil, errors.New("expansion bypassed plan reacquisition")
		}
		expanded.Store(true)
		return json.RawMessage(`{"jobs":[]}`), runspec.SecretRegistration{Version: 1}, nil, nil
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	completion, err := owner.Run(ctx, f.Input)
	if err != nil || completion.Outcome != "success" || !queued.Load() || !expanded.Load() || !approved.Load() {
		t.Fatalf("completion=%+v queued=%t expanded=%t approved=%t error=%v", completion, queued.Load(), expanded.Load(), approved.Load(), err)
	}
	for _, id := range []string{"released", "ready", "approval", "expansion"} {
		if row := capacityRow(t, f, id); row.Outcome != runstate.NodeSuccess {
			t.Fatalf("unsettled %s: %+v", id, row)
		}
	}
	state, err := f.Tenant.GetConcurrencyState(t.Context(), "plan:global:deploys")
	if err != nil || state.UsedCost != 0 || len(state.Waiters) != 0 {
		t.Fatalf("settled run retained plan: %+v %v", state, err)
	}
	if live, granted, released := daemon.counts(); live != 0 || granted != released {
		t.Fatalf("compute leaked: %d/%d/%d", live, granted, released)
	}
}

// A capacity-policy skip and a true predicate both settle as skips.
// Predicate evaluation belongs to the actual execution award.
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
						if _, err := other.Tenant.MayStart(t.Context(), runstate.CapacityBinding{RunID: holder.Accepted.RunID, NodeID: holder.NodeID, Generation: int64(holder.Generation)}, executorActor(holder)); err != nil {
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
					config.Admission = ComposeStartGate(f.Tenant, machine, false)
					owner, _, _, err := NewNativeHost(config)
					if err != nil {
						t.Fatal(err)
					}
					_, _, deps, _ := executorFixture(t)
					deps.Store, deps.Bind = f.State, nil
					deps.EnsureEngine = func(_ context.Context, identity engineid.Identity, _ releaseasset.Target) (engine.Resolved, error) {
						return engine.Resolved{Identity: identity}, nil
					}
					evaluated := 0
					deps.Start = func(ctx context.Context, input NodeInput, _ ExecutorActivity) (ExecutorProcess, error) {
						row := capacityRow(t, f, input.NodeID)
						if row.ClaimedBy != input.Authority.Holder || row.CapabilityPrefix != input.Authority.CapabilityPrefix || row.Attempt != input.Attempt || row.Status != runstate.StatusRunning {
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
					if err != nil || completion.Outcome != "success" || row.Outcome != runstate.NodeSkipped || evaluated != wantEvaluated {
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

func TestRecoveredOwnerSettlesWithoutPlanReacquisition(t *testing.T) {
	for _, compute := range []bool{false, true} {
		t.Run(fmt.Sprintf("compute=%t", compute), func(t *testing.T) {
			f := newStoreHostFixture(t, planGrouped("global", "queue", "released"))
			releaseRemote(t, f, "released")
			_, input := claimRemote(t, f, "released")
			if err := f.report(t.Context(), input, runstate.NodeSuccess); err != nil {
				t.Fatal(err)
			}
			if _, err := teststore.SQL(t, f.Store).ExecContext(t.Context(), `UPDATE concurrency_holders SET lease_expires_at=1 WHERE run_id=? AND node_id=''`, f.Input.RunID); err != nil {
				t.Fatal(err)
			}
			other := newStoreHostFixtureOn(t, f.Store, planGrouped("global", "queue", "hold"), runspec.Options{})
			releaseRemote(t, other, "hold")
			config, _, _, _ := nativeHostFixture(t)
			config.Store, config.Owner = f.State, ownerOperationsFixture(f.Host)
			var machine *LocalAdmission
			if compute {
				machine = &LocalAdmission{Daemon: newFakeDaemon()}
			}
			config.Admission = ComposeStartGate(f.Tenant, machine, false)
			owner, _, _, err := NewNativeHost(config)
			if err != nil {
				t.Fatal(err)
			}
			owner.Executor = ownerNodeFunc(func(context.Context, NodeInput) error { t.Fatal("settled work executed again"); return nil })
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			completion, err := owner.Run(ctx, f.Input)
			if err != nil || completion.Outcome != "success" {
				t.Fatalf("settlement waited on plan: %+v %v", completion, err)
			}
			state, err := f.Tenant.GetConcurrencyState(t.Context(), "plan:global:deploys")
			if err != nil || state.UsedCost != 1 || len(state.Waiters) != 0 {
				t.Fatalf("settlement disturbed another plan: %+v %v", state, err)
			}
		})
	}
}
