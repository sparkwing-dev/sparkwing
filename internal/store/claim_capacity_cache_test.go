package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func cachedClaimGroupFixture(t *testing.T, capacity int) (*Store, UnitDemand) {
	t.Helper()
	s, demand := unitClaimFixture(t)
	raw := []byte(fmt.Sprintf(`{"concurrency":[{"group":"scan","scope":"global","capacity":%d,"cost":1,"on_limit":"queue"}]}`, capacity))
	rewriteSchemaExec(t, s.db, `UPDATE nodes SET execution_json=? WHERE node_id='job'`, raw)
	indexFixtureUnit(t, s, "a", UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: "job"})
	first, err := s.AsOperator().ClaimUnit(t.Context(), demand)
	if err != nil {
		t.Fatal(err)
	}
	key := runspec.ConcurrencyKey("job", "global", "", "scan")
	rewriteSchemaExec(t, s.db, `INSERT INTO concurrency_entries(team,key,capacity,updated_at) VALUES('a',?,?,1)`, key, capacity)
	rewriteSchemaExec(t, s.db, `INSERT INTO concurrency_holders(team,key,holder_id,run_id,node_id,claimed_at,lease_expires_at,cost,declared_capacity,generation) VALUES('a',?,'run/job','run','job',1,?,1,?,?)`, key, s.now().Add(time.Hour).UnixNano(), capacity, first.Award.Generation)
	demand.Target = &UnitRef{Kind: capability.UnitTrigger, RunID: "run"}
	owner, err := s.AsOperator().ClaimUnit(t.Context(), demand)
	if err != nil {
		t.Fatal(err)
	}
	demand.CapabilityPrefix = owner.Award.CapabilityPrefix
	demand.Target = nil
	demand.Kinds = []capability.UnitKind{capability.UnitNode}
	return s, demand
}

