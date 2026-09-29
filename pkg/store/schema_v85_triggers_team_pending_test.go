package store_test

import (
	"context"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

// A v84 database gains the index the per-team pending count reads, so the
// trigger shed walks one team's pending rows rather than every team's.
func TestSchemaV85_UpgradeAddsTheTeamPendingTriggerIndex(t *testing.T) {
	ctx := context.Background()
	target := storetest.New(t)
	st := target.Open(t)
	if _, err := st.DB().ExecContext(ctx, `DROP INDEX IF EXISTS idx_triggers_team_pending`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(ctx, `DELETE FROM sparkwing_schema_version WHERE version >= 85`); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()

	st = target.Open(t)
	query := `SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`
	if st.Dialect() == store.DialectPostgres {
		query = `SELECT COUNT(*) FROM pg_indexes WHERE schemaname = current_schema() AND indexname = ?`
	}
	var indexes int
	if err := st.DB().QueryRowContext(ctx, storetest.Rebind(st, query), "idx_triggers_team_pending").Scan(&indexes); err != nil {
		t.Fatal(err)
	}
	if indexes != 1 {
		t.Fatalf("idx_triggers_team_pending count = %d after the upgrade, want 1", indexes)
	}
}

func TestSchemaV85_TeamPendingCountUsesTheIndex(t *testing.T) {
	st := storetest.OpenSQLite(t)
	rows, err := st.DB().Query(`EXPLAIN QUERY PLAN SELECT COUNT(*) FROM triggers WHERE team = 'acme' AND status = 'pending'`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(plan, "; "); !strings.Contains(got, "idx_triggers_team_pending") {
		t.Errorf("the per-team pending count plans %q, want it to use idx_triggers_team_pending", got)
	}
}
