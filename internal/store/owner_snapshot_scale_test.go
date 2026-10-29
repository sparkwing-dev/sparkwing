package store

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func TestOwnerSnapshotPlacementWorkDoesNotMultiplyByJobCount(t *testing.T) {
	tenant, actor := ownerPlacementFixture(t)
	s := tenant.s
	s.claimProfiles = claimProfileFake{profile: match.Profile{}}
	rewriteSchemaExec(t, s.db, `UPDATE nodes SET spec_hash='declaration'`)
	want := map[runspec.NodeID]bool{"job": true}
	for i := 0; i < 199; i++ {
		id := fmt.Sprintf("wide-%03d", i)
		execution := []byte(`{}`)
		want[runspec.NodeID(id)] = i%3 != 0
		if i%3 == 0 {
			execution = []byte(`{"requires":{"value":["gpu"]}}`)
		}
		rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,spec_json,spec_hash,execution_json,claim_requirements,claim_context) SELECT team,run_id,?,'pending',?,'declaration',?,claim_requirements,claim_context FROM nodes WHERE team='a' AND run_id='run' AND node_id='job'`, id, []byte(fmt.Sprintf(`{"id":%q}`, id)), execution)
		indexFixtureUnit(t, s, "a", UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: runspec.NodeID(id)})
	}
	run, err := tenant.ReadRun(t.Context(), "run")
	if err != nil {
		t.Fatal(err)
	}
	spec, err := json.Marshal(runspec.Spec{Request: run.Request, Resolved: runspec.Resolved{Args: map[string]string{"payload": strings.Repeat("x", 164000)}, Guards: runspec.Guards{Passed: true}}})
	if err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `UPDATE runs SET spec_json=?,spec_hash='spec',sealed_at=1,plan_json=x'7b7d',plan_hash='plan' WHERE team='a' AND id='run'`, spec)
	trace := countClaimVisits(t, s, func(string) {}, nil)
	trace.enabled = true
	snapshot, err := tenant.ReadOwnerSnapshot(t.Context(), actor.Authority, "run")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Nodes) != 200 {
		t.Fatalf("nodes=%d", len(snapshot.Nodes))
	}
	for _, node := range snapshot.Nodes {
		if node.OwnerExecution == nil || node.OwnerExecution.Allowed != want[node.Ref.NodeID] {
			t.Fatalf("wrong placement for %s", node.Ref.NodeID)
		}
	}
	count := 0
	for _, statement := range trace.statements {
		if strings.Contains(statement.query, "work_input AS MATERIALIZED") {
			count++
		}
	}
	if count > 1 {
		t.Fatalf("snapshot compiled %d placement queries for 200 jobs; want one shared eligibility read", count)
	}
}
