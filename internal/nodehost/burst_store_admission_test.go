package nodehost

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestBurstSharedOwnerPersistsMachineAdmissionAndSetupFailure(t *testing.T) {
	for _, refuse := range []bool{false, true} {
		t.Run(map[bool]string{false: "executes", true: "refuses-before-start"}[refuse], func(t *testing.T) {
			request := runspec.Request{
				Version: 1, Team: string(store.DefaultTeam), Pipeline: "p", Mode: runspec.IntakeExecute, Origin: runspec.OriginTrigger, Dispatch: runspec.DispatchHost,
				Git: runspec.Git{Repository: runspec.Repository{Key: "gh:1", Display: "https://github.com/acme/repo"}}, Burst: &runspec.BurstPlacement{Preferences: []runspec.PlacementStep{{Target: "here"}, {Target: "cloud"}}},
			}
			request.Options.Resources.Value.Cores.Value = 4
			request.Options.Resources.Value.Cores.Set = true
			request.Options.Resources.Set = true
			request.Options.OwnerExecutionTimeout = runspec.Setting[time.Duration]{Optional: runspec.Optional[time.Duration]{Set: true, Value: time.Hour}}
			fixture := newStoreHostFixtureForRequest(t, openStore(t), nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "job", Steps: []nodeproto.StepSpec{{ID: "body"}}}}}, request)
			daemon := newFakeDaemon()
			admitter := Admitter(daemon)
			if refuse {
				admitter = admissionAcquireFunc(func(ctx context.Context, request wingwire.AdmissionRequest, queued func(wingwire.Queued)) (Lease, error) {
					if request.SemaphoresOnly {
						return daemon.Acquire(ctx, request, queued)
					}
					row, err := fixture.Tenant.ReadNode(ctx, controllerwire.NodeRef{RunID: fixture.Input.RunID, NodeID: "job"})
					if err != nil || row.ReadyAt == nil || row.ClaimedBy != "" || row.Attempt != 0 {
						t.Errorf("machine admission consumed the claim before capacity: %+v %v", row, err)
					}
					return nil, errors.New("machine unavailable")
				})
			}
			configureNativeAdmission(&fixture.Host, &LocalAdmission{Daemon: admitter}, &storeAdmission{Store: fixture.Tenant, Owner: fixture.Actor}, false)
			executed := false
			fixture.Host.Executor = ownerNodeFunc(func(ctx context.Context, input NodeInput) error {
				executed = true
				requests := daemon.requests()
				if len(requests) != 1 || requests[0].SemaphoresOnly || requests[0].Resources.Cores != 4 {
					t.Errorf("shared owner lost per-job capacity: %+v", requests)
				}
				return fixture.report(ctx, input, store.NodeSuccess)
			})
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			completion, err := fixture.Host.Run(ctx, fixture.Input)
			want, attempts := "success", int64(1)
			if refuse {
				if err == nil || completion.Outcome != "" || executed {
					t.Fatalf("machine observation failure did not stop safely: %+v %v", completion, err)
				}
				row, readErr := fixture.Tenant.ReadNode(t.Context(), controllerwire.NodeRef{RunID: fixture.Input.RunID, NodeID: "job"})
				if readErr != nil || row.ClaimedBy != "" || row.Attempt != 0 || row.Outcome != "" {
					t.Fatalf("preclaim failure changed the job: %+v %v", row, readErr)
				}
				if live, granted, released := daemon.counts(); live != 0 || granted != released {
					t.Fatalf("preclaim failure leaked admission: %d/%d/%d", live, granted, released)
				}
				return
			}
			if err != nil || completion.Outcome != want || executed == refuse {
				t.Fatalf("shared burst owner completion=%+v executed=%v error=%v", completion, executed, err)
			}
			row, err := fixture.Tenant.ReadNode(t.Context(), controllerwire.NodeRef{RunID: fixture.Input.RunID, NodeID: "job"})
			if err != nil || row.Outcome != store.NodeState(want) || row.Attempt != attempts || row.ClaimedBy != "" {
				t.Fatalf("shared burst owner persisted the wrong result: %+v %v", row, err)
			}
			if live, granted, released := daemon.counts(); live != 0 || granted != released {
				t.Fatalf("shared burst owner leaked machine admission: %d/%d/%d", live, granted, released)
			}
		})
	}
}

