package store

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func TestLauncherSnapshotRejectsMalformedScalarWithoutLosingHealthyAwards(t *testing.T) {
	s, demand, first, _ := launcherSnapshotFixture(t, capability.UnitNode)
	rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) SELECT team,run_id,'sibling','pending',1,spec_json,execution_json FROM nodes WHERE team='a' AND run_id='run' AND node_id='job'`)
	demand.Target.NodeID = "sibling"
	second, err := s.AsOperator().ClaimUnit(t.Context(), demand)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.exec(t.Context(), `UPDATE nodes SET ready_at=CASE WHEN node_id='job' THEN 2 ELSE 'damaged' END WHERE team='a' AND run_id='run' AND node_id IN ('job','sibling')`); err == nil {
		t.Fatal("accepted malformed ready_at alongside a healthy award")
	}
	for _, nodeID := range []string{"job", "sibling"} {
		var readyAt int64
		if err := s.db.QueryRowContext(t.Context(), `SELECT ready_at FROM nodes WHERE team=? AND run_id=? AND node_id=?`, "a", "run", nodeID).Scan(&readyAt); err != nil || readyAt != 1 {
			t.Fatalf("rejected update changed %s: ready_at=%d, %v", nodeID, readyAt, err)
		}
	}
	ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: demand.CapabilityPrefix}
	rows, _, err := s.AsOperator().ReconcileLauncherAwards(t.Context(), ref, nil, nil, nil, nil, nil)
	if err != nil || len(rows) != 2 {
		t.Fatalf("snapshot=%+v, %v", rows, err)
	}
	want := map[string]runstate.ClaimAward{first.NodeID: first.Award, second.NodeID: second.Award}
	for _, row := range rows {
		award, ok := want[row.Claim.NodeID]
		if !ok || row.Failure != nil || row.Gone || row.Claim.Award != award {
			t.Fatalf("healthy award changed after rejected scalar damage: %+v", row)
		}
		delete(want, row.Claim.NodeID)
	}
	if len(want) != 0 {
		t.Fatalf("missing healthy awards: %+v", want)
	}
}
