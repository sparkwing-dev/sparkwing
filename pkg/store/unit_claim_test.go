package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func unitClaimFixture(t *testing.T) (*Store, UnitDemand) {
	t.Helper()
	return unitClaimFixtureStore(t, authorityStore(t))
}

func unitClaimFixtureStore(t *testing.T, s *Store) (*Store, UnitDemand) {
	t.Helper()
	now := time.Unix(1700000000, 0).UTC()
	s.clock = func() time.Time { return now }
	root := authorityRoot("runner", "a")
	root.Grants = []capability.Grant{capability.NodesClaim, capability.TriggersClaim}
	authorityPersist(t, s, root)
	request, err := json.Marshal(runspec.SealedRequest{Request: runspec.Request{Git: runspec.Git{Repository: runspec.Repository{Key: "gh:1", Display: "https://github.com/acme/repo"}}}})
	if err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,repo_display,dispatch,intake_mode,allowed_pools,created_principal,created_at,log_retention_class,log_retention_days) VALUES ('a','run',1,'ci','running',?,'request',1,'gh:1','https://github.com/acme/repo','host','execute',?,'account',1,'r30',30)`, request, []byte(`[]`))
	rewriteSchemaExec(t, s.db, `INSERT INTO triggers(team,id,unit_kind,available_at,created_at,expires_at,needs_labels,prefers_labels) VALUES ('a','run','trigger',1,1,?,?,?)`, now.Add(time.Hour).UnixNano(), []byte(`[]`), []byte(`[]`))
	execution := runspec.JobExecutionSettings{Resources: runspec.Setting[runspec.Resources]{Optional: runspec.Optional[runspec.Resources]{Value: runspec.Resources{Cores: runspec.Setting[float64]{Optional: runspec.Optional[float64]{Value: 4, Set: true}}}, Set: true}}}
	body, err := json.Marshal(execution)
	if err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES ('a','run','job','pending',1,?,?)`, []byte(`{"id":"job"}`), body)
	return s, UnitDemand{CapabilityPrefix: root.Prefix, Target: &UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: "job"}, Capacity: match.Resources{Cores: 8}, Available: match.Resources{Cores: 8}}
}

