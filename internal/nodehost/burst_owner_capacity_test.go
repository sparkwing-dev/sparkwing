package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
)

type burstOwner struct {
	f        *storeHostFixture
	home     string
	clock    *atomic.Int64
	entry    *source.Entry
	hold     *wingdclient.Lease
	executed chan string
}

func fullMachine(t *testing.T, budget string) (string, *wingdclient.Lease) {
	t.Helper()
	home := t.TempDir()
	parsed, err := userconfig.ParseBudget(budget)
	if err != nil {
		t.Fatal(err)
	}
	daemon, err := wingd.New(wingd.Config{Home: home, Version: "v1.0.0", Budget: parsed})
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- daemon.Run(ctx) }()
	select {
	case <-daemon.Ready():
	case err := <-done:
		stop()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stop()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	connection, err := wingdclient.EnsureDaemon(ctx, wingdclient.Options{Home: home, Version: "v1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	hold, err := connection.Acquire(ctx, wingwire.AdmissionRequest{RunID: "busy-local", Resources: wingwire.HostResources{Cores: 1}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = hold.Release() })
	return home, hold
}

func newBurstOwner(t *testing.T, budget string) *burstOwner {
	t.Helper()
	b := &burstOwner{clock: &atomic.Int64{}, executed: make(chan string, 2)}
	b.home, b.hold = fullMachine(t, budget)
	request := runspec.Request{Version: 1, Team: string(store.DefaultTeam), Pipeline: "p", Mode: runspec.IntakeExecute, Origin: runspec.OriginTrigger, Dispatch: runspec.DispatchHost, Git: runspec.Git{Repository: runspec.Repository{Key: "gh:1"}}, Burst: &runspec.BurstPlacement{Preferences: []runspec.PlacementStep{{Target: "here", Hold: 5 * time.Second}, {Target: "machines", Hold: 5 * time.Second}, {Target: "cloud"}}}}
	request.Options.OwnerExecutionTimeout = runspec.Setting[time.Duration]{Optional: runspec.Optional[time.Duration]{Set: true, Value: time.Hour}}
	b.clock.Store(time.Now().UnixNano())
	b.f = newStoreHostFixtureForRequest(t, openStore(t, teststore.WithClock(func() time.Time { return time.Unix(0, b.clock.Load()) })), capacityPlan("queue", 0, "job", "recovery"), request)
	ready := b.ref("job")
	if _, err := b.f.Tenant.TransitionNode(t.Context(), store.ReadyNode{Ref: ready, Actor: b.f.Actor}); err != nil {
		t.Fatal(err)
	}
	if err := b.f.Tenant.SetOverflowLimit(t.Context(), b.f.Control.Authority, new(int64(120))); err != nil {
		t.Fatal(err)
	}
	b.f.Host.executionAdmission = ComposeStartGate(b.f.Tenant, MachineAdmission(b.home, "v1.0.0"), true)
	b.f.Host.Executor = ownerNodeFunc(func(_ context.Context, input NodeInput) error {
		b.executed <- input.NodeID
		return nil
	})
	var err error
	b.entry, err = b.f.Host.Acquire(t.Context(), b.f.Input)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := b.entry.Close(); err != nil {
			t.Error(err)
		}
	})
	return b
}

func (b *burstOwner) ref(node string) runspec.NodeRef {
	return runspec.NodeRef{RunID: runspec.RunID(b.f.Input.RunID), NodeID: runspec.NodeID(node)}
}

func (b *burstOwner) execute(ctx context.Context, node string, leftReady <-chan struct{}) error {
	return b.f.Host.execute(ctx, b.f.Input, b.f.Accepted, b.entry, store.ReconcileRequest{Ref: b.ref(node), Actor: b.f.Actor, Policy: runstate.SourceNone}, 1, leftReady)
}

func (b *burstOwner) awards(t *testing.T, node string) int {
	t.Helper()
	var count int
	err := teststore.SQL(t, b.f.Store).QueryRowContext(t.Context(), `SELECT COUNT(*) FROM capabilities WHERE issuance='award' AND bind_kind='node' AND bind_run=? AND bind_node=?`, b.f.Input.RunID, node).Scan(&count)
	if err != nil {
		t.Fatal(err)
	}
	return count
}

