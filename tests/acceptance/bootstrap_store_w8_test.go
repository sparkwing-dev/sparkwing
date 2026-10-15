package acceptance_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/internal/runners/launcher"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func storedCloudClaimantW8(t *testing.T) (*controller.EngineController, capability.Issued) {
	t.Helper()
	server, machine := newStoredRunnerW8(t, capability.UnitNode)
	claimant, err := storedRunnerClientW8(t.Context(), server, machine.Raw)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := claimant.Claim(t.Context(), controllerwire.ClaimRequest{RunnerVersion: "v0.71.0", Kinds: []capability.UnitKind{capability.UnitTrigger}, AvailableCores: 2, AvailableMemoryBytes: 1 << 30})
	if err != nil || owner.Claim == nil {
		t.Fatalf("run owner acquisition: present=%v error=%v", owner.Claim != nil, err)
	}
	operator, err := server.Store.ProvisionCapability(t.Context(), capability.ProvisionSpec{Kind: capability.ProvisionBootstrap, RegistrationID: "runner-store-fixture", Subject: capability.Subject{Kind: capability.SubjectOperator, ID: "cloud-operator"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.OperatorTokens}})
	if err != nil {
		t.Fatal(err)
	}
	expiry := time.Now().UTC().Add(time.Hour)
	parent := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: operator.Capability.Prefix}
	root, err := server.Store.ProvisionCapability(t.Context(), capability.ProvisionSpec{Kind: capability.ProvisionService, RegistrationID: "runner-store-fixture", Authority: &parent, Subject: capability.Subject{Kind: capability.SubjectService, ID: "launcher"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.ClaimsLaunch}, ExpiresAt: &expiry})
	if err != nil {
		t.Fatal(err)
	}
	execution := runspec.JobExecutionSettings{Resources: runspec.Setting[runspec.Resources]{Optional: runspec.Optional[runspec.Resources]{Set: true, Value: runspec.Resources{
		Cores:       runspec.Setting[float64]{Optional: runspec.Optional[float64]{Set: true, Value: 1}},
		MemoryBytes: runspec.Setting[int64]{Optional: runspec.Optional[int64]{Set: true, Value: 1 << 29}},
	}}}}
	body, err := json.Marshal(execution)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.Store.DB().ExecContext(t.Context(), `UPDATE nodes SET execution_json=? WHERE run_id='run' AND node_id='job'`, body); err != nil {
		t.Fatal(err)
	}
	return server, root
}

func storedCloudAwardW8(t *testing.T) (*controller.EngineController, launcher.CloudJobAward) {
	t.Helper()
	server, root := storedCloudClaimantW8(t)
	cloud, err := storedRunnerClientW8(t.Context(), server, root.Raw)
	if err != nil {
		t.Fatal(err)
	}
	response, err := cloud.Claim(t.Context(), controllerwire.ClaimRequest{RunnerVersion: "v0.71.0", Kinds: []capability.UnitKind{capability.UnitNode}, RunID: "run", JobID: "job", AvailableCores: 2, AvailableMemoryBytes: 1 << 30})
	if err != nil || response.Claim == nil {
		t.Fatalf("Cloud acquisition: present=%v error=%v", response.Claim != nil, err)
	}
	unit := response.Claim
	if unit.Delivery != capability.DeliveryBootstrap || unit.ParentCapabilityExpiry == nil || unit.Engine == nil || unit.Execution == nil || unit.Execution.Resources.Value.Cores.Value != 1 || unit.Execution.Resources.Value.MemoryBytes.Value != 1<<29 {
		t.Fatal("Cloud claim lost bootstrap delivery, parent expiry, engine or execution resources")
	}
	binding := launcher.CloudJobBinding{Kind: launcher.CloudWork, UnitID: unit.UnitID, Team: unit.Request.Request.Team, RunID: unit.RunID, NodeID: unit.JobID, Holder: unit.Binding.Holder, Generation: unit.Binding.Generation, Attempt: unit.Binding.Attempt, ParentCapabilityExpiry: *unit.ParentCapabilityExpiry}
	if unit.OwnerDeadline != nil {
		binding.OwnerDeadline = *unit.OwnerDeadline
	}
	if unit.AttemptDeadline != nil {
		binding.AttemptDeadline = *unit.AttemptDeadline
	}
	return server, launcher.CloudJobAward{Binding: binding, Bootstrap: []byte(unit.Capability), Engine: unit.Engine, CPUCores: 1, Memory: 1 << 29, Source: source.Spec{RepoKey: "gh:1", RemoteURL: "https://github.com/team/repo.git", SHA: "sealed-commit", PipelineDir: ".sparkwing", Engine: unit.Engine}}
}

