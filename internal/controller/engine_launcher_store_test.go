package controller

import (
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func TestEngineLauncherSyncUsesStoreSnapshot(t *testing.T) {
	e, tenant := engineRuntimeStore(t)
	if _, err := tenant.RecordCreditGrant(t.Context(), store.CreditGrantRequest{Kind: store.CreditGrantFree, AmountMicro: 100_000_000, Reference: "launcher-test", CreatedBy: "fixture"}); err != nil {
		t.Fatal(err)
	}
	raw := engineHTTPRoot(t, e, "", "launcher", []capability.Grant{capability.ClaimsLaunch})
	engineSQL(t, e.Store, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES ('a','run','job','pending',1,?,?)`, []byte(`{"id":"job"}`), []byte(`{}`))
	engineAcceptedFixturePlan(t, e, "a", "run")
	claim, err := e.Store.AsOperator().ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: "launcher", Target: &store.UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: "job"}, Capacity: match.Resources{Cores: 1}, Available: match.Resources{Cores: 1}})
	if err != nil {
		t.Fatal(err)
	}
	h := New(e.Store, slog.New(slog.NewTextHandler(io.Discard, nil))).WithEngineController(e.EngineController).Handler()
	observation := controllerwire.LauncherObservation{Award: capability.LeaseRef{Kind: capability.UnitNode, Holder: claim.Award.Holder, Generation: int64(claim.Award.Generation)}}
	principal, err := e.Store.AsOperator().ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix})
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
		if len(response.Awards) != 1 || len(response.Retired) != 0 || response.Awards[0].Capability != "" || response.Awards[0].UnitID != observation.Award.ID || response.Awards[0].Binding.Generation != int64(claim.Award.Generation) || response.Awards[0].Delivery != capability.DeliveryBootstrap || response.Awards[0].Execution == nil {
			t.Fatalf("current award not retained: %+v", response)
		}
		if exchange == 0 {
			if _, err := e.Store.AsOperator().ExchangeCapability(t.Context(), capability.ExchangeSpec{BootstrapPrefix: claim.Award.CapabilityPrefix}); err != nil {
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
		return len(response.Awards) == 0 && len(response.Retired) == 1 && response.Retired[0] == forged.Award
	}
	if err := quick.Check(check, &quick.Config{MaxCount: 100}); err != nil {
		t.Fatal(err)
	}
	engineSQL(t, e.Store, `UPDATE runs SET cancel_requested_at=1 WHERE id='run'`)
	response := request(observation)
	if len(response.Awards) != 0 || len(response.Retired) != 1 || response.Retired[0] != observation.Award {
		t.Fatalf("cancelled award retained: %+v", response)
	}
	row, err := tenant.ReadNode(t.Context(), controllerwire.NodeRef{RunID: "run", NodeID: "job"})
	if err != nil || row.Generation != claim.Award.Generation || row.ClaimedBy != claim.Award.Holder || row.Status != store.StatusPending {
		t.Fatalf("sync changed holder state: %+v, %v", row, err)
	}
}

func TestEngineLauncherSyncRejectsAmbiguousJobObservations(t *testing.T) {
	e, _ := engineRuntimeStore(t)
	engineHTTPRoot(t, e, "", "launcher", []capability.Grant{capability.ClaimsLaunch})
	a := capability.Authorized{Principal: capability.Principal{Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "launcher"}}}
	first := controllerwire.LauncherObservation{Award: capability.LeaseRef{Kind: capability.UnitPlan, ID: "run", Holder: "launcher", Generation: 1}}
	second := first
	second.Rejected = true
	if response, err := e.SyncLauncher(t.Context(), a, controllerwire.LauncherSyncRequest{Observed: []controllerwire.LauncherObservation{first, second}}); err == nil || len(response.Retired) != 0 || len(response.Awards) != 0 {
		t.Fatalf("duplicate award returned decisions: %+v %v", response, err)
	}
}

func TestEngineLauncherSyncCommitsPodRejection(t *testing.T) {
	e, tenant := engineRuntimeStore(t)
	if _, err := tenant.RecordCreditGrant(t.Context(), store.CreditGrantRequest{Kind: store.CreditGrantFree, AmountMicro: 100_000_000, Reference: "pod-rejection", CreatedBy: "fixture"}); err != nil {
		t.Fatal(err)
	}
	raw := engineHTTPRoot(t, e, "", "launcher", []capability.Grant{capability.ClaimsLaunch})
	engineSQL(t, e.Store, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES ('a','run','job','pending',1,?,?)`, []byte(`{"id":"job"}`), []byte(`{}`))
	engineAcceptedFixturePlan(t, e, "a", "run")
	claim, err := e.Store.AsOperator().ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: "launcher", Target: &store.UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: "job"}, Capacity: match.Resources{Cores: 1}, Available: match.Resources{Cores: 1}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := e.Store.AsOperator().ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix})
	if err != nil {
		t.Fatal(err)
	}
	observation := controllerwire.LauncherObservation{Award: capability.LeaseRef{Kind: capability.UnitNode, ID: p.Capability.UnitID, Holder: claim.Award.Holder, Generation: int64(claim.Award.Generation)}, Rejected: true}
	body, err := json.Marshal(controllerwire.LauncherSyncRequest{Observed: []controllerwire.LauncherObservation{observation}})
	if err != nil {
		t.Fatal(err)
	}
	h := New(e.Store, slog.New(slog.NewTextHandler(io.Discard, nil))).WithEngineController(e.EngineController).Handler()
	for range 2 {
		w := engineClaimHTTP(t, h, raw, "POST", "/api/v1/launcher/sync", string(body), 200)
		var response controllerwire.LauncherSyncResponse
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if len(response.Awards) != 0 || len(response.Retired) != 1 || response.Retired[0] != observation.Award {
			t.Fatalf("rejection not acknowledged: %+v", response)
		}
	}
	row, err := tenant.ReadNode(t.Context(), controllerwire.NodeRef{RunID: "run", NodeID: "job"})
	if err != nil || row.Outcome != store.NodeFailed || row.ClaimedBy != "" {
		t.Fatalf("rejection did not fail persisted award: %+v %v", row, err)
	}
}

