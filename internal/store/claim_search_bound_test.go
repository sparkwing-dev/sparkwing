package store

import (
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"modernc.org/sqlite"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func countClaimVisits(t *testing.T, s *Store, nodeVisit, runVisit func(string)) {
	t.Helper()
	if s.dialect != DialectSQLite {
		t.Skip("readiness predicate instrumentation requires SQLite")
	}
	path := filepath.Join(t.TempDir(), "claims.db")
	rewriteSchemaExec(t, s.db, `VACUUM INTO ?`, path)
	probe := fmt.Sprintf("claim_search_probe_%x", time.Now().UnixNano())
	if err := sqlite.RegisterScalarFunction(probe, 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		nodeVisit(args[0].(string))
		return int64(1), nil
	}); err != nil {
		t.Fatal(err)
	}
	var runProbe string
	if runVisit != nil {
		runProbe = probe + "_run"
		if err := sqlite.RegisterScalarFunction(runProbe, 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			runVisit(args[0].(string))
			return int64(1), nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	sql.Register(probe, &claimBenchmarkDriver{Driver: s.db.Driver(), probe: probe, runProbe: runProbe, trace: &claimBenchmarkTrace{examine: true}})
	db, err := sql.Open(probe, sqliteDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	s.db = db
}

func TestClaimSearchCappedBacklogDoesNotExamineNodes(t *testing.T) {
	var baseline [2]int64
	for _, backlog := range []int{1, 4000} {
		t.Run(fmt.Sprint(backlog), func(t *testing.T) {
			s, demand := launcherFairnessFixture(t)
			var raw []byte
			if err := s.db.QueryRow(`SELECT request_json FROM runs WHERE id='run'`).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var sealed runspec.SealedRequest
			if err := json.Unmarshal(raw, &sealed); err != nil {
				t.Fatal(err)
			}
			zero := int64(0)
			sealed.Request.Burst = &runspec.BurstPlacement{Preferences: []runspec.PlacementStep{{Target: "cloud"}}, OverflowMinutes: &zero}
			raw, err := json.Marshal(sealed)
			if err != nil {
				t.Fatal(err)
			}
			rewriteSchemaExec(t, s.db, `UPDATE runs SET request_json=? WHERE id='run'`, raw)
			rewriteSchemaExec(t, s.db, `UPDATE nodes SET node_id='capped-0000' WHERE team='a'`)
			indexFixtureUnit(t, s, "a", UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: "capped-0000"})
			if backlog > 1 {
				rewriteSchemaExec(t, s.db, `WITH RECURSIVE backlog(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM backlog WHERE i<?) INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json,claim_requirements,claim_context) SELECT team,run_id,printf('capped-%04d',i),'pending',1,spec_json,execution_json,claim_requirements,claim_context FROM nodes CROSS JOIN backlog WHERE team='a' AND node_id='capped-0000'`, backlog-1)
			}
			rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json,claim_requirements,claim_context) SELECT team,run_id,'next','pending',2,?,execution_json,claim_requirements,claim_context FROM nodes WHERE team='b' AND node_id='job'`, []byte(`{"id":"next"}`))
			var capped, total atomic.Int64
			countClaimVisits(t, s, func(id string) {
				total.Add(1)
				if strings.HasPrefix(id, "capped-") {
					capped.Add(1)
				}
			}, nil)
			for poll := range 2 {
				capped.Store(0)
				total.Store(0)
				claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
				if err != nil || claim.Team != "b" {
					t.Fatalf("poll %d: capped team hid healthy work: %+v %v", poll, claim, err)
				}
				if got := capped.Load(); got != 0 {
					t.Errorf("poll %d examined %d capped backlog nodes; want no node visits for a refused team", poll, got)
				}
				got := total.Load()
				if backlog == 1 {
					baseline[poll] = got
				} else if got != baseline[poll] {
					t.Errorf("poll %d examined %d nodes with %d capped nodes; want %d visits as with one capped node", poll, got, backlog, baseline[poll])
				}
			}
		})
	}
}

func TestClaimSearchActionsFindsBoundPushBehindOlderRuns(t *testing.T) {
	s, demand := unitClaimFixture(t)
	actions := authorityRoot("actions", "a")
	actions.Grants = []capability.Grant{capability.NodesClaim}
	actions.Repositories = []string{"gh:1"}
	actions.Binding = capability.Binding{Kind: capability.BindingPush, Push: &capability.PushScope{RepositoryKey: "gh:1", Ref: "refs/heads/main", SHA: fixtureFeatureSHA, WorkflowRunID: "workflow", WorkflowRunAttempt: "1", WorkflowRef: "acme/repo/.github/workflows/sparkwing.yaml@refs/heads/main"}}
	authorityPersist(t, s, actions)
	provenance, git := referenceAppProvenance(t, "refs/heads/main", fixtureOtherSHA)
	git.Repository.Display = "https://github.com/acme/repo"
	raw := sealWith(t, runspec.SealedRequest{Request: runspec.Request{Git: git}}, provenance)
	rewriteSchemaExec(t, s.db, `UPDATE runs SET request_json=?,git_branch=?,git_sha=? WHERE id='run'`, raw, git.Ref, git.SHA)
	rewriteSchemaExec(t, s.db, `UPDATE nodes SET node_id='older-0' WHERE run_id='run'`)
	indexFixtureUnit(t, s, "a", UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: "older-0"})
	for i := 1; i <= 8; i++ {
		run := fmt.Sprintf("push-%d", i)
		node := fmt.Sprintf("older-%d", i)
		body := raw
		if i == 8 {
			node = "matching"
			p, g := referenceAppProvenance(t, "refs/heads/main", fixtureFeatureSHA)
			g.Repository.Display = git.Repository.Display
			body = sealWith(t, runspec.SealedRequest{Request: runspec.Request{Git: g}}, p)
		}
		rewriteSchemaExec(t, s.db, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,repo_display,repo_identity,dispatch,intake_mode,allowed_pools,created_principal,created_at,log_retention_class,log_retention_days) SELECT team,?,?,pipeline,status,?,'request',1,repo_key,repo_display,repo_identity,dispatch,intake_mode,allowed_pools,created_principal,?,log_retention_class,log_retention_days FROM runs WHERE id='run'`, run, i+1, body, i+1)
		rewriteSchemaExec(t, s.db, `INSERT INTO triggers(team,id,unit_kind,available_at,created_at,expires_at,needs_labels,prefers_labels) SELECT team,?,unit_kind,?,?,expires_at,needs_labels,prefers_labels FROM triggers WHERE id='run'`, run, i+1, i+1)
		rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) SELECT team,?,?,'pending',?,spec_json,execution_json FROM nodes WHERE run_id='run'`, run, node, i+1)
		indexFixtureUnit(t, s, "a", UnitRef{Kind: capability.UnitNode, RunID: runspec.RunID(run), NodeID: runspec.NodeID(node)})
	}
	var older, total, unrelatedRuns atomic.Int64
	countClaimVisits(t, s, func(id string) {
		total.Add(1)
		if strings.HasPrefix(id, "older-") {
			older.Add(1)
		}
	}, func(id string) {
		if id != "push-8" {
			unrelatedRuns.Add(1)
		}
	})
	demand.CapabilityPrefix, demand.Target = actions.Prefix, nil
	demand.Kinds = []capability.UnitKind{capability.UnitNode}
	claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
	if err != nil || claim.NodeID != "matching" {
		t.Errorf("first poll did not reach bound SHA behind eight older runs: %+v %v", claim, err)
	}
	if got := older.Load(); got != 0 {
		t.Errorf("examined %d nodes from other pushes; want direct bound-push lookup", got)
	}
	if got := total.Load(); got > 2 {
		t.Errorf("examined %d nodes; want at most the selected indexed node and award recheck", got)
	}
	if got := unrelatedRuns.Load(); got != 0 {
		t.Errorf("examined %d unrelated runs preparing funding; want only the bound push", got)
	}
}

func TestClaimSearchPreservesTeamFairShareBeforeArrival(t *testing.T) {
	s, demand := launcherFairnessFixture(t)
	rewriteSchemaExec(t, s.db, `UPDATE nodes SET ready_at=100 WHERE team='b'`)
	rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json,claim_requirements,claim_context) SELECT team,run_id,'next','pending',ready_at+1,?,execution_json,claim_requirements,claim_context FROM nodes`, []byte(`{"id":"next"}`))
	var examined atomic.Int64
	countClaimVisits(t, s, func(string) { examined.Add(1) }, nil)
	var visits [4]int64
	for poll, team := range []Team{"a", "b", "a", "b"} {
		examined.Store(0)
		claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
		if err != nil || claim.Team != team {
			t.Fatalf("poll %d: claim=%+v error=%v; want team %s", poll, claim, err, team)
		}
		visits[poll] = examined.Load()
	}
	// A healthy team with two ready nodes needs six predicate evaluations, including rechecks.
	for poll, got := range visits {
		if got > 2*6 {
			t.Errorf("poll %d examined %d nodes; want at most two teams' six predicate evaluations", poll, got)
		}
	}
}

func TestClaimSearchDoesNotExamineLaterFairShareTeams(t *testing.T) {
	s, demand := launcherFairnessFixture(t)
	rewriteSchemaExec(t, s.db, `UPDATE claim_awards SET ordinal=2`)
	rewriteSchemaExec(t, s.db, `UPDATE teams SET claim_award_ordinal=CASE name WHEN 'a' THEN 1 ELSE 2 END`)
	rewriteSchemaExec(t, s.db, `UPDATE nodes SET node_id='later-team' WHERE team='b'`)
	var later atomic.Int64
	countClaimVisits(t, s, func(id string) {
		if id == "later-team" {
			later.Add(1)
		}
	}, nil)
	claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
	if err != nil || claim.Team != "a" {
		t.Fatalf("claim=%+v error=%v; want team a", claim, err)
	}
	if got := later.Load(); got != 0 {
		t.Fatalf("examined %d nodes from a later fair-share team", got)
	}
}

func TestClaimSearchTiedTeamsKeepArrivalOrder(t *testing.T) {
	s, demand := launcherFairnessFixture(t)
	rewriteSchemaExec(t, s.db, `UPDATE nodes SET ready_at=2 WHERE team='a'`)
	claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
	if err != nil || claim.Team != "b" {
		t.Fatalf("claim=%+v error=%v; want older work from tied team b", claim, err)
	}
}

func TestClaimSearchNarrowRunDoesNotExamineOtherRuns(t *testing.T) {
	for _, claimant := range []string{"host", "named", "here"} {
		t.Run(claimant, func(t *testing.T) {
			s, demand := unitClaimFixture(t)
			switch claimant {
			case "host":
				host, err := s.AsOperator().ClaimUnit(t.Context(), UnitDemand{CapabilityPrefix: demand.CapabilityPrefix, Target: &UnitRef{Kind: capability.UnitTrigger, RunID: "run"}})
				if err != nil {
					t.Fatal(err)
				}
				demand.CapabilityPrefix, demand.Target = host.Award.CapabilityPrefix, nil
			case "here":
				root := authorityRoot("requester", "a")
				root.Grants = []capability.Grant{capability.RunsWrite, capability.TriggersClaim}
				authorityPersist(t, s, root)
				rewriteSchemaExec(t, s.db, `UPDATE triggers SET created_at=?`, s.now().UnixNano())
				var here capability.Issued
				if err := s.inTx(t.Context(), func(tx *storeTx) error {
					var err error
					here, err = s.issue(t.Context(), tx, capability.IssueRequest{Kind: capability.Award, Award: &capability.AwardSpec{Claimant: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Prefix}, Unit: capability.UnitTrigger, UnitID: "run", HereRunID: "run", Purpose: capability.PurposeHere, Delivery: capability.DeliveryDirect}})
					return err
				}); err != nil {
					t.Fatal(err)
				}
				demand.CapabilityPrefix = here.Capability.Prefix
				demand.Target = &UnitRef{Kind: capability.UnitTrigger, RunID: "run"}
			}
			rewriteSchemaExec(t, s.db, `UPDATE nodes SET ready_at=100`)
			for i := range 8 {
				run, node := fmt.Sprintf("older-run-%d", i), fmt.Sprintf("older-%d", i)
				rewriteSchemaExec(t, s.db, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,repo_display,repo_identity,dispatch,intake_mode,allowed_pools,created_principal,created_at,log_retention_class,log_retention_days) SELECT team,?,?,pipeline,status,request_json,'request',1,repo_key,repo_display,repo_identity,dispatch,intake_mode,allowed_pools,created_principal,?,log_retention_class,log_retention_days FROM runs WHERE id='run'`, run, i+2, i+1)
				rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) SELECT team,?,?,'pending',?,spec_json,execution_json FROM nodes WHERE run_id='run'`, run, node, i+1)
				indexFixtureUnit(t, s, "a", UnitRef{Kind: capability.UnitNode, RunID: runspec.RunID(run), NodeID: runspec.NodeID(node)})
			}
			var older, total, unrelatedRuns atomic.Int64
			countClaimVisits(t, s, func(id string) {
				total.Add(1)
				if strings.HasPrefix(id, "older-") {
					older.Add(1)
				}
			}, func(id string) {
				if id != "run" {
					unrelatedRuns.Add(1)
				}
			})
			demand.Kinds = []capability.UnitKind{capability.UnitNode}
			wantNode := "job"
			if claimant == "here" {
				demand.Kinds, wantNode = []capability.UnitKind{capability.UnitTrigger}, ""
			}
			claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
			if err != nil || claim.RunID != "run" || claim.NodeID != wantNode {
				t.Errorf("first poll did not reach the selected run behind eight older runs: %+v %v", claim, err)
			}
			if got := older.Load(); got != 0 {
				t.Errorf("examined %d nodes from other runs; want direct bound-run lookup", got)
			}
			if got := total.Load(); got > 2 {
				t.Errorf("examined %d nodes; want at most the selected indexed node and award recheck", got)
			}
			if got := unrelatedRuns.Load(); got != 0 {
				t.Errorf("examined %d unrelated runs preparing funding; want only the bound run", got)
			}
		})
	}
}