func TestClaimUnitNamedAndQueuedUseCapacityAndIdentity(t *testing.T) {
	for _, named := range []bool{true, false} {
		t.Run(map[bool]string{true: "named", false: "queue"}[named], func(t *testing.T) {
			s, demand := unitClaimFixture(t)
			if !named {
				demand.Target = nil
				demand.Kinds = []capability.UnitKind{capability.UnitNode}
			}
			demand.Capacity.Cores, demand.Available.Cores = 2, 2
			if _, err := s.ClaimUnit(t.Context(), demand); err == nil {
				t.Fatal("two-core runner claimed four-core job")
			}
			demand.Capacity.Cores, demand.Available.Cores = 8, 0
			if _, err := s.ClaimUnit(t.Context(), demand); err == nil {
				t.Fatal("busy runner claimed job")
			}
			demand.Available.Cores = 8
			demand.Labels = []string{"name=forged", "class=cloud"}
			claim, err := s.ClaimUnit(t.Context(), demand)
			if err != nil {
				t.Fatal(err)
			}
			if claim.Team != "a" || claim.Award.Holder != "runner" || claim.Award.Generation != 1 || len(claim.Bearer) == 0 {
				t.Fatalf("claim = %+v", claim)
			}
			row, err := (&Tenant{s: s, team: "a"}).ReadNode(t.Context(), NodeRef{"run", "job"})
			if err != nil || row.ClaimedBy != "runner" || row.StateSeq != 1 || row.Attempt != 0 {
				t.Fatalf("row=%+v err=%v", row, err)
			}
			if _, err := s.ClaimUnit(t.Context(), demand); err == nil {
				t.Fatal("claimed twice")
			}
			rewriteSchemaCount(t, s.db, "billing_awards", 1)
			if err := s.ReleaseUnit(t.Context(), claim, claim.Award.CapabilityPrefix); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestClaimUnitRejectsForeignAndMissingRequirements(t *testing.T) {
	for _, test := range []string{"foreign", "pool", "name", "labels", "expired", "grant"} {
		t.Run(test, func(t *testing.T) {
			s, demand := unitClaimFixture(t)
			switch test {
			case "foreign":
				root := authorityRoot("other", "b")
				authorityPersist(t, s, root)
				demand.CapabilityPrefix = root.Prefix
			case "pool":
				rewriteSchemaExec(t, s.db, `UPDATE nodes SET execution_json=?`, []byte(`{"requires":{"set":true,"value":["pool=prod"]}}`))
				demand.Labels = []string{"pool=prod"}
			case "name":
				rewriteSchemaExec(t, s.db, `UPDATE nodes SET execution_json=?`, []byte(`{"requires":{"set":true,"value":["name=other"]}}`))
				demand.Labels = []string{"name=other"}
			case "labels":
				rewriteSchemaExec(t, s.db, `UPDATE nodes SET execution_json=?`, []byte(`{"requires":{"set":true,"value":["gpu"]}}`))
			case "expired":
				rewriteSchemaExec(t, s.db, `UPDATE triggers SET expires_at=1`)
			case "grant":
				root := authorityRoot("reader", "a")
				root.Grants = []capability.Grant{capability.RunsRead}
				authorityPersist(t, s, root)
				demand.CapabilityPrefix = root.Prefix
			}
			if _, err := s.ClaimUnit(t.Context(), demand); err == nil {
				t.Fatal("invalid claimant acquired job")
			}
			rewriteSchemaCount(t, s.db, "billing_awards", 0)
			var sequence int64
			if err := s.db.QueryRow(`SELECT state_seq FROM nodes`).Scan(&sequence); err != nil || sequence != 0 {
				t.Fatalf("sequence=%d err=%v", sequence, err)
			}
		})
	}
}

type claimProfileFake struct {
	profile   match.Profile
	available match.Resources
	before    func()
}

func (f claimProfileFake) ResolveClaimProfile(context.Context, capability.Principal) (match.Profile, match.Resources, error) {
	if f.before != nil {
		f.before()
	}
	return f.profile, f.available, nil
}

func TestClaimUnitRepositoryEnrollmentAndMeteringDoNotGrantPlacement(t *testing.T) {
	for _, queued := range []bool{false, true} {
		for _, metered := range []bool{false, true} {
			t.Run(map[bool]string{false: "named", true: "queue"}[queued]+"/"+map[bool]string{false: "unmetered", true: "metered"}[metered], func(t *testing.T) {
				s, demand := unitClaimFixture(t)
				if queued {
					demand.Target = nil
					demand.Kinds = []capability.UnitKind{capability.UnitNode}
				}
				root := authorityRoot("enrolled", "a")
				root.RootMetered = metered
				authorityPersist(t, s, root)
				demand.CapabilityPrefix = root.Prefix
				accept, err := match.ParseAccept([]string{"github.com/other/*"})
				if err != nil {
					t.Fatal(err)
				}
				s.SetClaimProfileResolver(claimProfileFake{profile: match.Profile{Accept: accept}})
				_, err = s.ClaimUnit(t.Context(), demand)
				var denied *apierr.Error
				if queued && !errors.Is(err, ErrNotFound) || !queued && (!errors.As(err, &denied) || denied.Code != apierr.Forbidden) {
					t.Fatalf("repository denial=%v", err)
				}
				rewriteSchemaCount(t, s.db, "billing_awards", 0)
				if metered {
					return
				}
				accept, err = match.ParseAccept([]string{"github.com/acme/*"})
				if err != nil {
					t.Fatal(err)
				}
				s.SetClaimProfileResolver(claimProfileFake{profile: match.Profile{Accept: accept}})
				claim, err := s.ClaimUnit(t.Context(), demand)
				if err != nil || claim.NodeID != "job" || claim.Award.Holder != root.Subject.ID {
					t.Fatalf("authorized repository claim=%+v, %v", claim, err)
				}
			})
		}
	}
}

func TestClaimUnitGrantedProfileKeys(t *testing.T) {
	for _, queued := range []bool{false, true} {
		for _, requirement := range []string{"name=runner", "class=cloud", "gpu"} {
			t.Run(map[bool]string{false: "named", true: "queue"}[queued]+"/"+requirement, func(t *testing.T) {
				s, demand := unitClaimFixture(t)
				if queued {
					demand.Target = nil
					demand.Kinds = []capability.UnitKind{capability.UnitNode}
				}
				execution, err := json.Marshal(runspec.JobExecutionSettings{Requires: runspec.Setting[[]string]{Optional: runspec.Optional[[]string]{Set: true, Value: []string{requirement}}}})
				if err != nil {
					t.Fatal(err)
				}
				rewriteSchemaExec(t, s.db, `UPDATE nodes SET execution_json=?`, execution)
				impostor := authorityRoot("impostor", "a")
				impostor.Subject.ID = "other"
				impostor.Grants = []capability.Grant{capability.NodesClaim}
				impostor.RootMetered = true
				authorityPersist(t, s, impostor)
				demand.CapabilityPrefix = impostor.Prefix
				if requirement != "gpu" {
					demand.Labels = []string{requirement}
				}
				_, err = s.ClaimUnit(t.Context(), demand)
				var denied *apierr.Error
				if queued && !errors.Is(err, ErrNotFound) || !queued && (!errors.As(err, &denied) || denied.Code != apierr.Forbidden) {
					t.Fatalf("unmet requirement=%v", err)
				}
				rewriteSchemaCount(t, s.db, "billing_awards", 0)
				demand.CapabilityPrefix = "runner"
				demand.Labels = nil
				if requirement == "gpu" {
					demand.Labels = []string{"gpu"}
				}
				if requirement == "class=cloud" {
					launcher := authorityRoot("launcher", "")
					launcher.Subject.Kind = capability.SubjectService
					launcher.Grants = []capability.Grant{capability.ClaimsLaunch}
					authorityPersist(t, s, launcher)
					demand.CapabilityPrefix = launcher.Prefix
				}
				claim, err := s.ClaimUnit(t.Context(), demand)
				if err != nil || claim.NodeID != "job" || claim.Award.Holder != demand.CapabilityPrefix {
					t.Fatalf("matching identity claim=%+v, %v", claim, err)
				}
			})
		}
	}
}

func TestClaimUnitOwnerKindsAndEvalFences(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitTrigger, capability.UnitPlan, capability.UnitEval} {
		t.Run(string(kind), func(t *testing.T) {
			s, demand := unitClaimFixture(t)
			demand.Target = &UnitRef{Kind: kind, RunID: "run"}
			if kind != capability.UnitTrigger {
				root := authorityRoot("launcher", "")
				root.Subject.Kind = capability.SubjectService
				root.Grants = []capability.Grant{capability.ClaimsLaunch}
				authorityPersist(t, s, root)
				demand.CapabilityPrefix = root.Prefix
				rewriteSchemaExec(t, s.db, `UPDATE runs SET dispatch='controller'`)
				rewriteSchemaExec(t, s.db, `UPDATE triggers SET unit_kind='plan'`)
			}
			if kind == capability.UnitEval {
				demand.Target.NodeID, demand.Target.SourceGeneration = "job", 7
				rewriteSchemaExec(t, s.db, `UPDATE nodes SET status='done',outcome='success',finished_at=1,generation=7,expansion='expected'`)
				rewriteSchemaExec(t, s.db, `INSERT INTO leases(name,holder,until,generation) VALUES ('controller.owner','controller',?,3)`, s.now().Add(time.Hour).UnixNano())
				rewriteSchemaExec(t, s.db, `INSERT INTO eval_units(team,run_id,source_node_id,source_generation,status,owner_generation) VALUES ('a','run','job',7,'pending',3)`)
			}
			claim, err := s.ClaimUnit(t.Context(), demand)
			if err != nil {
				t.Fatal(err)
			}
			if claim.Kind != kind || claim.Award.Generation != 1 || claim.Team != "a" || claim.Owner.Holder != claim.Award.Holder {
				t.Fatalf("claim=%+v", claim)
			}
			var persisted capability.Capability
			err = s.inTx(t.Context(), func(tx *storeTx) error {
				var err error
				persisted, err = loadCapabilityTx(t.Context(), tx, claim.Award.CapabilityPrefix)
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			if kind == capability.UnitEval && (persisted.Binding.Generation != 7 || persisted.Binding.Expansion == nil || persisted.Binding.Expansion.Generation != 1) {
				t.Fatalf("eval binding=%+v", persisted.Binding)
			}
			if kind != capability.UnitTrigger && persisted.Delivery != capability.DeliveryBootstrap {
				t.Fatalf("launcher delivery=%s", persisted.Delivery)
			}
			if err = s.ReleaseUnit(t.Context(), claim, claim.Award.CapabilityPrefix); err != nil {
				t.Fatal(err)
			}
			if kind == capability.UnitEval {
				rewriteSchemaExec(t, s.db, `UPDATE leases SET generation=4,holder='new-controller' WHERE name='controller.owner'`)
				demand.Target = nil
				demand.Kinds = []capability.UnitKind{capability.UnitEval}
			}
			second, err := s.ClaimUnit(t.Context(), demand)
			if err != nil || second.Award.Generation != 2 {
				t.Fatalf("reclaim=%+v err=%v", second, err)
			}
			if kind == capability.UnitEval {
				var generation int64
				if err = s.db.QueryRow(`SELECT owner_generation FROM eval_units`).Scan(&generation); err != nil || generation != 4 {
					t.Fatalf("eval owner generation=%d err=%v", generation, err)
				}
			}
		})
	}
}

func TestClaimUnitTriggerRequirementsAndQueuePreference(t *testing.T) {
	s, demand := unitClaimFixture(t)
	demand.Target = &UnitRef{Kind: capability.UnitTrigger, RunID: "run"}
	var body []byte
	if err := s.db.QueryRow(`SELECT request_json FROM runs`).Scan(&body); err != nil {
		t.Fatal(err)
	}
	var sealed runspec.SealedRequest
	if err := json.Unmarshal(body, &sealed); err != nil {
		t.Fatal(err)
	}
	sealed.Request.Options.Requires.Optional = runspec.Optional[[]string]{Set: true, Value: []string{"gpu"}}
	body, err := json.Marshal(sealed)
	if err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `UPDATE runs SET request_json=?`, body)
	if _, err = s.ClaimUnit(t.Context(), demand); err == nil {
		t.Fatal("trigger ignored requirements")
	}
	demand.Labels = []string{"gpu"}
	if _, err = s.ClaimUnit(t.Context(), demand); err != nil {
		t.Fatal(err)
	}
	demand.Target = nil
	demand.Kinds = []capability.UnitKind{capability.UnitNode}
	rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES ('a','run','preferred','pending',1,?,?)`, []byte(`{"id":"preferred"}`), []byte(`{"prefers":{"set":true,"value":["gpu"]}}`))
	claim, err := s.ClaimUnit(t.Context(), demand)
	if err != nil || claim.NodeID != "preferred" {
		t.Fatalf("preferred=%+v err=%v", claim, err)
	}
}

func TestClaimUnitMismatchPreservesQueueAndDoesNotHideEligibleWork(t *testing.T) {
	s, demand := unitClaimFixture(t)
	rewriteSchemaExec(t, s.db, `UPDATE nodes SET execution_json=?`, []byte(`{"requires":{"set":true,"value":["pool=prod"]}}`))
	rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES ('a','run','later','pending',2,?,?)`, []byte(`{"id":"later"}`), []byte(`{}`))
	demand.Target = nil
	demand.Kinds = []capability.UnitKind{capability.UnitNode}
	demand.Labels = []string{"pool=prod"}
	claimed, err := s.ClaimUnit(t.Context(), demand)
	if err != nil || claimed.NodeID != "later" {
		t.Fatalf("oldest eligible claim: %+v, %v", claimed, err)
	}
	tenant := &Tenant{s: s, team: "a"}
	for range 2 {
		if _, err := s.ClaimUnit(t.Context(), demand); !errors.Is(err, ErrNotFound) {
			t.Fatalf("ineligible queue: %v", err)
		}
	}
	row, err := tenant.ReadNode(t.Context(), NodeRef{"run", "job"})
	if err != nil || row.ReadyAt == nil || row.ReadyAt.UnixNano() != 1 || row.StateSeq != 0 || row.ClaimedBy != "" {
		t.Fatalf("mismatch changed queue position: %+v, %v", row, err)
	}
}

func TestClaimUnitRefusesElapsedQueueBeforeReaping(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitTrigger, capability.UnitNode} {
		for _, named := range []bool{false, true} {
			t.Run(string(kind)+map[bool]string{true: "/named", false: "/queue"}[named], func(t *testing.T) {
				s, demand := unitClaimFixture(t)
				var raw []byte
				if err := s.db.QueryRow(`SELECT request_json FROM runs WHERE id='run'`).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				var seal runspec.SealedRequest
				if err := json.Unmarshal(raw, &seal); err != nil {
					t.Fatal(err)
				}
				seal.Request.Options.ClaimWait.Optional = runspec.Optional[time.Duration]{Set: true, Value: time.Minute}
				raw, err := json.Marshal(seal)
				if err != nil {
					t.Fatal(err)
				}
				start := s.now().Add(-time.Minute).UnixNano()
				rewriteSchemaExec(t, s.db, `UPDATE runs SET request_json=?,claim_wait_ns=?,created_at=? WHERE id='run'`, raw, int64(time.Minute), start)
				rewriteSchemaExec(t, s.db, `UPDATE nodes SET ready_at=?,queue_started_at=?`, start, start)
				demand.Kinds = []capability.UnitKind{kind}
				if named {
					demand.Target = &UnitRef{Kind: kind, RunID: "run"}
					if kind == capability.UnitNode {
						demand.Target.NodeID = "job"
					}
				} else {
					demand.Target = nil
				}
				if _, err := s.ClaimUnit(t.Context(), demand); err == nil {
					t.Fatal("expired queue was awarded before reaping")
				}
				rewriteSchemaCount(t, s.db, "billing_awards", 0)
			})
		}
	}
}

func TestClaimUnitHereWindowAndReclaim(t *testing.T) {
	for _, late := range []bool{false, true} {
		t.Run(map[bool]string{false: "within window", true: "after window"}[late], func(t *testing.T) {
			s, demand := unitClaimFixture(t)
			root := authorityRoot("requester", "a")
			root.Grants = []capability.Grant{capability.RunsWrite}
			authorityPersist(t, s, root)
			rewriteSchemaExec(t, s.db, `UPDATE triggers SET created_at=?`, s.now().UnixNano())
			var here capability.Issued
			err := s.inTx(t.Context(), func(tx *storeTx) error {
				var err error
				here, err = s.Issue(t.Context(), tx, capability.IssueRequest{Kind: capability.Award, Award: &capability.AwardSpec{Claimant: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Prefix}, Unit: capability.UnitTrigger, UnitID: "run", HereRunID: "run", Purpose: capability.PurposeHere, Delivery: capability.DeliveryDirect}})
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			start := s.now()
			if late {
				s.clock = func() time.Time { return start.Add(11 * time.Minute) }
			}
			demand.CapabilityPrefix = here.Capability.Prefix
			demand.Target = &UnitRef{Kind: capability.UnitTrigger, RunID: "run"}
			claim, err := s.ClaimUnit(t.Context(), demand)
			if late {
				if err == nil {
					t.Fatal("here claimed after initial window")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = s.ReleaseUnit(t.Context(), claim, claim.Award.CapabilityPrefix); err != nil {
				t.Fatal(err)
			}
			s.clock = func() time.Time { return start.Add(11 * time.Minute) }
			if claim, err = s.ClaimUnit(t.Context(), demand); err != nil || claim.Award.Generation != 2 {
				t.Fatalf("here reclaim=%+v err=%v", claim, err)
			}
		})
	}
}

func TestClaimUnitRechecksOwnerAfterProfileResolution(t *testing.T) {
	s, demand := unitClaimFixture(t)
	demand.Target = &UnitRef{Kind: capability.UnitTrigger, RunID: "run"}
	owner, err := s.ClaimUnit(t.Context(), demand)
	if err != nil {
		t.Fatal(err)
	}
	s.SetClaimProfileResolver(claimProfileFake{before: func() { s.clock = func() time.Time { return owner.Award.LeaseExpiresAt } }})
	demand.Target = &UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: "job"}
	demand.CapabilityPrefix = owner.Award.CapabilityPrefix
	if _, err = s.ClaimUnit(t.Context(), demand); err == nil {
		t.Fatal("owner lease lapse during profile resolution still claimed job")
	}
	var holder sql.NullString
	if err = s.db.QueryRow(`SELECT claimed_by FROM nodes`).Scan(&holder); err != nil || holder.Valid {
		t.Fatalf("holder=%v err=%v", holder, err)
	}
	rewriteSchemaCount(t, s.db, "billing_awards", 1)
}

func TestClaimUnitInheritsRunnerSelection(t *testing.T) {
	for _, unitKind := range []capability.UnitKind{capability.UnitTrigger, capability.UnitNode} {
		for _, runnerKind := range []runspec.RunnerSelectionKind{runspec.RunnerName, runspec.RunnerLabel} {
			for _, queued := range []bool{false, true} {
				t.Run(string(unitKind)+"/"+string(runnerKind)+"/"+map[bool]string{false: "named", true: "queue"}[queued], func(t *testing.T) {
					s, demand := unitClaimFixture(t)
					demand.Target.Kind = unitKind
					if unitKind == capability.UnitTrigger {
						demand.Target.NodeID = ""
					}
					if queued {
						demand.Target = nil
						demand.Kinds = []capability.UnitKind{unitKind}
					}
					selected := "other"
					if runnerKind == runspec.RunnerLabel {
						selected = "gpu"
					}
					setClaimRunnerSelection(t, s, runspec.RunnerSelection{Kind: runnerKind, Value: selected})
					demand.Labels = []string{"name=other"}
					if _, err := s.ClaimUnit(t.Context(), demand); err == nil {
						t.Fatal("runner selection was ignored")
					}
					if runnerKind == runspec.RunnerName {
						setClaimRunnerSelection(t, s, runspec.RunnerSelection{Kind: runnerKind, Value: "runner"})
					} else {
						demand.Labels = append(demand.Labels, "gpu")
					}
					if _, err := s.ClaimUnit(t.Context(), demand); err != nil {
						t.Fatal(err)
					}
				})
			}
		}
	}
}

func setClaimRunnerSelection(t *testing.T, s *Store, selection runspec.RunnerSelection) {
	t.Helper()
	var body []byte
	if err := s.db.QueryRow(`SELECT request_json FROM runs WHERE id='run'`).Scan(&body); err != nil {
		t.Fatal(err)
	}
	var sealed runspec.SealedRequest
	if err := json.Unmarshal(body, &sealed); err != nil {
		t.Fatal(err)
	}
	sealed.Request.Options.Runner.Optional = runspec.Optional[runspec.RunnerSelection]{Set: true, Value: selection}
	body, err := json.Marshal(sealed)
	if err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `UPDATE runs SET request_json=? WHERE id='run'`, body)
}

func TestClaimUnitRefusesAcceptedGuardFailure(t *testing.T) {
	for _, queued := range []bool{false, true} {
		t.Run(map[bool]string{false: "named", true: "queue"}[queued], func(t *testing.T) {
			s, demand := unitClaimFixture(t)
			if queued {
				demand.Target = nil
				demand.Kinds = []capability.UnitKind{capability.UnitNode}
			}
			body, err := json.Marshal(runspec.Spec{Resolved: runspec.Resolved{Guards: runspec.Guards{When: []string{"main"}, Passed: false}}})
			if err != nil {
				t.Fatal(err)
			}
			rewriteSchemaExec(t, s.db, `UPDATE runs SET spec_json=?,spec_hash='accepted',sealed_at=1,plan_json=?,plan_hash='plan' WHERE id='run'`, body, []byte(`{"jobs":[{"id":"job"}]}`))
			if _, err = s.ClaimUnit(t.Context(), demand); err == nil {
				t.Fatal("ready job bypassed refused run guard")
			}
			rewriteSchemaCount(t, s.db, "billing_awards", 0)
			demand.Target = &UnitRef{Kind: capability.UnitTrigger, RunID: "run"}
			demand.Kinds = nil
			if _, err = s.ClaimUnit(t.Context(), demand); err != nil {
				t.Fatalf("owner cannot finish guard-refused run: %v", err)
			}
		})
	}
}
