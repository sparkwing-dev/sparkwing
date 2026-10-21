package store

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestNodeCancellationLeavesSiblingLeaseBoundAlone(t *testing.T) {
	s, _, _, now := renewalFixture(t, capability.UnitNode)
	expiry := now.Add(time.Hour)
	earlier := now.Add(-time.Minute)
	rewriteSchemaExec(t, s.db, `UPDATE nodes SET cancel_requested_at=?,lease_expires_at=? WHERE team='a' AND run_id='run' AND node_id='job'`, earlier.UnixNano(), expiry.UnixNano())
	rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,claimed_by,capability_prefix,lease_expires_at,generation,spec_json,execution_json) VALUES ('a','run','sibling','pending','other','other',?,1,?,?)`, expiry.UnixNano(), []byte(`{}`), []byte(`{}`))
	if err := s.inTx(t.Context(), func(tx *storeTx) error { return clampRunLeasesTx(t.Context(), tx, "a", "run", "") }); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id   string
		want time.Time
	}{{"job", earlier.Add(5 * time.Minute)}, {"sibling", expiry}} {
		var got int64
		if err := s.db.QueryRow(`SELECT lease_expires_at FROM nodes WHERE team='a' AND run_id='run' AND node_id=?`, tc.id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != tc.want.UnixNano() {
			t.Fatalf("%s lease=%d, want %d", tc.id, got, tc.want.UnixNano())
		}
	}
}