func (b *burstOwner) assertUnclaimed(t *testing.T, before runstate.NodeRow) {
	t.Helper()
	after := capacityRow(t, b.f, string(before.Ref.NodeID))
	if after.Generation != before.Generation || after.ClaimedBy != "" || !after.ReadyAt.Equal(*before.ReadyAt) || b.awards(t, string(before.Ref.NodeID)) != 0 {
		t.Fatalf("a here owner whose machine cannot fit the job claimed it: generation %d -> %d, claimed=%q, awards=%d", before.Generation, after.Generation, after.ClaimedBy, b.awards(t, string(before.Ref.NodeID)))
	}
	select {
	case node := <-b.executed:
		t.Fatalf("job %s executed on a machine that cannot fit it", node)
	default:
	}
}

func TestBurstHereOwnerWaitsUnclaimedWhileOnlyAFractionOfACoreIsFree(t *testing.T) {
	b := newBurstOwner(t, "1.5,128MiB,ignore-external")
	state, err := wingdclient.Query(t.Context(), wingdclient.Options{Home: b.home, Version: "v1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	for _, resource := range state.Resources {
		if resource.Key == "cores" && (resource.Available <= 0 || resource.Available >= 1) {
			t.Fatalf("fixture must leave a fraction of a core free, got %+v", resource)
		}
	}
	before := capacityRow(t, b.f, "job")
	synctest.Test(t, func(t *testing.T) {
		done := make(chan error, 1)
		go func() { done <- b.execute(t.Context(), "job", nil) }()
		for range 3 {
			synctest.Wait()
			b.assertUnclaimed(t, before)
			time.Sleep(waiterPoll)
		}
		if err := b.hold.Release(); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
	if node := <-b.executed; node != "job" {
		t.Fatalf("executed %q after capacity returned, want job", node)
	}
}

func TestBurstHereOwnerCancelledWhileWaitingForCapacityLeavesNoClaim(t *testing.T) {
	b := newBurstOwner(t, "1.5,128MiB,ignore-external")
	before := capacityRow(t, b.f, "job")
	cancelled := &runCancelled{}
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancelCause(t.Context())
		done := make(chan error, 1)
		go func() { done <- b.execute(ctx, "job", nil) }()
		synctest.Wait()
		start := time.Now()
		cancel(cancelled)
		err := <-done
		if elapsed := time.Since(start); elapsed > waiterPoll {
			t.Fatalf("cancelled wait ended after %s, want within one %s poll", elapsed, waiterPoll)
		}
		if !errors.Is(err, cancelled) {
			t.Fatalf("cancelled wait returned %v, want the cancellation cause", err)
		}
	})
	b.assertUnclaimed(t, before)
}

type runCancelled struct{}

func (*runCancelled) Error() string { return "run cancelled" }

func TestBurstHereOwnerLeavesSaturatedJobsUnclaimed(t *testing.T) {
	b := newBurstOwner(t, "1,128MiB,ignore-external")
	ctx := t.Context()
	_, member := teststore.Console(t, b.f.Store, store.DefaultTeam, "burst-requester")
	personal, err := b.f.Store.AsOperator().ProvisionCapability(ctx, capability.ProvisionSpec{Kind: capability.ProvisionMachine, Personal: true, Authority: &member.Principal.Authority, RegistrationID: "runner:" + string(store.DefaultTeam) + ":personal", Team: string(store.DefaultTeam), Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "personal"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.NodesClaim}})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.f.Store.AsOperator().RecordRunnerPoll(ctx, store.UnitDemand{CapabilityPrefix: personal.Capability.Prefix, FreeSlots: new(int), Kinds: []capability.UnitKind{capability.UnitNode}}); err != nil {
		t.Fatal(err)
	}
	before := capacityRow(t, b.f, "job")
	leftReady := make(chan struct{})
	waiting := make(chan error, 1)
	go func() { waiting <- b.execute(ctx, "job", leftReady) }()
	b.assertUnclaimed(t, before)

	b.clock.Add(int64(11 * time.Second))
	_, launcher, err := teststore.ProvisionCredential(ctx, b.f.Store, capability.ProvisionSpec{Kind: capability.ProvisionService, RegistrationID: "service:launcher", Subject: capability.Subject{Kind: capability.SubjectService, ID: "launcher"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.ClaimsLaunch}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.f.Tenant.RecordCreditGrant(ctx, store.CreditGrantRequest{Kind: store.CreditGrantFree, AmountMicro: 100000000, Reference: "burst-fixture", CreatedBy: "fixture"}); err != nil {
		t.Fatal(err)
	}
	claim, err := b.f.Store.AsOperator().ClaimUnit(ctx, store.UnitDemand{CapabilityPrefix: launcher.Prefix, Target: &store.UnitRef{Kind: capability.UnitNode, RunID: runspec.RunID(b.f.Input.RunID), NodeID: "job"}})
	if err != nil || claim.NodeID != "job" {
		t.Fatalf("Cloud did not claim after burst horizon: %+v %v", claim, err)
	}
	close(leftReady)
	held := capacityRow(t, b.f, "job")
	cloud := NodeInput{Accepted: b.f.Accepted, NodeID: "job", Generation: claim.Award.Generation, Attempt: held.Attempt, Authority: ClaimAuthority{CapabilityPrefix: claim.Award.CapabilityPrefix, Holder: claim.Award.Holder}}
	if err := b.f.report(ctx, cloud, runstate.NodeSuccess); err != nil {
		t.Fatal(err)
	}
	if err := <-waiting; err != nil {
		t.Fatal(err)
	}
	if after := capacityRow(t, b.f, "job"); after.Generation != before.Generation+1 || after.Outcome != runstate.NodeSuccess {
		t.Fatalf("the waiting owner took the job Cloud ran: generation %d -> %d, outcome %q", before.Generation, after.Generation, after.Outcome)
	}

	if _, err := b.f.Tenant.TransitionNode(ctx, store.ReadyNode{Ref: b.ref("recovery"), Actor: b.f.Actor}); err != nil {
		t.Fatal(err)
	}
	if err := b.hold.Release(); err != nil {
		t.Fatal(err)
	}
	if err := b.execute(ctx, "recovery", nil); err != nil {
		t.Fatal(err)
	}
	if node := <-b.executed; node != "recovery" {
		t.Fatalf("executed %q when capacity returned, want recovery", node)
	}
}

type finishedJobReconciler struct{ *ownerFixture }

func (s finishedJobReconciler) ReconcileNode(ctx context.Context, request store.ReconcileRequest) (runstate.ReconciledNode, error) {
	row, err := s.ReadNode(ctx, request.Ref)
	if err == nil && row.Status == runstate.StatusDone {
		return runstate.ReconciledNode{Kind: runstate.KeepTerminal, Node: row}, nil
	}
	return s.ownerFixture.ReconcileNode(ctx, request)
}

func TestBurstOwnerRunFinishesWhenAnotherExecutorRanTheJobItWaitedFor(t *testing.T) {
	home, _ := fullMachine(t, "1,128MiB,ignore-external")
	synctest.Test(t, func(t *testing.T) {
		fixture, host, input := newOwnerFixture(t, true)
		state := finishedJobReconciler{fixture}
		fixture.snapshot.Run.Spec.Request.Request.Burst = &runspec.BurstPlacement{Preferences: []runspec.PlacementStep{{Target: "here", Hold: 5 * time.Second}, {Target: "cloud"}}}
		input.Accepted.Spec, _ = json.Marshal(fixture.snapshot.Run.Spec)
		ready := time.Now()
		fixture.snapshot.Nodes = []runstate.NodeRow{{Ref: runspec.NodeRef{RunID: "run", NodeID: "job"}, Spec: marshalEventFixture(t, nodewire.JobSpec{ID: "job"}), Status: runstate.StatusPending, SpecHash: "job", ReadyAt: &ready}}
		host.Store, host.Decide = state, store.Decide
		host.Local = func(runstate.NodeRow) bool { return true }
		host.executionAdmission = ComposeStartGate(state, MachineAdmission(home, "v1.0.0"), true)
		host.Executor = ownerNodeFunc(func(context.Context, NodeInput) error {
			t.Error("the owner executed a job its full machine cannot fit")
			return nil
		})
		done := make(chan error, 1)
		go func() { _, err := host.Run(t.Context(), input); done <- err }()
		synctest.Wait()
		fixture.mu.Lock()
		claimed := slices.Contains(fixture.trace, "claim")
		row := &fixture.snapshot.Nodes[0]
		row.Status, row.Outcome, row.StateSeq = runstate.StatusDone, runstate.NodeSuccess, row.StateSeq+1
		fixture.mu.Unlock()
		if claimed {
			t.Fatal("the owner claimed a job its full machine cannot fit")
		}
		fixture.changed <- struct{}{}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}