func TestEngineLauncherSyncIsolatesCorruptionDuringPreparationAndSnapshot(t *testing.T) {
	for _, damage := range []struct {
		name, query string
		body        []byte
	}{
		{"capability-envelope", `UPDATE capabilities SET authority_json=? WHERE prefix=?`, []byte(`{`)},
		{"capability-projection", `UPDATE capabilities SET unit_id='damaged' WHERE prefix=?`, nil},
		{"node-state", `UPDATE nodes SET status='running',attempt=0 WHERE run_id='run'`, nil},
		{"node-scalar", `UPDATE nodes SET ready_at='damaged' WHERE run_id='run'`, nil},
		{"billing-price", `UPDATE credit_charges SET cpu_class=0 WHERE kind='reservation' AND run_id='run'`, nil},
		{"node-syntax", `UPDATE nodes SET execution_json=? WHERE run_id='run'`, []byte(`{`)},
		{"node-type", `UPDATE nodes SET execution_json=? WHERE run_id='run'`, []byte(`[]`)},
		{"run-syntax", `UPDATE runs SET request_json=? WHERE id='run'`, []byte(`{`)},
		{"run-type", `UPDATE runs SET request_json=? WHERE id='run'`, []byte(`[]`)},
	} {
		t.Run(damage.name, func(t *testing.T) {
			e, tenant := engineRuntimeStore(t)
			if _, err := tenant.RecordCreditGrant(t.Context(), store.CreditGrantRequest{Kind: store.CreditGrantFree, AmountMicro: 100_000_000, Reference: "isolation", CreatedBy: "fixture"}); err != nil {
				t.Fatal(err)
			}
			engineHTTPRoot(t, e, "", "launcher", []capability.Grant{capability.ClaimsLaunch})
			engineSQL(t, e.Store, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,allowed_pools,created_principal,created_at,lineage_json,log_retention_class,log_retention_days) SELECT team,'sibling',2,pipeline,status,CAST(REPLACE(CAST(request_json AS TEXT),'"run"','"sibling"') AS BLOB),request_hash,request_sealed_at,repo_key,dispatch,intake_mode,allowed_pools,created_principal,created_at,lineage_json,log_retention_class,log_retention_days FROM runs WHERE id='run'`)
			engineSQL(t, e.Store, `INSERT INTO triggers(team,id,unit_kind,available_at,created_at,expires_at,needs_labels,prefers_labels) SELECT team,'sibling',unit_kind,available_at,created_at,expires_at,needs_labels,prefers_labels FROM triggers WHERE id='run'`)
			var observed []controllerwire.LauncherObservation
			var preparing []capability.LeaseRef
			var badPrefix string
			for _, id := range []string{"run", "sibling"} {
				engineSQL(t, e.Store, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES ('a',?,'job','pending',1,?,?)`, id, []byte(`{"id":"job"}`), []byte(`{}`))
				engineAcceptedFixturePlan(t, e, "a", id)
				claim, err := e.Store.AsOperator().ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: "launcher", Target: &store.UnitRef{Kind: capability.UnitNode, RunID: runspec.RunID(id), NodeID: "job"}, Capacity: match.Resources{Cores: 1}, Available: match.Resources{Cores: 1}})
				if err != nil {
					t.Fatal(err)
				}
				principal, err := e.Store.AsOperator().ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix})
				if err != nil {
					t.Fatal(err)
				}
				fence := capability.LeaseRef{Kind: capability.UnitNode, ID: principal.Capability.UnitID, Holder: claim.Award.Holder, Generation: int64(claim.Award.Generation)}
				observed = append(observed, controllerwire.LauncherObservation{Award: fence})
				preparing = append(preparing, fence)
				if id == "run" {
					badPrefix = claim.Award.CapabilityPrefix
				}
			}
			var args []any
			if damage.body != nil {
				args = append(args, damage.body)
			}
			if strings.HasPrefix(damage.name, "capability-") {
				args = append(args, badPrefix)
			}
			_, err := teststore.SQL(t, e.Store).ExecContext(t.Context(), damage.query, args...)
			rejected := damage.name == "node-state" || damage.name == "node-scalar"
			if rejected && err == nil {
				t.Fatal("schema accepted an invalid node")
			}
			if !rejected && err != nil {
				t.Fatal(err)
			}
			principal, err := e.Store.AsOperator().ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "launcher"})
			if err != nil {
				t.Fatal(err)
			}
			checkResponse := func(response controllerwire.LauncherSyncResponse, err error) {
				t.Helper()
				if rejected {
					if err != nil || len(response.Deferred) != 0 || len(response.Awards) != 2 || len(response.Retired) != 0 || response.Awards[0].RunID == response.Awards[1].RunID {
						t.Fatalf("healthy response after rejected corruption=%+v, %v", response, err)
					}
				} else if err != nil || len(response.Deferred) != 1 || response.Deferred[0] != preparing[0] || len(response.Awards) != 1 || response.Awards[0].RunID != "sibling" || len(response.Retired) != 0 {
					t.Fatalf("damaged response=%+v, %v", response, err)
				}
			}
			response, err := e.syncLauncher(t.Context(), capability.Authorized{Principal: principal}, controllerwire.LauncherSyncRequest{Observed: observed})
			checkResponse(response, err)
			response, err = e.syncLauncher(t.Context(), capability.Authorized{Principal: principal}, controllerwire.LauncherSyncRequest{Observed: observed, Preparing: preparing})
			checkResponse(response, err)
			for i := range observed {
				observed[i].Reservation = true
			}
			response, err = e.syncLauncher(t.Context(), capability.Authorized{Principal: principal}, controllerwire.LauncherSyncRequest{Observed: observed, Preparing: preparing})
			checkResponse(response, err)
			engineSQL(t, e.Store, `UPDATE nodes SET lease_expires_at=1 WHERE run_id='sibling'`)
			response, err = e.syncLauncher(t.Context(), capability.Authorized{Principal: principal}, controllerwire.LauncherSyncRequest{Observed: observed[1:], Preparing: preparing[1:]})
			if err != nil || len(response.Deferred) != 0 || len(response.Retired) != 1 {
				t.Fatalf("retired reservation deferred: %+v, %v", response, err)
			}
			engineHTTPRoot(t, e, "", "foreign-launcher", []capability.Grant{capability.ClaimsLaunch})
			foreign, err := e.Store.AsOperator().ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "foreign-launcher"})
			if err != nil {
				t.Fatal(err)
			}
			if response, err := e.syncLauncher(t.Context(), capability.Authorized{Principal: foreign}, controllerwire.LauncherSyncRequest{Observed: observed[:1], Preparing: preparing[:1]}); err == nil {
				t.Fatalf("foreign damaged award treated as owned: %+v", response)
			}
		})
	}
}
