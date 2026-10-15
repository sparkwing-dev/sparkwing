package controller

import (
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestEngineLauncherSyncUsesStoreSnapshot(t *testing.T) {
	e, tenant := engineRuntimeStore(t)
	if _, err := tenant.RecordCreditGrant(t.Context(), store.CreditGrantRequest{Kind: store.CreditGrantFree, AmountMicro: 100_000_000, Reference: "launcher-test", CreatedBy: "fixture"}); err != nil {
		t.Fatal(err)
	}
	raw := engineHTTPRoot(t, e, "", "launcher", []capability.Grant{capability.ClaimsLaunch})
	engineSQL(t, e.Store, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES ('a','run','job','pending',1,?,?)`, []byte(`{"id":"job"}`), []byte(`{}`))
	claim, err := e.Store.ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: "launcher", Target: &store.UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: "job"}, Capacity: match.Resources{Cores: 1}, Available: match.Resources{Cores: 1}})
	if err != nil {
		t.Fatal(err)
	}
	h := New(e.Store, slog.New(slog.NewTextHandler(io.Discard, nil))).WithEngineController(e).Handler()
	observation := controllerwire.LauncherObservation{Award: capability.LeaseRef{Kind: capability.UnitNode, Holder: claim.Award.Holder, Generation: claim.Award.Generation}, Job: controllerwire.LauncherJobRef{Namespace: "ci", Name: "job", UID: "old-job-uid"}, NodeUID: "node-uid"}
	principal, err := e.Store.ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix})
	if err != nil {
		t.Fatal(err)
	}
	observation.Award.ID = principal.Capability.UnitID
	request := func(o controllerwire.LauncherObservation) controllerwire.LauncherSyncResponse {
		t.Helper()
		body, err := json.Marshal(controllerwire.LauncherSyncRequest{Observed: []controllerwire.LauncherObservation{o}})
		if err != nil {
			t.Fatal(err)
		}
		w := engineClaimHTTP(t, h, raw, "POST", "/api/v1/launcher/sync", string(body), 200)
		var response controllerwire.LauncherSyncResponse
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response
	}
	for exchange := range 2 {
		response := request(observation)
		if len(response.Awards) != 1 || len(response.Remove) != 0 || response.Awards[0].Capability != "" || response.Awards[0].UnitID != observation.Award.ID || response.Awards[0].Binding.Generation != claim.Award.Generation || response.Awards[0].Delivery != capability.DeliveryBootstrap || response.Awards[0].Execution == nil {
			t.Fatalf("current award not retained: %+v", response)
		}
		if exchange == 0 {
			if _, err := e.Store.ExchangeCapability(t.Context(), capability.ExchangeSpec{BootstrapPrefix: claim.Award.CapabilityPrefix}); err != nil {
				t.Fatal(err)
			}
		}
	}
	check := func(generation uint16, selector uint8) bool {
		forged := observation
		switch selector % 4 {
		case 0:
			forged.Award.Generation += int64(generation) + 1
		case 1:
			forged.Award.Holder += "/other"
		case 2:
			forged.Award.ID += "/other"
		case 3:
			forged.Award.Kind = capability.UnitPlan
		}
		response := request(forged)
		return len(response.Awards) == 0 && len(response.Remove) == 1 && response.Remove[0] == forged.Job
	}
	if err := quick.Check(check, &quick.Config{MaxCount: 100}); err != nil {
		t.Fatal(err)
	}
	engineSQL(t, e.Store, `UPDATE runs SET cancel_requested_at=1 WHERE id='run'`)
	response := request(observation)
	if len(response.Awards) != 0 || len(response.Remove) != 1 || response.Remove[0] != observation.Job {
		t.Fatalf("cancelled award retained: %+v", response)
	}
	row, err := tenant.ReadNode(t.Context(), store.NodeRef{RunID: "run", NodeID: "job"})
	if err != nil || row.Generation != claim.Award.Generation || row.ClaimedBy != claim.Award.Holder || row.Status != store.StatusPending {
		t.Fatalf("sync changed holder state: %+v, %v", row, err)
	}
}

func TestEngineLauncherSyncRejectsAmbiguousJobObservations(t *testing.T) {
	e, _ := engineRuntimeStore(t)
	engineHTTPRoot(t, e, "", "launcher", []capability.Grant{capability.ClaimsLaunch})
	a := capability.Authorized{Principal: capability.Principal{Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "launcher"}}}
	first := controllerwire.LauncherObservation{Award: capability.LeaseRef{Kind: capability.UnitPlan, ID: "run", Holder: "launcher", Generation: 1}, Job: controllerwire.LauncherJobRef{Namespace: "ci", Name: "job", UID: "uid"}, NodeUID: "node"}
	for _, sameJob := range []bool{true, false} {
		second := first
		if sameJob {
			second.Award.Generation++
		} else {
			second.Job.Name += "-second"
			second.Job.UID += "-second"
		}
		if response, err := e.SyncLauncher(t.Context(), a, controllerwire.LauncherSyncRequest{Observed: []controllerwire.LauncherObservation{first, second}}); err == nil || len(response.Remove) != 0 || len(response.Awards) != 0 {
			t.Fatalf("ambiguous observations returned decisions: %+v, %v", response, err)
		}
	}
}
