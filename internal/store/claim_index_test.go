package store

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestCloudNamedRecheckSeeksTheTenantUnitKey(t *testing.T) {
	s, demand := launcherFairnessFixture(t)
	demand.Target = &UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: "job"}
	err := s.inTx(t.Context(), func(tx *storeTx) error {
		q := claimSQL{}
		query, err := claimQueryTx(t.Context(), tx, demand, demand.Kinds, &q)
		if err != nil {
			return err
		}
		rows, err := tx.QueryContext(t.Context(), `EXPLAIN QUERY PLAN `+query, q.args...)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		var plan []string
		found := false
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				return err
			}
			plan = append(plan, detail)
			if strings.Contains(detail, "SEARCH n ") && strings.Contains(detail, "team=? AND run_id=? AND node_id=?") {
				found = true
			}
			if strings.HasPrefix(detail, "SCAN n") {
				t.Errorf("named recheck scans nodes: %s", detail)
			}
		}
		if !found {
			t.Errorf("named recheck lacks a complete tenant/unit index seek: %v", plan)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
}