func addCachedClaimJob(t *testing.T, s *Store, id string, ready int64, capacity, cost int, timeout *int64) {
	t.Helper()
	execution := runspec.JobExecutionSettings{Concurrency: []runspec.Concurrency{{Group: "scan", Scope: "global", Capacity: capacity, Cost: cost, OnLimit: "queue", QueueTimeoutMS: timeout}}}
	raw, err := json.Marshal(execution)
	if err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES('a','run',?,'pending',?,?,?)`, id, ready, []byte(fmt.Sprintf(`{"id":%q}`, id)), raw)
	indexFixtureUnit(t, s, "a", UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: runspec.NodeID(id)})
}

func TestClaimCapacitySearchReclaimsRetainedHolder(t *testing.T) {
	for _, bound := range []bool{false, true} {
		t.Run(fmt.Sprintf("bound=%t", bound), func(t *testing.T) {
			s, demand := cachedClaimGroupFixture(t, 1)
			if !bound {
				demand.CapabilityPrefix = "runner"
			}
			// safety: the run owner's lease keeps the retained permit live while its node is unclaimed.
			rewriteSchemaExec(t, s.db, `UPDATE nodes SET claimed_by=NULL,capability_prefix='',lease_expires_at=NULL,status='pending',ready_at=2 WHERE team='a' AND run_id='run' AND node_id='job'`)
			addCachedClaimJob(t, s, "blocked", 1, 1, 1, nil)
			// safety: the older sibling seeds the full-key exclusion before the holder reaches search.
			claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
			if err != nil || claim.NodeID != "job" {
				t.Fatalf("full-key exclusion hid retained holder: claim=%s error=%v", claim.NodeID, err)
			}
		})
	}
}

func TestClaimCapacityCachePreservesWeightedBackfill(t *testing.T) {
	s, demand := cachedClaimGroupFixture(t, 2)
	addCachedClaimJob(t, s, "heavy", 1, 2, 2, nil)
	addCachedClaimJob(t, s, "cheap", 2, 2, 1, nil)
	claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
	if err != nil || claim.NodeID != "cheap" {
		t.Fatalf("cached full demand blocked weighted backfill: %s %v", claim.NodeID, err)
	}
}

func TestClaimCapacityCacheLeavesOversizeValidationToAdmission(t *testing.T) {
	s, demand := cachedClaimGroupFixture(t, 1)
	addCachedClaimJob(t, s, "blocked", 1, 1, 1, nil)
	addCachedClaimJob(t, s, "oversize", 2, 1, 2, nil)
	claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
	if err != nil || claim.NodeID != "oversize" {
		t.Fatalf("full-key exclusion hid invalid cost from admission: %s %v", claim.NodeID, err)
	}
}

func TestClaimCapacityCachePreservesEveryTimedSiblingDeadline(t *testing.T) {
	s, demand := cachedClaimGroupFixture(t, 1)
	addCachedClaimJob(t, s, "untimed", 1, 1, 1, nil)
	timeout := int64(1000)
	for _, id := range []string{"timed-a", "timed-b"} {
		addCachedClaimJob(t, s, id, 2, 1, 1, &timeout)
	}
	now := s.now()
	s.clock = func() time.Time { return now }
	for _, elapsed := range []time.Duration{0, 999 * time.Millisecond, time.Second} {
		s.clock = func() time.Time { return now.Add(elapsed) }
		if _, err := s.AsOperator().ClaimUnit(t.Context(), demand); !errors.Is(err, ErrNotFound) {
			t.Fatalf("full group claim: %v", err)
		}
		tenant, err := s.ForTeam(t.Context(), "a")
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{"timed-a", "timed-b"} {
			row, err := tenant.ReadNode(t.Context(), runspec.NodeRef{RunID: "run", NodeID: runspec.NodeID(id)})
			if err != nil {
				t.Fatal(err)
			}
			if elapsed < time.Second && row.Status != "pending" {
				t.Fatalf("premature deadline: %+v", row)
			}
			if elapsed == time.Second && (row.Outcome != "failed" || row.FailureReason != FailureQueueTimeout || row.Attempt != 0) {
				t.Fatalf("cached full key lost sibling deadline: %+v", row)
			}
		}
	}
}

func TestClaimCapacityCacheKeepsRunScopesSeparate(t *testing.T) {
	s, demand := unitClaimFixture(t)
	raw := []byte(`{"concurrency":[{"group":"scan","scope":"run","capacity":1,"cost":1,"on_limit":"queue"}]}`)
	rewriteSchemaExec(t, s.db, `UPDATE nodes SET execution_json=? WHERE node_id='job'`, raw)
	indexFixtureUnit(t, s, "a", UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: "job"})
	first, err := s.AsOperator().ClaimUnit(t.Context(), demand)
	if err != nil {
		t.Fatal(err)
	}
	key := runspec.ConcurrencyKey("job", "run", "run", "scan")
	rewriteSchemaExec(t, s.db, `INSERT INTO concurrency_entries(team,key,capacity,updated_at) VALUES('a',?,1,1)`, key)
	rewriteSchemaExec(t, s.db, `INSERT INTO concurrency_holders(team,key,holder_id,run_id,node_id,claimed_at,lease_expires_at,cost,declared_capacity,generation) VALUES('a',?,'run/job','run','job',1,?,1,1,?)`, key, s.now().Add(time.Hour).UnixNano(), first.Award.Generation)
	rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES('a','run','blocked','pending',1,?,?)`, []byte(`{"id":"blocked"}`), raw)
	indexFixtureUnit(t, s, "a", UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: "blocked"})
	rewriteSchemaExec(t, s.db, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,created_principal,created_at,log_retention_class,log_retention_days) SELECT team,'other-run',number+1,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,created_principal,created_at,log_retention_class,log_retention_days FROM runs WHERE team='a' AND id='run'`)
	rewriteSchemaExec(t, s.db, `INSERT INTO triggers(team,id,unit_kind,available_at,created_at,expires_at,needs_labels,prefers_labels) SELECT team,'other-run',unit_kind,available_at,created_at,expires_at,needs_labels,prefers_labels FROM triggers WHERE team='a' AND id='run'`)
	rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES('a','other-run','other-job','pending',2,?,?)`, []byte(`{"id":"other-job"}`), raw)
	indexFixtureUnit(t, s, "a", UnitRef{Kind: capability.UnitNode, RunID: "other-run", NodeID: "other-job"})
	demand.Target = nil
	demand.Kinds = []capability.UnitKind{capability.UnitNode}
	claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
	if err != nil || claim.RunID != "other-run" || claim.NodeID != "other-job" {
		t.Fatalf("cached full key escaped its run scope: %s/%s %v", claim.RunID, claim.NodeID, err)
	}
}
