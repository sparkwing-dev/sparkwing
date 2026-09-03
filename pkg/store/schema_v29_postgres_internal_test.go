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

func TestSchemaV30PostgresRetryColumnsUseNativeTypes(t *testing.T) {
	want := map[string]map[string]string{
		"runs": {
			"retry_cause_node_id": "TEXT NOT NULL DEFAULT ''", "retry_avoid_coordinator_id": "TEXT NOT NULL DEFAULT ''",
			"retry_avoid_executor_kind": "TEXT NOT NULL DEFAULT ''", "retry_avoid_executor_id": "TEXT NOT NULL DEFAULT ''",
			"retry_avoid_until": "BIGINT",
		},
		"nodes-attempt": {
			"coordinator_id": "TEXT NOT NULL DEFAULT ''", "executor_kind": "TEXT NOT NULL DEFAULT ''",
			"executor_id": "TEXT NOT NULL DEFAULT ''", "execution_started_at": "BIGINT",
			"reservation_id": "TEXT NOT NULL DEFAULT ''", "avoid_coordinator_id": "TEXT NOT NULL DEFAULT ''",
			"avoid_executor_kind": "TEXT NOT NULL DEFAULT ''", "avoid_executor_id": "TEXT NOT NULL DEFAULT ''",
			"avoid_until": "BIGINT",
		},
		"nodes-retry": {
			"claim_generation": "BIGINT NOT NULL DEFAULT 0", "claim_membership_id": "TEXT NOT NULL DEFAULT ''",
			"attempts_consumed": "BIGINT NOT NULL DEFAULT 0", "retry_root_run_id": "TEXT NOT NULL DEFAULT ''",
			"executor_location": "TEXT NOT NULL DEFAULT 'unknown'", "required_coordinator_id": "TEXT NOT NULL DEFAULT ''",
			"required_executor_location": "TEXT NOT NULL DEFAULT ''",
		},
		"triggers": {"available_at": "BIGINT NOT NULL DEFAULT 0"},
	}
	got := map[string]map[string]string{
		"runs": runAgentRetryColsPostgres, "nodes-attempt": nodeAgentAttemptColsPostgres,
		"nodes-retry": nodeAgentRetryColsPostgres, "triggers": triggerAvailableColsPostgres,
	}
	for group, columns := range want {
		if len(got[group]) != len(columns) {
			t.Errorf("Postgres %s columns = %d, want %d", group, len(got[group]), len(columns))
		}
		for name, wantType := range columns {
			if gotType := got[group][name]; gotType != wantType {
				t.Errorf("Postgres %s.%s type = %q, want %q", group, name, gotType, wantType)
			}
		}
	}
	for name, definition := range map[string]string{
		"agent_loss_retries":      agentLossRetriesTablePostgres,
		"node_execution_attempts": nodeExecutionAttemptsTablePostgres,
	} {
		upper := strings.ToUpper(definition)
		if strings.Contains(upper, " BLOB") || strings.Contains(upper, " INTEGER") {
			t.Errorf("Postgres %s retained a SQLite type: %s", name, definition)
		}
	}
}

func TestSchemaV30PostgresFreshRetryColumnsUseNativeTypes(t *testing.T) {
	groups := map[string]map[string]string{
		"runs":     runAgentRetryColsPostgres,
		"nodes":    mergeColumnTypes(nodeAgentAttemptColsPostgres, nodeAgentRetryColsPostgres),
		"triggers": triggerAvailableColsPostgres,
	}
	for tableName, columns := range groups {
		table := postgresTableDefinition(t, tableName)
		for name, wantType := range columns {
			pattern := `(?m)^\s*` + regexp.QuoteMeta(name) + `\s+` + regexp.QuoteMeta(wantType) + `\s*,?\s*$`
			if !regexp.MustCompile(pattern).MatchString(table) {
				t.Errorf("fresh Postgres %s.%s does not use %q", tableName, name, wantType)
			}
		}
	}
}

func mergeColumnTypes(groups ...map[string]string) map[string]string {
	out := make(map[string]string)
	for _, group := range groups {
		for name, definition := range group {
			out[name] = definition
		}
	}
	return out
}

func postgresTableDefinition(t *testing.T, name string) string {
	t.Helper()
	start := strings.Index(schemaPostgres, "CREATE TABLE IF NOT EXISTS "+name+" (")
	if start < 0 {
		t.Fatalf("Postgres schema has no %s table", name)
	}
	table := schemaPostgres[start:]
	end := strings.Index(table, ");")
	if end < 0 {
		t.Fatalf("Postgres %s table is unterminated", name)
	}
	return table[:end]
}
