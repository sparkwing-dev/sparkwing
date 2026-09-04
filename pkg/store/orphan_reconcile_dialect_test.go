package store

import (
	"strings"
	"testing"
)

func TestOrphanedRunsQueryUsesGreatestOnPostgres(t *testing.T) {
	sqlite := (&Store{dialect: DialectSQLite}).orphanedRunsQuery()
	postgres := (&Store{dialect: DialectPostgres}).orphanedRunsQuery()

	if !strings.Contains(sqlite, "AND max(") {
		t.Errorf("SQLite query lost its scalar max():\n%s", sqlite)
	}
	if strings.Contains(postgres, "AND max(") {
		t.Errorf("Postgres query calls max() in WHERE, where max is an aggregate:\n%s", postgres)
	}
	if !strings.Contains(postgres, "AND GREATEST(") {
		t.Errorf("Postgres query does not use GREATEST:\n%s", postgres)
	}
	for _, q := range []string{sqlite, postgres} {
		if !strings.Contains(q, "SELECT MAX(last_heartbeat) FROM nodes") {
			t.Errorf("query lost the per-node heartbeat aggregate:\n%s", q)
		}
	}
}
