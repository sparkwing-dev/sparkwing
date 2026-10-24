package store

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestAcceptedWorkIndexesItsImmutableClaimPolicy(t *testing.T) {
	tenant, acceptance, _ := planFixture(t, runspec.DispatchHost)
	var initial, accepted, node string
	if err := tenant.s.db.QueryRow(`SELECT claim_context FROM triggers WHERE team=? AND id=?`, tenant.team, acceptance.RunID).Scan(&initial); err != nil || initial == "" {
		t.Fatalf("intake context=%q: %v", initial, err)
	}
	if _, err := tenant.AcceptPlan(t.Context(), acceptance); err != nil {
		t.Fatal(err)
	}
	if err := tenant.s.db.QueryRow(`SELECT claim_context FROM triggers WHERE team=? AND id=?`, tenant.team, acceptance.RunID).Scan(&accepted); err != nil || accepted == "" || accepted == initial {
		t.Fatalf("accepted context=%q intake=%q: %v", accepted, initial, err)
	}
	if err := tenant.s.db.QueryRow(`SELECT claim_context FROM nodes WHERE team=? AND run_id=? AND node_id='job'`, tenant.team, acceptance.RunID).Scan(&node); err != nil || node == "" || node == accepted {
		t.Fatalf("job context=%q owner=%q: %v", node, accepted, err)
	}
	if _, err := tenant.s.db.Exec(`DELETE FROM claim_contexts WHERE team=? AND id=?`, tenant.team, node); err == nil {
		t.Fatal("deleted a context referenced by ready work")
	}
}

