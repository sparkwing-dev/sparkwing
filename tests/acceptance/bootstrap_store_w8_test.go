package acceptance_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"

	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/runners/launcher"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func storedCloudClaimantW8(t *testing.T) (*storedEngineW8, capability.Issued) {
	t.Helper()
	expiry := time.Now().UTC().Add(time.Hour)
	return storedCloudClaimantExpiryW8(t, &expiry)
}

func storedCloudClaimantExpiryW8(t *testing.T, expiry *time.Time) (*storedEngineW8, capability.Issued) {
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
	parent := server.Bootstrap
	root, err := server.Store.AsOperator().ProvisionCapability(t.Context(), capability.ProvisionSpec{Kind: capability.ProvisionService, RegistrationID: "service:launcher", Authority: &parent, Subject: capability.Subject{Kind: capability.SubjectService, ID: "launcher"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.ClaimsLaunch}, ExpiresAt: expiry})
	if err != nil {
		t.Fatalf("provision launcher: %v", err)
	}
	tenant, err := server.Store.ForTeam(t.Context(), store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.RecordCreditGrant(t.Context(), store.CreditGrantRequest{Kind: store.CreditGrantFree, AmountMicro: 100000000, Reference: "cloud-fixture", CreatedBy: "fixture"}); err != nil {
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
	if _, err := teststore.SQL(t, server.Store).ExecContext(t.Context(), `UPDATE nodes SET execution_json=? WHERE run_id='run' AND node_id='job'`, body); err != nil {
		t.Fatal(err)
	}
	return server, root
}

func storedCloudAwardW8(t *testing.T) (*storedEngineW8, launcher.CloudJobAward) {
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
	if unit.CloudResources == nil || unit.CloudResources.Cores != 2 || unit.CloudResources.MemoryBytes != store.CPUClassMemoryBytes(2) || unit.Delivery != capability.DeliveryBootstrap || unit.ParentCapabilityExpiry == nil || unit.Engine == nil || unit.Execution == nil || unit.Execution.Resources.Value.Cores.Value != 1 || unit.Execution.Resources.Value.MemoryBytes.Value != 1<<29 {
		t.Fatal("Cloud claim lost bootstrap delivery, parent expiry, engine or execution resources")
	}
	binding := launcher.CloudJobBinding{Kind: launcher.CloudWork, UnitID: unit.UnitID, Team: unit.Request.Request.Team, RunID: runspec.RunID(unit.RunID), NodeID: runspec.NodeID(unit.JobID), Holder: unit.Binding.Holder, Generation: runspec.ClaimGeneration(unit.Binding.Generation), Attempt: runspec.AttemptOrdinal(unit.Binding.Attempt), ParentCapabilityExpiry: *unit.ParentCapabilityExpiry}
	if unit.OwnerDeadline != nil {
		binding.OwnerDeadline = *unit.OwnerDeadline
	}
	if unit.AttemptDeadline != nil {
		binding.AttemptDeadline = *unit.AttemptDeadline
	}
	projection, err := source.ForExecution(unit.Accepted.Spec)
	if err != nil {
		t.Fatal(err)
	}
	return server, launcher.CloudJobAward{LeaseExpiresAt: unit.LeaseExpiresAt, Binding: binding, Bootstrap: []byte(unit.Capability), Engine: unit.Engine, CPUCores: unit.CloudResources.Cores, Memory: unit.CloudResources.MemoryBytes, Source: projection}
}

func cloudStoreClientW8(t *testing.T, server *storedEngineW8) func(client.EngineOptions) (controllerwire.Controller, error) {
	t.Helper()
	return func(options client.EngineOptions) (controllerwire.Controller, error) {
		handler := controller.New(server.Store, nil).WithEngineController(server.EngineController).Handler()
		options.HTTP = &http.Client{Transport: surfaceTransport(func(request *http.Request) (*http.Response, error) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			return response.Result(), nil
		})}
		return client.NewEngineClient(options)
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
	executed := false
	var held controllerwire.ClaimedUnit
	hosting := bootstrapHostW8(func(ctx context.Context, unit controllerwire.ClaimedUnit) error {
		held = unit
		if unit.Capability == string(award.Bootstrap) || unit.Delivery != capability.DeliveryDirect || unit.Binding.Attempt != 0 || unit.Runner.Name != "launcher" || unit.Runner.Type != "cloud" {
			t.Fatal("Cloud exchange lost its awarded authority or identity")
		}
		principal, err := server.Store.AsOperator().LookupCapability(ctx, sha256.Sum256([]byte(unit.Capability)))
		if err != nil || principal.Capability.Issuance != capability.Exchange || !reflect.DeepEqual(principal.Capability.Parent, parent) {
			t.Fatalf("Cloud ancestry: %v", err)
		}
		if replay, err := bootstrap.Exchange(ctx); err == nil || replay.Claim.Capability != "" {
			t.Fatal("Cloud bootstrap returned a second bearer")
		}
		bound, err := storedRunnerClientW8(ctx, server, unit.Capability)
		if err != nil {
			return err
		}
		started, err := bound.TransitionJob(ctx, unit.RunID, unit.JobID, controllerwire.JobTransitionRequest{To: "running", Cause: "start", SourceAttempt: &unit.Binding.Attempt})
		if err != nil || started.Attempt != 1 {
			t.Fatalf("Cloud start: attempt=%d error=%v", started.Attempt, err)
		}
		_, err = bound.TransitionJob(ctx, unit.RunID, unit.JobID, controllerwire.JobTransitionRequest{To: "success", Cause: "report", SourceAttempt: &started.Attempt, Result: &controllerwire.JobResult{Outcome: "success"}})
		executed = err == nil
		return err
	})
	restored, err := launcher.ReadBootstrapAward(bootstrapFile)
	if err != nil {
		t.Fatal(err)
	}
	host := launcher.BootstrapHost{ControllerURL: cfg.ControllerURL, NewController: transport, Host: hosting}
	if err := host.Run(t.Context(), restored); err != nil {
		t.Fatal(err)
	}
	if !executed {
		t.Fatal("Cloud award was not completed")
	}
	if err := host.Run(t.Context(), restored); err == nil {
		t.Fatal("Cloud bootstrap was usable twice")
	}
	tenant, err := server.Store.ForTeam(t.Context(), store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	row, err := tenant.ReadNode(t.Context(), controllerwire.NodeRef{RunID: held.RunID, NodeID: held.JobID})
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
			award.Binding.RunID += runspec.RunID(text + "x")
		case 2:
			award.Binding.NodeID += runspec.NodeID(text + "x")
		case 3:
			award.Binding.Holder += text + "x"
		case 4:
			award.Binding.Generation ^= runspec.ClaimGeneration(number | 1)
		case 5:
			award.Binding.Attempt ^= runspec.AttemptOrdinal(number | 1)
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
