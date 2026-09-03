package store

import (
	"regexp"
	"strings"
	"testing"
)

func TestSchemaV29PostgresNodeOfferColumnsUseNativeTypes(t *testing.T) {
	want := map[string]string{
		"prefers_labels":         "BYTEA",
		"requested_cores":        "DOUBLE PRECISION NOT NULL DEFAULT 0",
		"requested_memory_bytes": "BIGINT NOT NULL DEFAULT 0",
		"requested_slots":        "BIGINT NOT NULL DEFAULT 1",
		"offer_started_at":       "BIGINT",
		"offer_priority_target":  "BIGINT NOT NULL DEFAULT 100",
		"claim_base_priority":    "BIGINT NOT NULL DEFAULT 0",
		"claim_priority":         "BIGINT NOT NULL DEFAULT 0",
		"claim_worker_id":        "TEXT NOT NULL DEFAULT ''",
		"claim_executor_kind":    "TEXT NOT NULL DEFAULT ''",
		"claim_reservation_id":   "TEXT NOT NULL DEFAULT ''",
	}
	if len(nodesOfferColsPostgres) != len(want) {
		t.Fatalf("Postgres node offer columns = %d, want %d", len(nodesOfferColsPostgres), len(want))
	}
	for name, wantType := range want {
		got, ok := nodesOfferColsPostgres[name]
		if !ok {
			t.Errorf("Postgres node offer columns missing %s", name)
			continue
		}
		if got != wantType {
			t.Errorf("Postgres node offer column %s type = %q, want %q", name, got, wantType)
		}
		upper := strings.ToUpper(got)
		if strings.Contains(upper, "BLOB") || strings.Contains(upper, "INTEGER") || upper == "REAL" {
			t.Errorf("Postgres node offer column %s retained SQLite type %q", name, got)
		}
	}
}

func TestSchemaV29PostgresFreshNodeOfferColumnsUseNativeTypes(t *testing.T) {
	start := strings.Index(schemaPostgres, "CREATE TABLE IF NOT EXISTS nodes (")
	if start < 0 {
		t.Fatal("Postgres schema has no nodes table")
	}
	table := schemaPostgres[start:]
	end := strings.Index(table, ");")
	if end < 0 {
		t.Fatal("Postgres nodes table is unterminated")
	}
	table = table[:end]
	for name, wantType := range nodesOfferColsPostgres {
		pattern := `(?m)^\s*` + regexp.QuoteMeta(name) + `\s+` + regexp.QuoteMeta(wantType) + `\s*,?\s*$`
		if !regexp.MustCompile(pattern).MatchString(table) {
			t.Errorf("fresh Postgres nodes.%s does not use %q", name, wantType)
		}
	}
}
