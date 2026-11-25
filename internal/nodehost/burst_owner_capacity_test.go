package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
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

type fakeMachine struct {
	mu       sync.Mutex
	capacity float64
	held     float64
	hang     bool
	polled   chan struct{}
}

func newFakeMachine(capacity, held float64) *fakeMachine {
	return &fakeMachine{capacity: capacity, held: held, polled: make(chan struct{}, 1024)}
}

func (m *fakeMachine) QueueState(ctx context.Context) (wingwire.QueueState, error) {
	m.mu.Lock()
	hang, state := m.hang, wingwire.QueueState{Resources: []wingwire.ResourceState{{Key: "cores", Capacity: m.capacity, Held: m.held, Available: max(0, m.capacity-m.held)}}}
	m.mu.Unlock()
	m.polled <- struct{}{}
	if hang {
		<-ctx.Done()
		return wingwire.QueueState{}, context.Cause(ctx)
	}
	return state, nil
}

func (m *fakeMachine) Acquire(_ context.Context, request wingwire.AdmissionRequest, _ func(wingwire.Queued)) (Lease, error) {
	cores := request.Resources.Cores
	if cores <= 0 {
		cores = 1
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.held > 0 && m.held+cores > m.capacity {
		return nil, &wingdclient.AdmissionError{Key: "capacity"}
	}
	m.held += cores
	return &fakeMachineLease{machine: m, cores: cores, done: make(chan struct{})}, nil
}

func (m *fakeMachine) free(cores float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.held -= cores
}

type fakeMachineLease struct {
	machine *fakeMachine
	cores   float64
	once    sync.Once
	done    chan struct{}
}

func (l *fakeMachineLease) Token() string { return "fake" }

func (l *fakeMachineLease) Release() error {
	l.once.Do(func() { l.machine.free(l.cores); close(l.done) })
	return nil
}

func (l *fakeMachineLease) Watch(func(wingwire.Evicted), func(wingwire.Cancel)) error {
	<-l.done
	return nil
}

type observedMachine struct {
	machineAdmitter
	polled chan struct{}
}

func (m observedMachine) QueueState(ctx context.Context) (wingwire.QueueState, error) {
	state, err := m.machineAdmitter.QueueState(ctx)
	m.polled <- struct{}{}
	return state, err
}

type burstOwner struct {
	f        *storeHostFixture
	clock    *atomic.Int64
	entry    *source.Entry
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

func newBurstOwner(t *testing.T) *burstOwner {
	t.Helper()
	b := &burstOwner{clock: &atomic.Int64{}, executed: make(chan string, 2)}
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

func (b *burstOwner) use(machine Admitter) {
	b.f.Host.executionAdmission = ComposeStartGate(b.f.Tenant, &LocalAdmission{Daemon: machine}, true)
}

func (b *burstOwner) ref(node string) runspec.NodeRef {
	return runspec.NodeRef{RunID: runspec.RunID(b.f.Input.RunID), NodeID: runspec.NodeID(node)}
}

func (b *burstOwner) execute(ctx context.Context, node string, leftReady <-chan struct{}) error {
	return b.f.Host.execute(ctx, b.f.Input, b.f.Accepted, b.entry, store.ReconcileRequest{Ref: b.ref(node), Actor: b.f.Actor, Policy: runstate.SourceNone}, localTurn{cores: 1, leftReady: leftReady})
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
	home, hold := fullMachine(t, "1.5,128MiB,ignore-external")
	machine := observedMachine{machineAdmitter: MachineAdmission(home, "v1.0.0").Daemon.(machineAdmitter), polled: make(chan struct{}, 1024)}
	state, err := machine.machineAdmitter.QueueState(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, resource := range state.Resources {
		if resource.Key == "cores" && (resource.Available <= 0 || resource.Available >= 1) {
			t.Fatalf("fixture must leave a fraction of a core free, got %+v", resource)
		}
	}
	b := newBurstOwner(t)
	b.use(machine)
	before := capacityRow(t, b.f, "job")
	done := make(chan error, 1)
	go func() { done <- b.execute(t.Context(), "job", nil) }()
	// safety: a second probe shows the owner judged the first one full and waited a poll.
	<-machine.polled
	<-machine.polled
	b.assertUnclaimed(t, before)
	if err := hold.Release(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if node := <-b.executed; node != "job" {
		t.Fatalf("executed %q after capacity returned, want job", node)
	}
}

func TestBurstHereOwnerCancelledWhileWaitingForCapacityLeavesNoClaim(t *testing.T) {
	b := newBurstOwner(t)
	before := capacityRow(t, b.f, "job")
	cancelled := &runCancelled{}
	synctest.Test(t, func(t *testing.T) {
		machine := newFakeMachine(1.5, 1)
		b.use(machine)
		ctx, cancel := context.WithCancelCause(t.Context())
		done := make(chan error, 1)
		go func() { done <- b.execute(ctx, "job", nil) }()
		<-machine.polled
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
	b := newBurstOwner(t)
	machine := newFakeMachine(1, 1)
	b.use(machine)
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
	<-machine.polled
	<-machine.polled
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
	machine.free(1)
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

func burstRun(t *testing.T, machine *fakeMachine, jobs ...runstate.NodeRow) (*ownerFixture, OwnerHost, RunInput) {
	t.Helper()
	fixture, host, input := newOwnerFixture(t, true)
	state := finishedJobReconciler{fixture}
	fixture.snapshot.Run.Spec.Request.Request.Burst = &runspec.BurstPlacement{Preferences: []runspec.PlacementStep{{Target: "here", Hold: 5 * time.Second}, {Target: "cloud"}}}
	input.Accepted.Spec, _ = json.Marshal(fixture.snapshot.Run.Spec)
	for _, job := range jobs {
		job.Ref.RunID, job.Spec, job.Status, job.SpecHash = "run", marshalEventFixture(t, nodewire.JobSpec{ID: string(job.Ref.NodeID)}), runstate.StatusPending, "job"
		fixture.snapshot.Nodes = append(fixture.snapshot.Nodes, job)
	}
	host.Store = state
	host.Decide = func(graph store.ScheduleSnapshot) store.ScheduleDecision {
		decision := store.Decide(graph)
		decision.Nodes = slices.DeleteFunc(decision.Nodes, func(node store.NodeDecision) bool { return node.Kind == store.DecisionReady })
		return decision
	}
	host.Local = func(runstate.NodeRow) bool { return true }
	host.executionAdmission = ComposeStartGate(state, &LocalAdmission{Daemon: machine}, true)
	return fixture, host, input
}

func (f *ownerFixture) finishElsewhere(node runspec.NodeID) {
	f.mu.Lock()
	for i := range f.snapshot.Nodes {
		if row := &f.snapshot.Nodes[i]; row.Ref.NodeID == node {
			row.Status, row.Outcome, row.StateSeq = runstate.StatusDone, runstate.NodeSuccess, row.StateSeq+1
		}
	}
	f.mu.Unlock()
	f.notify()
}

func (f *ownerFixture) notify() {
	select {
	case f.changed <- struct{}{}:
	default:
	}
}

func TestBurstOwnerRunFinishesWhenAnotherExecutorRanTheJobItWaitedFor(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		machine := newFakeMachine(1, 1)
		ready := time.Now()
		fixture, host, input := burstRun(t, machine, runstate.NodeRow{Ref: runspec.NodeRef{NodeID: "job"}, ReadyAt: &ready})
		host.Executor = ownerNodeFunc(func(context.Context, NodeInput) error {
			t.Error("the owner executed a job its full machine cannot fit")
			return nil
		})
		done := make(chan error, 1)
		go func() { _, err := host.Run(t.Context(), input); done <- err }()
		<-machine.polled
		synctest.Wait()
		fixture.mu.Lock()
		claimed := slices.Contains(fixture.trace, "claim")
		fixture.mu.Unlock()
		if claimed {
			t.Fatal("the owner claimed a job its full machine cannot fit")
		}
		fixture.finishElsewhere("job")
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}

func TestBurstOwnerWaitingForCapacityLeavesTheWorkerSlotToAJobThatFits(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		machine := newFakeMachine(1.5, 1)
		ready := time.Now()
		fixture, host, input := burstRun(t, machine,
			runstate.NodeRow{Ref: runspec.NodeRef{NodeID: "big"}, ReadyAt: &ready, Execution: runspec.JobExecutionSettings{Cores: 1}},
			runstate.NodeRow{Ref: runspec.NodeRef{NodeID: "small"}, Execution: runspec.JobExecutionSettings{Cores: 0.5}},
		)
		fixture.snapshot.Run.Spec.Resolved.Settings.Options.Workers.Value = 1
		input.Accepted.Spec, _ = json.Marshal(fixture.snapshot.Run.Spec)
		executed := make(chan string, 2)
		host.Executor = ownerNodeFunc(func(ctx context.Context, node NodeInput) error {
			executed <- node.NodeID
			_, err := fixture.TransitionNode(ctx, store.ReportNode{Ref: runspec.NodeRef{RunID: "run", NodeID: runspec.NodeID(node.NodeID)}, Outcome: runstate.NodeSuccess})
			return err
		})
		done := make(chan error, 1)
		go func() { _, err := host.Run(t.Context(), input); done <- err }()
		<-machine.polled
		synctest.Wait()
		fixture.mu.Lock()
		now := time.Now()
		fixture.snapshot.Nodes[1].ReadyAt, fixture.snapshot.Nodes[1].StateSeq = &now, fixture.snapshot.Nodes[1].StateSeq+1
		fixture.mu.Unlock()
		fixture.notify()
		select {
		case node := <-executed:
			if node != "small" {
				t.Fatalf("executed %q, want the job that fits", node)
			}
		case <-time.After(time.Minute):
			t.Fatal("a job the machine can fit waited behind a job waiting for capacity with --workers=1")
		}
		fixture.finishElsewhere("big")
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}

func TestBurstOwnerRunFinishesWhileTheDaemonProbeHangs(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		machine := newFakeMachine(1, 1)
		machine.hang = true
		ready := time.Now()
		fixture, host, input := burstRun(t, machine, runstate.NodeRow{Ref: runspec.NodeRef{NodeID: "job"}, ReadyAt: &ready})
		host.Executor = ownerNodeFunc(func(context.Context, NodeInput) error {
			t.Error("the owner executed a job another executor finished")
			return nil
		})
		done := make(chan error, 1)
		go func() { _, err := host.Run(t.Context(), input); done <- err }()
		<-machine.polled
		synctest.Wait()
		fixture.finishElsewhere("job")
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(waiterPoll):
			t.Fatal("a hung daemon probe kept the run from finishing after another executor finished its job")
		}
	})
}
