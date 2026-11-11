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

func TestClaimSelectionSkipsFullGroupsAndReturnsWhenCapacityFrees(t *testing.T) {
	for _, scope := range []string{"global", "run", "box"} {
		t.Run(scope, func(t *testing.T) {
			s, demand := unitClaimFixture(t)
			var execution runspec.JobExecutionSettings
			var raw []byte
			if err := s.db.QueryRow(`SELECT execution_json FROM nodes WHERE node_id='job'`).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(raw, &execution); err != nil {
				t.Fatal(err)
			}
			execution.Concurrency = []runspec.Concurrency{{Group: "scan", Scope: scope, Capacity: 1, Cost: 1, OnLimit: "queue"}}
			raw, _ = json.Marshal(execution)
			rewriteSchemaExec(t, s.db, `UPDATE nodes SET execution_json=? WHERE node_id='job'`, raw)
			indexFixtureUnit(t, s, "a", UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: "job"})
			first, err := s.AsOperator().ClaimUnit(t.Context(), demand)
			if err != nil {
				t.Fatal(err)
			}
			identity := ""
			if scope == "run" {
				identity = "run"
			}
			if scope == "box" {
				identity = "runner"
			}
			key := runspec.ConcurrencyKey("job", scope, identity, "scan")
			rewriteSchemaExec(t, s.db, `INSERT INTO concurrency_entries(team,key,capacity,updated_at) VALUES('a',?,1,1)`, key)
			rewriteSchemaExec(t, s.db, `INSERT INTO concurrency_holders(team,key,holder_id,run_id,node_id,claimed_at,lease_expires_at,cost,declared_capacity,generation) VALUES('a',?,'run/job','run','job',1,?,1,1,?)`, key, s.now().Add(time.Hour).UnixNano(), first.Award.Generation)
			if err := s.inTx(t.Context(), func(tx *storeTx) error {
				ready, err := claimCapacityReadyTx(t.Context(), tx, demand.CapabilityPrefix, "a", UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: "job"})
				if err == nil && !ready {
					t.Error("retained holder blocked its own work")
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 20; i++ {
				id := fmt.Sprintf("queued-%02d", i)
				rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES('a','run',?,'pending',1,?,?)`, id, []byte(fmt.Sprintf(`{"id":%q}`, id)), raw)
				indexFixtureUnit(t, s, "a", UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: runspec.NodeID(id)})
			}
			child := execution
			child.Concurrency = nil
			childRaw, _ := json.Marshal(child)
			rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES('a','run','stamp','pending',2,?,?)`, []byte(`{"id":"stamp"}`), childRaw)
			indexFixtureUnit(t, s, "a", UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: "stamp"})
			demand.Target = &UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: "queued-00"}
			if _, err := s.AsOperator().ClaimUnit(t.Context(), demand); !errors.Is(err, ErrNotFound) {
				t.Fatalf("named full group claim: %v", err)
			}
			demand.Target = nil
			demand.Kinds = []capability.UnitKind{capability.UnitNode}
			got, err := s.AsOperator().ClaimUnit(t.Context(), demand)
			if err != nil || got.NodeID != "stamp" {
				t.Fatalf("full group starved child: claim=%s error=%v", got.NodeID, err)
			}
			var waiting int
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM nodes WHERE node_id LIKE 'queued-%' AND claimed_by IS NULL`).Scan(&waiting); err != nil || waiting != 20 {
				t.Fatalf("queued fanout left store: unclaimed=%d error=%v", waiting, err)
			}
			if _, err := s.AsOperator().ClaimUnit(t.Context(), demand); !errors.Is(err, ErrNotFound) {
				t.Fatalf("full group awarded work: %v", err)
			}
			rewriteSchemaExec(t, s.db, `DELETE FROM concurrency_holders WHERE team='a' AND key=?`, key)
			other := authorityRoot("other-runner", "a")
			other.Grants = []capability.Grant{capability.NodesClaim}
			authorityPersist(t, s, other)
			demand.CapabilityPrefix = other.Prefix
			got, err = s.AsOperator().ClaimUnit(t.Context(), demand)
			if err != nil || got.NodeID != "queued-00" {
				t.Fatalf("freed group did not return in arrival order to another runner: claim=%s error=%v", got.NodeID, err)
			}
		})
	}
}
