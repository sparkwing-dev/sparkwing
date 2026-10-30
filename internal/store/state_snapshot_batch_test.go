package store

import (
	"reflect"
	"strings"
	"testing"
)

func TestReadSnapshotLoadsNodeRowsTogether(t *testing.T) {
	tenant, _, _ := stateFixture(t)
	s := tenant.s
	rewriteSchemaExec(t, s.db, `WITH RECURSIVE more(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM more WHERE i<199) INSERT INTO nodes(team,run_id,node_id,status,spec_json,execution_json,seq) SELECT 'a','run','wide-'||i,'pending',?, ?,i FROM more`, []byte(`{}`), []byte(`{}`))
	rewriteSchemaExec(t, s.db, `INSERT INTO node_attempts(team,run_id,node_id,attempt,generation,holder,capability_prefix,executor_kind,executor_name,executor_location,started_at,finished_at,outcome) VALUES ('a','run','job',1,1,'old','old','runner','runner','remote',1,2,'failed'),('a','run','job',2,1,'old','old','runner','runner','remote',3,4,'failed')`)
	trace := countClaimVisits(t, s, func(string) {}, nil)
	trace.enabled, trace.allStatements = true, true
	snapshot, err := tenant.ReadSnapshot(t.Context(), "run")
	trace.enabled = false
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Nodes) != 200 {
		t.Fatalf("snapshot has %d nodes, want 200", len(snapshot.Nodes))
	}
	reads, retries := 0, 0
	for _, statement := range trace.statements {
		if strings.HasPrefix(statement.query, "SELECT "+nodeReadColumns+" FROM nodes ") {
			reads++
		}
		if strings.HasPrefix(statement.query, "SELECT COALESCE(SUM(n-1),0) FROM") {
			retries++
		}
	}
	if reads != 1 || retries != 0 {
		t.Errorf("snapshot made %d node row queries and %d standalone retry queries, want 1 and 0", reads, retries)
	}
	for _, node := range snapshot.Nodes {
		point, err := tenant.ReadNode(t.Context(), node.Ref)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(node, point) {
			t.Fatalf("snapshot differs from point read for %s: %+v / %+v", node.Ref.NodeID, node, point)
		}
		want := 0
		if node.Ref.NodeID == "job" {
			want = 1
		}
		if node.ApplicationRetries != want {
			t.Fatalf("node %s retries=%d, want %d", node.Ref.NodeID, node.ApplicationRetries, want)
		}
	}
}
