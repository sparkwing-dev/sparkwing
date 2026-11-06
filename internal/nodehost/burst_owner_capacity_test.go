package nodehost

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestBurstHereOwnerLeavesSaturatedJobsUnclaimed(t *testing.T) {
	home := t.TempDir()
	budget, err := userconfig.ParseBudget("1,128MiB,ignore-external")
	if err != nil {
		t.Fatal(err)
	}
	daemon, err := wingd.New(wingd.Config{Home: home, Version: "v1.0.0", Budget: budget})
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(t.Context())
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
	defer connection.Close()
	held, err := connection.Acquire(ctx, wingwire.AdmissionRequest{RunID: "busy-local", Resources: wingwire.HostResources{Cores: 1}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	request := runspec.Request{Version: 1, Team: string(store.DefaultTeam), Pipeline: "p", Mode: runspec.IntakeExecute, Origin: runspec.OriginTrigger, Dispatch: runspec.DispatchHost, Git: runspec.Git{Repository: runspec.Repository{Key: "gh:1"}}, Burst: &runspec.BurstPlacement{Preferences: []runspec.PlacementStep{{Target: "here", Hold: 5 * time.Second}, {Target: "machines", Hold: 5 * time.Second}, {Target: "cloud"}}}}
	request.Options.OwnerExecutionTimeout = runspec.Setting[time.Duration]{Optional: runspec.Optional[time.Duration]{Set: true, Value: time.Hour}}
	now := time.Now()
	f := newStoreHostFixtureForRequest(t, openStore(t, teststore.WithClock(func() time.Time { return now })), capacityPlan("queue", 0, "job", "recovery"), request)
	ref := runspec.NodeRef{RunID: runspec.RunID(f.Input.RunID), NodeID: "job"}
	if _, err := f.Tenant.TransitionNode(ctx, store.ReadyNode{Ref: ref, Actor: f.Actor}); err != nil {
		t.Fatal(err)
	}
	if err := f.Tenant.SetOverflowLimit(ctx, f.Control.Authority, new(int64(120))); err != nil {
		t.Fatal(err)
	}
	_, member := teststore.Console(t, f.Store, store.DefaultTeam, "burst-requester")
	personal, err := f.Store.AsOperator().ProvisionCapability(ctx, capability.ProvisionSpec{Kind: capability.ProvisionMachine, Personal: true, Authority: &member.Principal.Authority, RegistrationID: "runner:" + string(store.DefaultTeam) + ":personal", Team: string(store.DefaultTeam), Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "personal"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.NodesClaim}})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Store.AsOperator().RecordRunnerPoll(ctx, store.UnitDemand{CapabilityPrefix: personal.Capability.Prefix, FreeSlots: new(int), Kinds: []capability.UnitKind{capability.UnitNode}}); err != nil {
		t.Fatal(err)
	}
	before := capacityRow(t, f, "job")
	f.Host.executionAdmission = ComposeStartGate(f.Tenant, MachineAdmission(home, "v1.0.0"), true)
	executed := false
	f.Host.Executor = ownerNodeFunc(func(context.Context, NodeInput) error { executed = true; return nil })
	entry, err := f.Host.Acquire(ctx, f.Input)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := entry.Close(); err != nil {
			t.Error(err)
		}
	}()
	for range 3 {
		err := f.Host.execute(ctx, f.Input, f.Accepted, entry, store.ReconcileRequest{Ref: ref, Actor: f.Actor, Policy: runstate.SourceNone})
		if err != nil {
			t.Fatal(err)
		}
	}
	after := capacityRow(t, f, "job")
	if executed || after.Generation != before.Generation || after.ClaimedBy != "" || !after.ReadyAt.Equal(*before.ReadyAt) {
		t.Fatalf("saturated here owner consumed/requeued work: generation %d -> %d, claimed=%q executed=%t", before.Generation, after.Generation, after.ClaimedBy, executed)
	}

	now = now.Add(11 * time.Second)
	_, launcher, err := teststore.ProvisionCredential(ctx, f.Store, capability.ProvisionSpec{Kind: capability.ProvisionService, RegistrationID: "service:launcher", Subject: capability.Subject{Kind: capability.SubjectService, ID: "launcher"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.ClaimsLaunch}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Tenant.RecordCreditGrant(ctx, store.CreditGrantRequest{Kind: store.CreditGrantFree, AmountMicro: 100000000, Reference: "burst-fixture", CreatedBy: "fixture"}); err != nil {
		t.Fatal(err)
	}
	claim, err := f.Store.AsOperator().ClaimUnit(ctx, store.UnitDemand{CapabilityPrefix: launcher.Prefix, Target: &store.UnitRef{Kind: capability.UnitNode, RunID: ref.RunID, NodeID: ref.NodeID}})
	if err != nil || claim.NodeID != "job" {
		t.Fatalf("Cloud did not claim after burst horizon: %+v %v", claim, err)
	}
	ref.NodeID = "recovery"
	if _, err := f.Tenant.TransitionNode(ctx, store.ReadyNode{Ref: ref, Actor: f.Actor}); err != nil {
		t.Fatal(err)
	}
	if err := held.Release(); err != nil {
		t.Fatal(err)
	}
	if err := f.Host.execute(ctx, f.Input, f.Accepted, entry, store.ReconcileRequest{Ref: ref, Actor: f.Actor, Policy: runstate.SourceNone}); err != nil {
		t.Fatal(err)
	}
	if !executed {
		t.Fatal("here execution did not recover when capacity returned")
	}
}