func TestClaimContextsDistinguishPlacementPolicy(t *testing.T) {
	s, _ := unitClaimFixture(t)
	persist := func(kind capability.UnitKind, execution string) string {
		t.Helper()
		var id string
		if err := s.inTx(t.Context(), func(tx *storeTx) error {
			var err error
			id, err = persistClaimContextTx(t.Context(), tx, "a", "run", kind, "", []byte(execution))
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	baseline := persist(capability.UnitNode, `{}`)
	if baseline == "" || persist(capability.UnitNode, `{}`) != baseline {
		t.Fatal("identical immutable policy did not share a context")
	}
	if persist(capability.UnitPlan, `{}`) == baseline {
		t.Fatal("different unit kinds shared a context")
	}
	if persist(capability.UnitNode, `{"prefers":{"value":["gpu"]}}`) == baseline {
		t.Fatal("different placement preferences shared a context")
	}
	if _, err := s.db.Exec(`UPDATE runs SET repo_identity='github.com/other/repo'`); err != nil {
		t.Fatal(err)
	}
	if persist(capability.UnitNode, `{}`) == baseline {
		t.Fatal("different repository access policies shared a context")
	}
	if _, err := s.db.Exec(`UPDATE runs SET repo_identity='github.com/acme/repo',created_principal='another-account'`); err != nil {
		t.Fatal(err)
	}
	if persist(capability.UnitNode, `{}`) == baseline {
		t.Fatal("different requesters shared a context")
	}
	if _, err := s.db.Exec(`UPDATE runs SET spec_json=?,spec_hash='spec',sealed_at=1,plan_json=?,plan_hash='plan'`, []byte(`{"resolved":{}}`), []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	missingOptions := persist(capability.UnitNode, `{}`)
	if _, err := s.db.Exec(`UPDATE runs SET spec_json=?`, []byte(`{"resolved":{"settings":{"options":{}}}}`)); err != nil {
		t.Fatal(err)
	}
	if persist(capability.UnitNode, `{}`) == missingOptions {
		t.Fatal("missing and explicit options shared a context despite different request fallback")
	}
}

func TestClaimContextHoldsDoNotHideYoungerDifferentPolicies(t *testing.T) {
	for _, difference := range []string{"repository", "labels", "revision"} {
		t.Run(difference, func(t *testing.T) {
			s := authorityStore(t)
			f := s.TestOnlyRankingFixture(t, true)
			f.Request(t, func(r *runspec.Request) { r.Origin = runspec.OriginApp })
			observer := authorityRoot("restricted", "a")
			observer.Repositories = []string{"github.com/acme/repo"}
			authorityPersist(t, s, observer)
			s.testOnlyExec(t, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,repo_display,repo_identity,dispatch,intake_mode,allowed_pools,created_subject_kind,created_principal,created_at,log_retention_class,log_retention_days) SELECT team,'younger',2,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,repo_display,repo_identity,dispatch,intake_mode,allowed_pools,created_subject_kind,created_principal,created_at,log_retention_class,log_retention_days FROM runs WHERE team='a' AND id='run'`)
			s.testOnlyExec(t, `INSERT INTO triggers(team,id,unit_kind,available_at,created_at,expires_at,needs_labels,prefers_labels) SELECT team,'younger',unit_kind,available_at,created_at,expires_at,needs_labels,prefers_labels FROM triggers WHERE team='a' AND id='run'`)
			s.testOnlyExec(t, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) SELECT team,'younger',node_id,status,?,spec_json,execution_json FROM nodes WHERE team='a' AND run_id='run'`, f.ReadyAt.Add(time.Millisecond).UnixNano())
			if difference == "repository" {
				if _, err := s.db.Exec(`UPDATE runs SET repo_key='gh:2',repo_display='https://github.com/other/repo',repo_identity='github.com/other/repo' WHERE id='younger'`); err != nil {
					t.Fatal(err)
				}
			} else if difference == "revision" {
				var request []byte
				if err := s.db.QueryRow(`SELECT request_json FROM runs WHERE id='younger'`).Scan(&request); err != nil {
					t.Fatal(err)
				}
				request = []byte(strings.ReplaceAll(string(request), rankingSHA, strings.Repeat("b", 40)))
				if _, err := s.db.Exec(`UPDATE runs SET request_json=? WHERE id='younger'`, request); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := s.db.Exec(`UPDATE nodes SET execution_json=? WHERE run_id='younger'`, []byte(`{"requires":{"value":["gpu"]}}`)); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.inTx(t.Context(), func(tx *storeTx) error {
				for _, run := range []string{"run", "younger"} {
					var execution []byte
					if err := tx.QueryRowContext(t.Context(), `SELECT execution_json FROM nodes WHERE team='a' AND run_id=?`, run).Scan(&execution); err != nil {
						return err
					}
					var settings runspec.JobExecutionSettings
					if err := json.Unmarshal(execution, &settings); err != nil {
						return err
					}
					req, err := persistClaimRequirementsTx(t.Context(), tx, "a", settings.Requires.Value)
					if err != nil {
						return err
					}
					cc, err := persistClaimContextTx(t.Context(), tx, "a", run, capability.UnitNode, req, execution)
					if err != nil {
						return err
					}
					if _, err := tx.execContext(t.Context(), `UPDATE nodes SET claim_requirements=?,claim_context=? WHERE team='a' AND run_id=?`, req, cc, run); err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			f.At(-time.Second)
			prefix := observer.Prefix
			if difference == "revision" {
				prefix = f.Actions
			}
			f.Poll(t, prefix, capability.UnitNode)
			f.At(time.Second)
			claim, err := s.AsOperator().ClaimUnit(t.Context(), UnitDemand{CapabilityPrefix: f.Launcher, Kinds: []capability.UnitKind{capability.UnitNode}, Labels: []string{"sparkwing-4cpu", "gpu"}})
			if err != nil || claim.RunID != "younger" {
				t.Fatalf("held older policy hid younger %s policy: %+v %v", difference, claim, err)
			}
		})
	}
}

func TestClaimContextsIgnoreRunSpecificExecutionInputs(t *testing.T) {
	s, _ := unitClaimFixture(t)
	persist := func(request, spec, execution string) string {
		t.Helper()
		if _, err := s.db.Exec(`UPDATE runs SET request_json=?,spec_json=?,spec_hash='spec',sealed_at=1,plan_json=?,plan_hash='plan'`, []byte(request), []byte(spec), []byte(`{}`)); err != nil {
			t.Fatal(err)
		}
		var id string
		if err := s.inTx(t.Context(), func(tx *storeTx) error {
			var err error
			id, err = persistClaimContextTx(t.Context(), tx, "a", "run", capability.UnitNode, "", []byte(execution))
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	firstRequest := `{"request":{"mode":"execute","dispatch":"controller","requested_args":{"x":"first"},"lineage":{"parent_run_id":"first","root_run_id":"first","retry_of":"first"},"options":{"owner_execution_timeout":{"value":1},"runner":{"set":false}},"git":{"repository":{"display":"https://github.com/acme/repo"},"app":{"value":{"delivery_key":"first"}}}},"provenance":{"trust":"asserted","ref":"main","sha":"abc","delivery":"first"}}`
	nextRequest := `{"request":{"mode":"execute","dispatch":"controller","requested_args":{"x":"second"},"lineage":{"parent_run_id":"second","root_run_id":"second","retry_of":"second"},"options":{"owner_execution_timeout":{"value":99},"runner":{"set":false,"origin":{"key":"other"}}},"git":{"repository":{"display":"https://github.com/acme/repo"},"app":{"value":{"delivery_key":"second"}}}},"provenance":{"trust":"asserted","ref":"feature","sha":"def","delivery":"second"}}`
	firstSpec := `{"resolved":{"settings":{"options":{"requires":{"value":["linux"]},"prefers":{"value":["gpu"]},"owner_execution_timeout":{"value":1}}},"guards":{"passed":true}},"declarations":{"closures":{"skip_if":["first"]}}}`
	nextSpec := `{"resolved":{"settings":{"options":{"requires":{"value":["linux"],"origin":{"key":"other"}},"prefers":{"value":["gpu"]},"owner_execution_timeout":{"value":99}}},"guards":{"passed":true}},"declarations":{"closures":{"skip_if":["second"]}}}`
	firstExecution := `{"requires":{"value":["linux"]},"prefers":{"value":["gpu"]},"timeout":{"value":1},"concurrency":[{"group":"first"}],"closures":{"skip_if":["first"]}}`
	nextExecution := `{"requires":{"value":["linux"],"origin":{"key":"other"}},"prefers":{"value":["gpu"]},"timeout":{"value":99},"concurrency":[{"group":"second"}],"closures":{"skip_if":["second"]}}`
	baseline := persist(firstRequest, firstSpec, firstExecution)
	if got := persist(nextRequest, nextSpec, nextExecution); got != baseline {
		t.Fatalf("run-specific inputs split identical claim policy: %s != %s", got, baseline)
	}
	verified := strings.Replace(nextRequest, `"trust":"asserted"`, `"trust":"verified","default_branch":"main"`, 1)
	main := strings.Replace(verified, `"ref":"feature"`, `"ref":"refs/heads/main"`, 1)
	if persist(verified, nextSpec, nextExecution) == persist(main, nextSpec, nextExecution) {
		t.Fatal("verified main and feature work shared a claim context")
	}
	if persist(main, nextSpec, nextExecution) == persist(strings.Replace(main, `"default_branch":"main"`, `"default_branch":"trunk"`, 1), nextSpec, nextExecution) {
		t.Fatal("different verified default branches shared a claim context")
	}
	for _, change := range []struct{ name, request, spec, execution string }{
		{"runner", strings.Replace(nextRequest, `"set":false`, `"set":true,"value":{"kind":"name","value":"other"}`, 1), nextSpec, nextExecution},
		{"root requirements", strings.Replace(nextRequest, `"runner":`, `"requires":{"value":["class=cloud"]},"runner":`, 1), nextSpec, nextExecution},
		{"trust", strings.Replace(nextRequest, `"asserted"`, `"schedule"`, 1), nextSpec, nextExecution},
		{"job labels", nextRequest, nextSpec, strings.Replace(nextExecution, `"linux"`, `"arm"`, 1)},
		{"resolved preferences", nextRequest, strings.Replace(nextSpec, `"gpu"`, `"cpu"`, 1), nextExecution},
		{"guards", nextRequest, strings.Replace(nextSpec, `"passed":true`, `"passed":false`, 1), nextExecution},
	} {
		t.Run(change.name, func(t *testing.T) {
			if got := persist(change.request, change.spec, change.execution); got == baseline {
				t.Fatal("different claim policy shared a context")
			}
		})
	}
}

func TestClaimWithNoFreePollSlotDoesNotAward(t *testing.T) {
	s, demand := unitClaimFixture(t)
	slots := 0
	demand.FreeSlots = &slots
	if _, err := s.AsOperator().ClaimUnit(t.Context(), demand); !errors.Is(err, ErrNotFound) {
		t.Fatalf("busy poll award: %v", err)
	}
	demand.FreeSlots = nil
	if _, err := s.AsOperator().ClaimUnit(t.Context(), demand); err != nil {
		t.Fatalf("default poll slot: %v", err)
	}
}