func cloudStoreClientW8(t *testing.T, server *controller.EngineController) func(client.EngineOptions) (controllerwire.Controller, error) {
	t.Helper()
	return func(options client.EngineOptions) (controllerwire.Controller, error) {
		bearer, err := options.Bearer(t.Context())
		if err != nil {
			return nil, err
		}
		return storedRunnerClientW8(t.Context(), server, bearer)
	}
}

func TestCloudStoreBootstrapW8Components(t *testing.T) {
	server, award := storedCloudAwardW8(t)
	bootstrap, err := storedRunnerClientW8(t.Context(), server, string(award.Bootstrap))
	if err != nil {
		t.Fatal(err)
	}
	parent := bootstrap.authorized.Principal.Capability.Parent
	if parent == nil {
		t.Fatal("Cloud bootstrap has no claimant ancestry")
	}
	_, cfg, _ := cloudFixture(launcher.CloudWork)
	resources, err := launcher.BuildJob(t.Context(), time.Now(), cfg, award, &cloudBoundaryFake{})
	if err != nil {
		t.Fatal(err)
	}
	bootstrapFile := writeBootstrapW8(t, resources.BootstrapSecret.Data)
	transport := cloudStoreClientW8(t, server)
	joined, closed, executed := false, false, false
	var held controllerwire.ClaimedUnit
	hosting := runners.Hosting{
		ControllerURL: cfg.ControllerURL, NewController: transport, Lease: time.Minute, Interval: 10 * time.Second,
		StartKeeper: func(ctx context.Context, keeper lease.Keeper) (context.Context, func()) {
			if _, err := keeper.Renew(ctx); err != nil {
				t.Fatal(err)
			}
			return ctx, func() { joined = true }
		},
		PrepareNode: func(ctx context.Context, unit controllerwire.ClaimedUnit, bound controllerwire.Controller) (nodehost.NodeHost, nodehost.NodeInput, func() error, error) {
			held = unit
			if unit.Capability == string(award.Bootstrap) || unit.Delivery != capability.DeliveryDirect || unit.Binding.Attempt != 0 || unit.Runner.Name != "launcher" || unit.Runner.Type != "cloud" {
				t.Fatal("Cloud preparation lost its exchanged authority or granted execution identity")
			}
			principal, err := server.Store.LookupCapability(ctx, sha256.Sum256([]byte(unit.Capability)))
			if err != nil || principal.Capability.Issuance != capability.Exchange || !reflect.DeepEqual(principal.Capability.Parent, parent) {
				t.Fatalf("Cloud exchange lost claimant ancestry: %v", err)
			}
			if replay, err := bootstrap.Exchange(ctx); err == nil || replay.Claim.Capability != "" {
				t.Fatal("live Cloud bootstrap returned a second bearer")
			}
			return identityNodeW8(func(input nodehost.NodeInput) {
					executed = true
					if input.Engine.Identity != *award.Engine || input.Generation != award.Binding.Generation || input.Attempt != 0 || !reflect.DeepEqual(input.GrantedRunner, unit.Runner) {
						t.Fatal("Cloud execution lost the accepted engine, runner or claim fence")
					}
					started, err := bound.TransitionJob(ctx, unit.RunID, unit.JobID, controllerwire.JobTransitionRequest{To: "running", Cause: "start"})
					if err != nil || started.Attempt != 1 {
						t.Fatalf("Cloud start: attempt=%d error=%v", started.Attempt, err)
					}
					if _, err := bound.TransitionJob(ctx, unit.RunID, unit.JobID, controllerwire.JobTransitionRequest{To: "success", Cause: "report", Result: &controllerwire.JobResult{Outcome: "success"}}); err != nil {
						t.Fatal(err)
					}
				}), runnerIdentityInputW8(unit), func() error {
					if !joined {
						t.Error("Cloud source released before keeper joined")
					}
					closed = true
					return nil
				}, nil
		},
	}
	host := launcher.BootstrapHost{ControllerURL: cfg.ControllerURL, NewController: transport, Host: hosting}
	if err := host.RunFile(t.Context(), bootstrapFile); err != nil {
		t.Fatal(err)
	}
	if !executed || !joined || !closed {
		t.Fatalf("Cloud lifecycle: executed=%v joined=%v closed=%v", executed, joined, closed)
	}
	if err := host.RunFile(t.Context(), bootstrapFile); err == nil {
		t.Fatal("Cloud bootstrap was usable twice")
	}
	tenant, err := server.Store.ForTeam(t.Context(), store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	row, err := tenant.ReadNode(t.Context(), store.NodeRef{RunID: held.RunID, NodeID: held.JobID})
	if err != nil || row.Outcome != store.NodeSuccess || row.Attempt != 1 || row.Generation != award.Binding.Generation || row.ClaimedBy != "" {
		t.Fatalf("Cloud persisted completion: outcome=%s attempt=%d generation=%d error=%v", row.Outcome, row.Attempt, row.Generation, err)
	}
}

// safety: changed claim identity, engine, deadlines or resources must never reach Cloud execution.
func FuzzCloudStoreBootstrapConfinementW8(f *testing.F) {
	const fields = 13
	for field := uint8(0); field < fields; field++ {
		f.Add(field, "forged", int64(3))
	}
	f.Fuzz(func(t *testing.T, field uint8, text string, number int64) {
		server, award := storedCloudAwardW8(t)
		switch field % fields {
		case 0:
			award.Binding.Team += text + "x"
		case 1:
			award.Binding.RunID += text + "x"
		case 2:
			award.Binding.NodeID += text + "x"
		case 3:
			award.Binding.Holder += text + "x"
		case 4:
			award.Binding.Generation ^= number | 1
		case 5:
			award.Binding.Attempt ^= number | 1
		case 6:
			award.Memory ^= number | 1
		case 7:
			award.Binding.UnitID += text + "x"
		case 8:
			award.Engine.ManifestDigest += text + "x"
		case 9:
			award.Binding.OwnerDeadline = time.Unix(number, 0)
		case 10:
			award.Binding.AttemptDeadline = time.Unix(number, 0)
		case 11:
			award.Binding.ParentCapabilityExpiry = award.Binding.ParentCapabilityExpiry.Add(time.Duration(number | 1))
		case 12:
			award.CPUCores = 1.5
		}
		called := false
		host := launcher.BootstrapHost{ControllerURL: "https://controller.example", NewController: cloudStoreClientW8(t, server), Host: bootstrapHostW8(func(context.Context, controllerwire.ClaimedUnit) error {
			called = true
			return nil
		})}
		if err := host.Run(t.Context(), award); err == nil || called {
			t.Fatalf("mutated Cloud metadata reached execution: field=%d called=%v error=%v", field%fields, called, err)
		} else if strings.Contains(err.Error(), string(award.Bootstrap)) {
			t.Fatal("Cloud refusal exposed bootstrap authority")
		}
	})
}
