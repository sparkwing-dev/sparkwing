package store

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func TestOwnerNodeReadDoesNotLoadUnrelatedJobs(t *testing.T) {
	tenant, actor := ownerPlacementFixture(t)
	s := tenant.s
	s.claimProfiles = claimProfileFake{profile: match.Profile{}}
	rewriteSchemaExec(t, s.db, `UPDATE nodes SET spec_hash='declaration'`)
	rewriteSchemaExec(t, s.db, `WITH RECURSIVE more(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM more WHERE i<199) INSERT INTO nodes(team,run_id,node_id,status,spec_json,spec_hash,execution_json,claim_requirements,claim_context) SELECT team,run_id,'wide-'||i,'pending',spec_json,spec_hash,execution_json,claim_requirements,claim_context FROM nodes CROSS JOIN more WHERE team='a' AND run_id='run' AND node_id='job'`)
	trace := countClaimVisits(t, s, func(string) {}, nil)
	trace.enabled, trace.allStatements = true, true
	node, err := tenant.ReadOwnerNode(t.Context(), actor.Authority, runspec.NodeRef{RunID: "run", NodeID: "job"})
	if err != nil {
		t.Fatal(err)
	}
	if node.Ref.NodeID != "job" || node.OwnerExecution != nil {
		t.Fatalf("selected node: %+v", node)
	}
	reads, placement := 0, 0
	for _, statement := range trace.statements {
		if strings.HasPrefix(statement.query, "SELECT "+nodeReadColumns+" FROM nodes ") {
			reads++
		}
		if strings.Contains(statement.query, "work_input AS MATERIALIZED") {
			placement++
		}
	}
	if reads != 1 {
		t.Errorf("single-node lookup read %d job rows, want 1", reads)
	}
	if placement != 0 {
		t.Errorf("single-node lookup made %d placement queries, want 0", placement)
	}
}
