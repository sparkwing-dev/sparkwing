package store

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func expansionFixture(t *testing.T) (*Tenant, *time.Time, ExpansionSubmission) {
	t.Helper()
	tenant, now, _ := stateFixture(t)
	plan := []byte(`{"jobs":[{"id":"job","closures":{"generate":["group"]}}]}`)
	spec, err := json.Marshal(runspec.Spec{Version: 1, Request: runspec.SealedRequest{Version: 1, Request: runspec.Request{Pipeline: "p", Dispatch: runspec.DispatchHost}, Provenance: assertedProvenance(t, "p", "", "")}})
	if err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, tenant.s.db, `UPDATE runs SET spec_json=?,spec_hash='spec',plan_hash='plan',plan_json=?,sealed_at=1 WHERE id='run'`, spec, plan)
	rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET status='done',outcome='success',finished_at=?,claimed_by=NULL,lease_expires_at=NULL,capability_prefix='',spec_json=? WHERE run_id='run'`, now.UnixNano(), []byte(`{"id":"job","closures":{"generate":["group"]}}`))
	rewriteSchemaExec(t, tenant.s.db, `INSERT INTO triggers(team,id,status,unit_kind,available_at,created_at,claimed_by,capability_prefix,generation,lease_expires_at,needs_labels,prefers_labels,claimed_at) VALUES ('a','run','claimed','trigger',1,1,'host','owner',5,?,?,?,1)`, now.Add(time.Minute).UnixNano(), []byte(`[]`), []byte(`[]`))
	owner := authorityRoot("owner", "a")
	owner.Grants = []capability.Grant{capability.ExpansionSubmit}
	owner.Binding = capability.Binding{Kind: capability.BindingTrigger, RunID: "run", Generation: 5, Holder: "host"}
	authorityPersist(t, tenant.s, owner)
	expansion := authorityRoot("expansion", "a")
	expansion.Grants = []capability.Grant{capability.ExpansionSubmit}
	expansion.Parent = &capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: owner.Prefix}
	expansion.Binding = capability.Binding{Kind: capability.BindingNode, RunID: "run", NodeID: "job", Generation: 2, Expansion: &capability.LeaseRef{Kind: capability.UnitTrigger, ID: "run", Holder: "host", Generation: 5}}
	authorityPersist(t, tenant.s, expansion)
	request := ExpansionSubmission{Source: controllerwire.NodeRef{RunID: "run", NodeID: "job"}, SourceGeneration: 2, Failure: &ExpansionFailure{Code: apierr.SDKError, MaskedError: "generator failed"}, Actor: Actor{Kind: ActorRunHost, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: expansion.Prefix}, CapabilityPrefix: expansion.Prefix}}
	return tenant, now, request
}

func TestExpansionFailureIsAtomicAndReplayable(t *testing.T) {
	tenant, _, request := expansionFixture(t)
	accepted, err := tenant.AcceptExpansion(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.State != ExpansionFailed || accepted.Replayed || accepted.BodyHash == "" || len(accepted.Nodes) != 0 {
		t.Fatalf("failure = %+v", accepted)
	}
	row, err := tenant.ReadNode(t.Context(), request.Source)
	if err != nil {
		t.Fatal(err)
	}
	if row.Expansion != ExpansionFailed || row.Outcome != NodeSuccess || row.ExpansionDigest != accepted.BodyHash {
		t.Fatalf("persisted failure = %+v", row)
	}
	replay, err := tenant.AcceptExpansion(t.Context(), request)
	if err != nil || !replay.Replayed || replay.BodyHash != accepted.BodyHash {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	request.Failure = &ExpansionFailure{Code: apierr.SDKError, MaskedError: "changed"}
	if _, err := tenant.AcceptExpansion(t.Context(), request); err == nil {
		t.Fatal("changed failure replay succeeded")
	}
}

func TestExpansionRefusesStaleAuthorityAndInvalidSources(t *testing.T) {
	for _, problem := range []string{"source_generation", "owner_generation", "owner_expiry", "cancelled", "source_failed", "foreign_team", "old_source_claim", "no_generator", "both_forms", "neither_form", "unknown_code"} {
		t.Run(problem, func(t *testing.T) {
			tenant, now, request := expansionFixture(t)
			switch problem {
			case "source_generation":
				request.SourceGeneration++
			case "owner_generation":
				rewriteSchemaExec(t, tenant.s.db, `UPDATE triggers SET generation=6 WHERE id='run'`)
			case "owner_expiry":
				*now = now.Add(time.Minute)
			case "cancelled":
				rewriteSchemaExec(t, tenant.s.db, `UPDATE runs SET cancel_requested_at=1 WHERE id='run'`)
			case "source_failed":
				rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET outcome='failed' WHERE run_id='run'`)
			case "foreign_team":
				tenant = &Tenant{s: tenant.s, team: "b"}
			case "old_source_claim":
				request.Actor.Authority.ID, request.Actor.CapabilityPrefix = "owner", "owner"
			case "no_generator":
				rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET spec_json=? WHERE run_id='run'`, []byte(`{"id":"job"}`))
			case "both_forms":
				request.Body = []byte(`{"jobs":[]}`)
			case "neither_form":
				request.Failure = nil
			case "unknown_code":
				request.Failure = &ExpansionFailure{Code: "unknown", MaskedError: "failed"}
			}
			if _, err := tenant.AcceptExpansion(t.Context(), request); err == nil {
				t.Fatal("invalid expansion accepted")
			}
			var state string
			if err := tenant.s.db.QueryRow(`SELECT expansion FROM nodes WHERE team='a' AND run_id='run' AND node_id='job'`).Scan(&state); err != nil {
				t.Fatal(err)
			}
			if state != "expected" {
				t.Fatalf("refused expansion changed state: %q", state)
			}
		})
	}
}

func TestExpansionReplayStillRequiresLiveOwner(t *testing.T) {
	tenant, now, request := expansionFixture(t)
	if _, err := tenant.AcceptExpansion(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(time.Minute)
	if _, err := tenant.AcceptExpansion(t.Context(), request); err == nil {
		t.Fatal("expired owner replay succeeded")
	}
}

func TestExpansionEvalKeepsSourceAndLeaseFencesSeparate(t *testing.T) {
	for _, problem := range []string{"", "eval_generation", "owner_generation", "owner_expiry"} {
		t.Run(problem, func(t *testing.T) {
			tenant, now, request := expansionFixture(t)
			unitID, err := encodeAwardUnit(UnitRef{Kind: capability.UnitEval, RunID: "run", NodeID: "job", SourceGeneration: 2})
			if err != nil {
				t.Fatal(err)
			}
			eval := authorityRoot("evaluation", "a")
			eval.Grants = []capability.Grant{capability.ExpansionSubmit}
			eval.Binding = capability.Binding{Kind: capability.BindingNode, RunID: "run", NodeID: "job", Generation: 2, Expansion: &capability.LeaseRef{Kind: capability.UnitEval, ID: unitID, Holder: "eval-host", Generation: 8}}
			authorityPersist(t, tenant.s, eval)
			request.Actor.Authority.ID, request.Actor.CapabilityPrefix = eval.Prefix, eval.Prefix
			rewriteSchemaExec(t, tenant.s.db, `UPDATE runs SET dispatch='controller',plan_accepted_generation=1 WHERE id='run'`)
			rewriteSchemaExec(t, tenant.s.db, `INSERT INTO leases(name,holder,generation,until) VALUES ('controller.owner','controller',4,?)`, now.Add(time.Minute).UnixNano())
			rewriteSchemaExec(t, tenant.s.db, `INSERT INTO eval_units(team,run_id,source_node_id,source_generation,status,generation,claimed_by,capability_prefix,lease_expires_at,owner_generation,claimed_at,started_at) VALUES ('a','run','job',2,'running',8,'eval-host','evaluation',?,4,1,1)`, now.Add(time.Minute).UnixNano())
			switch problem {
			case "eval_generation":
				rewriteSchemaExec(t, tenant.s.db, `UPDATE eval_units SET generation=9`)
			case "owner_generation":
				rewriteSchemaExec(t, tenant.s.db, `UPDATE leases SET generation=5 WHERE name='controller.owner'`)
			case "owner_expiry":
				rewriteSchemaExec(t, tenant.s.db, `UPDATE leases SET until=? WHERE name='controller.owner'`, now.UnixNano())
			}
			accepted, err := tenant.AcceptExpansion(t.Context(), request)
			if problem != "" {
				if err == nil {
					t.Fatal("stale eval or controller owner accepted expansion")
				}
				return
			}
			if err != nil || accepted.SourceGeneration != 2 || accepted.State != ExpansionFailed {
				t.Fatalf("eval expansion = %+v, %v", accepted, err)
			}
			run, err := tenant.ReadRun(t.Context(), "run")
			if err != nil || run.State != RunFailed {
				t.Fatalf("failed expansion did not settle controller run: %+v, %v", run, err)
			}
		})
	}
}
