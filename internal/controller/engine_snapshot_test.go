package controller

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestEngineSnapshotRetainsEmptyExpansionAndPersistedState(t *testing.T) {
	server, _ := engineRuntimeStore(t)
	owner := engineRuntimeClaim(t, server, capability.UnitTrigger)
	plan := []byte(`{"jobs":[{"id":"source","closures":{"generate":["empty"]}},{"id":"consumer","needs_generated":["empty"]}]}`)
	expansion := json.RawMessage(`{"jobs":[]}`)
	registration := runspec.SecretRegistration{Version: 1}
	digest, err := runspec.CanonicalHash(struct {
		Body         json.RawMessage
		Registration runspec.SecretRegistration
	}{expansion, registration})
	if err != nil {
		t.Fatal(err)
	}
	registrationJSON, err := json.Marshal(registration)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := json.Marshal(runspec.Spec{Resolved: runspec.Resolved{Settings: runspec.ExecutionSettings{Options: runspec.Options{Only: runspec.Setting[string]{Optional: runspec.Optional[string]{Set: true, Value: "source"}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	engineSQL(t, server.Store, `UPDATE runs SET plan_json=?,plan_hash='plan',sealed_at=1,spec_json=?,spec_hash='spec'`, plan, spec)
	engineSQL(t, server.Store, `INSERT INTO nodes(team,run_id,node_id,seq,status,outcome,finished_at,generation,attempt,state_seq,spec_json,execution_json,expansion,expansion_body,expansion_registration,expansion_digest) VALUES ('a','run','source',0,'done','success',1,3,2,7,?,?,'accepted',?,?,?)`, []byte(`{"id":"source","closures":{"generate":["empty"]}}`), []byte(`{}`), expansion, registrationJSON, digest)
	engineSQL(t, server.Store, `INSERT INTO nodes(team,run_id,node_id,seq,status,spec_json,execution_json) VALUES ('a','run','consumer',1,'pending',?,?)`, []byte(`{"id":"consumer","needs_generated":["empty"]}`), []byte(`{}`))
	got, err := server.snapshot(t.Context(), owner)
	if err != nil {
		t.Fatal(err)
	}
	if got.SelectionSkips["consumer"] != `outside --only="source"` || len(got.SelectionSkips) != 1 {
		t.Fatalf("snapshot lost sealed selection: %+v", got.SelectionSkips)
	}
	bodyHash, err := runspec.CanonicalHash(json.RawMessage(`{"jobs":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if got.RunID != "run" || got.StoreNow.IsZero() || len(got.Jobs) != 2 || got.Jobs[0].ID != "source" || got.Jobs[0].Generation != 3 || got.Jobs[0].Attempt != 2 || got.Jobs[0].StateSeq != 7 || got.Jobs[1].Declaration.ID != "consumer" || got.Jobs[1].Ready || len(got.Generators) != 1 || got.Generators[0].ID != "empty" || len(got.Generators[0].Members) != 0 || got.Generators[0].State != "accepted" || len(got.Expansions) != 1 || got.Expansions[0].SourceGeneration != 3 || got.Expansions[0].BodyHash != string(bodyHash) || string(got.Expansions[0].Body) != `{"jobs":[]}` {
		t.Fatalf("persisted snapshot: %+v", got)
	}
	engineSQL(t, server.Store, `UPDATE runs SET cancel_requested_at=1`)
	got, err = server.snapshot(t.Context(), owner)
	if err != nil || !got.CancelRequested {
		t.Fatalf("cancelled snapshot: %+v, %v", got, err)
	}
}

func TestEngineSnapshotRefusesCorruptionAndStaleAuthority(t *testing.T) {
	for _, problem := range []string{"declaration", "foreign_team", "revoked", "owner_replaced", "lease_expired"} {
		t.Run(problem, func(t *testing.T) {
			server, _ := engineRuntimeStore(t)
			owner := engineRuntimeClaim(t, server, capability.UnitTrigger)
			engineSQL(t, server.Store, `UPDATE runs SET plan_json=?,plan_hash='plan',sealed_at=1,spec_json=?,spec_hash='spec'`, []byte(`{"jobs":[]}`), []byte(`{}`))
			switch problem {
			case "declaration":
				engineSQL(t, server.Store, `UPDATE runs SET plan_json=?`, []byte(`{"jobs":[{"id":"missing"}]}`))
			case "foreign_team":
				owner.Target.Team = "b"
			case "revoked":
				engineSQL(t, server.Store, `UPDATE capabilities SET revoked_at=1 WHERE prefix=?`, owner.Principal.Authority.ID)
			case "owner_replaced":
				engineSQL(t, server.Store, `UPDATE triggers SET generation=generation+1`)
			case "lease_expired":
				engineSQL(t, server.Store, `UPDATE triggers SET lease_expires_at=1`)
			}
			got, err := server.snapshot(t.Context(), owner)
			if err == nil || got.RunID != "" || len(got.Jobs) != 0 {
				t.Fatalf("invalid snapshot returned data: %+v, %v", got, err)
			}
		})
	}
}

func TestEngineSnapshotRefusesEveryReplacementOwnerFence(t *testing.T) {
	server, _ := engineRuntimeStore(t)
	owner := engineRuntimeClaim(t, server, capability.UnitTrigger)
	engineSQL(t, server.Store, `UPDATE runs SET plan_json=?,plan_hash='plan',sealed_at=1,spec_json=?,spec_hash='spec'`, []byte(`{"jobs":[]}`), []byte(`{}`))
	property := func(value []byte, generation uint16) bool {
		holder := "replacement-" + base64.RawURLEncoding.EncodeToString(value)
		engineSQL(t, server.Store, `UPDATE triggers SET claimed_by=?,generation=? WHERE id='run'`, holder, int64(generation)+owner.Binding.Generation+1)
		snapshot, err := server.snapshot(t.Context(), owner)
		return err != nil && snapshot.RunID == "" && len(snapshot.Jobs) == 0
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 64}); err != nil {
		t.Fatal(err)
	}
}

func TestEngineSnapshotRetainsSealedGuardRefusal(t *testing.T) {
	server, _ := engineRuntimeStore(t)
	owner := engineRuntimeClaim(t, server, capability.UnitTrigger)
	spec, err := json.Marshal(runspec.Spec{Resolved: runspec.Resolved{Guards: runspec.Guards{Reason: "guard refused"}}})
	if err != nil {
		t.Fatal(err)
	}
	engineSQL(t, server.Store, `UPDATE runs SET plan_json=?,plan_hash='plan',sealed_at=1,spec_json=?,spec_hash='spec'`, []byte(`{"jobs":[]}`), spec)
	got, err := server.snapshot(t.Context(), owner)
	if err != nil || got.GuardRefusal != "guard refused" || got.CancelRequested {
		t.Fatalf("sealed guard: %+v, %v", got, err)
	}
	engineSQL(t, server.Store, `UPDATE runs SET cancel_requested_at=1`)
	got, err = server.snapshot(t.Context(), owner)
	if err != nil || !got.CancelRequested || got.GuardRefusal != "guard refused" {
		t.Fatalf("cancelled guard: %+v, %v", got, err)
	}
}