func TestBurstSharedOwnerKeepsSaturatedWorkClaimableByAnotherMachine(t *testing.T) {
	burstSharedOwnerContention(t, false)
}

func TestBurstSharedOwnerContentionAfterPlacementLeavesJobUnclaimed(t *testing.T) {
	burstSharedOwnerContention(t, true)
}

func burstSharedOwnerContention(t *testing.T, optimistic bool) {
	burstSharedOwnerContentionResources(t, optimistic, 4, 4096)
}

func FuzzBurstMachineWaitNeverConsumesClaim(f *testing.F) {
	f.Add(uint8(4), uint32(4096))
	f.Add(uint8(1), uint32(0))
	f.Fuzz(func(t *testing.T, cpu uint8, memory uint32) {
		burstSharedOwnerContentionResources(t, true, 0.25+float64(cpu%32)/4, int64(memory))
	})
}

func burstSharedOwnerContentionResources(t *testing.T, optimistic bool, cores float64, memory int64) {
	t.Helper()
	request := runspec.Request{
		Version: 1, Team: string(store.DefaultTeam), Pipeline: "p", Mode: runspec.IntakeExecute, Origin: runspec.OriginTrigger, Dispatch: runspec.DispatchHost,
		Git: runspec.Git{Repository: runspec.Repository{Key: "gh:1", Display: "https://github.com/acme/repo"}}, Burst: &runspec.BurstPlacement{Preferences: []runspec.PlacementStep{{Target: "here"}, {Target: "name=desktop"}}},
	}
	request.Options.Resources.Set = true
	request.Options.Resources.Value.Cores.Set = true
	request.Options.Resources.Value.Cores.Value = cores
	request.Options.Resources.Value.MemoryBytes.Set = memory > 0
	request.Options.Resources.Value.MemoryBytes.Value = memory
	request.Options.OwnerExecutionTimeout = runspec.Setting[time.Duration]{Optional: runspec.Optional[time.Duration]{Set: true, Value: time.Hour}}
	plan := capacityPlan("queue", 0, "job")
	plan.Jobs[0].Concurrency[0].Scope = new("run")
	fixture := newStoreHostFixtureForRequest(t, openStore(t), plan, request)
	if !optimistic {
		if err := fixture.Store.AsOperator().RecordHereAvailability(t.Context(), fixture.Actor.Authority, match.Resources{}); err != nil {
			t.Fatal(err)
		}
	}
	requester, err := fixture.Store.AsOperator().ResolveAuthority(t.Context(), fixture.Control.Authority)
	if err != nil {
		t.Fatal(err)
	}
	if requester.Capability.Parent == nil || requester.Capability.Parent.Kind != capability.AuthoritySession {
		t.Fatal("burst requester has no account session")
	}
	issued, err := fixture.Store.AsOperator().ProvisionCapability(t.Context(), capability.ProvisionSpec{Kind: capability.ProvisionMachine, Personal: true, Repositories: []string{"github.com/acme/*"}, Authority: requester.Capability.Parent, RegistrationID: "runner:default:desktop", Team: string(store.DefaultTeam), Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "desktop"}, Grants: []capability.Grant{capability.NodesClaim}, Binding: capability.Binding{Kind: capability.BindingNone}})
	if err != nil {
		t.Fatal(err)
	}
	key := fixtureJobCapacityKey("db", "run", fixture.Input.RunID, "")
	daemon := newFakeDaemon()
	daemon.hostGate = make(chan struct{})
	admitter := admissionAcquireFunc(func(ctx context.Context, request wingwire.AdmissionRequest, queued func(wingwire.Queued)) (Lease, error) {
		capacity, err := fixture.Tenant.GetConcurrencyState(context.WithoutCancel(ctx), key)
		if err != nil || len(capacity.Holders) != 1 || capacity.Holders[0].NodeID != "job" {
			t.Errorf("machine admission preceded logical capacity: %+v %v", capacity, err)
		}
		return daemon.Acquire(ctx, request, queued)
	})
	configureNativeAdmission(&fixture.Host, &LocalAdmission{Daemon: admitter}, &storeAdmission{Store: fixture.Tenant, Owner: fixture.Actor}, false)
	fixture.Host.Executor = ownerNodeFunc(func(context.Context, NodeInput) error { t.Error("saturated host executed work"); return nil })
	ready := make(chan string, 1)
	wait := fixture.Host.Wait
	fixture.Host.Wait = func(ctx context.Context, snapshot store.RunSnapshot) error {
		if snapshot.Nodes[0].ReadyAt != nil {
			select {
			case ready <- "ready":
			default:
			}
		}
		return wait(ctx, snapshot)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	done := make(chan error, 1)
	go func() {
		completion, err := fixture.Host.Run(ctx, fixture.Input)
		if err == nil && completion.Outcome != "success" {
			err = errors.New("overflow run did not succeed")
		}
		done <- err
	}()
	joined := false
	defer func() {
		cancel()
		if !joined {
			<-done
		}
	}()
	if optimistic {
		ready = daemon.queued
	}
	select {
	case <-ready:
	case err := <-done:
		joined = true
		t.Fatalf("owner stopped before overflow readiness: %v", err)
	case <-ctx.Done():
		t.Fatal("saturated machine blocked overflow readiness")
	}
	rowBefore, err := fixture.Tenant.ReadNode(ctx, controllerwire.NodeRef{RunID: fixture.Input.RunID, NodeID: "job"})
	if err != nil || rowBefore.ClaimedBy != "" || rowBefore.Attempt != 0 {
		t.Fatalf("waiting for machine capacity consumed the overflow claim: %+v %v", rowBefore, err)
	}

	if live, _, _ := daemon.counts(); live != 0 {
		t.Fatalf("ready burst retained extra machine admission: live=%d", live)
	}
	claim, err := fixture.Store.AsOperator().ClaimUnit(ctx, store.UnitDemand{CapabilityPrefix: issued.Capability.Prefix, Target: &store.UnitRef{Kind: capability.UnitNode, RunID: fixture.Input.RunID, NodeID: "job"}, Available: match.Resources{Cores: 64, MemoryBytes: 64 << 30}})
	if err != nil || claim.Kind != capability.UnitNode {
		t.Fatalf("saturated owner's work was not claimable: %+v %v", claim, err)
	}
	row, err := fixture.Tenant.ReadNode(ctx, controllerwire.NodeRef{RunID: fixture.Input.RunID, NodeID: "job"})
	if err != nil {
		t.Fatal(err)
	}
	entry, err := fixture.Host.Acquire(ctx, fixture.Input)
	if err != nil {
		t.Fatal(err)
	}
	defer entry.Close()
	input, err := NewNodeInput(fixture.Accepted, row, entry.Engine, nodeproto.RunnerIdentity{Name: "desktop", Type: "agent", Labels: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	admission := ComposeNativeAdmission(fixture.Tenant, nil, false)
	if err := admission.claimedNode(ctx, input, func(ctx context.Context) error {
		state, err := fixture.Tenant.GetConcurrencyState(ctx, key)
		if err != nil || len(state.Holders) != 1 || state.Holders[0].NodeID != "job" {
			t.Errorf("claimed burst lost concurrency: %+v %v", state, err)
		}
		return fixture.report(ctx, input, store.NodeSuccess)
	}); err != nil {
		t.Fatalf("remote claimed execution: %v", err)
	}
	err = <-done
	joined = true
	if err != nil {
		t.Fatalf("owner completion after remote execution: %v", err)
	}

	if live, granted, released := daemon.counts(); live != 0 || granted != released {
		t.Fatalf("overflow retained local admission: %d/%d/%d", live, granted, released)
	}
}
