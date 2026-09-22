package store

import (
	"slices"
	"strings"
	"testing"
)

// safety: this list shrinks and never grows. A table leaves it in the
// commit that gives it a writer, and the pinned size moves with it, so a
// table cannot be added without a reviewer seeing the number move.
var tablesWithNoTeamWriter = []string{
	"agent_loss_retries",
	"agent_loss_retry_legacy_deny_all",
	"agent_loss_retry_node_sources",
	"approvals",
	"debug_pauses",
	"egress_usage",
	"events",
	"node_bounces",
	"node_claim_offers",
	"node_dispatches",
	"node_execution_attempts",
	"node_metrics",
	"node_steps",
	"run_definition_plans",
	"storage_month_usage",
	"storage_quotas",
	"storage_run_usage",
	"users",
}

// safety: pins the backlog's length so it can only shrink.
const tablesWithNoTeamWriterSize = 18

// A predicate is only as real as the data under it. The scope guard beside
// this one reads statement text, so it can prove a team predicate exists
// and never that anything ever wrote the column it reads: three tables
// carried the key for a full release with no writer that set it, and every
// guard reading them answered about the default team alone.
//
// This is the per-table half. A tenant-owned table needs at least one
// INSERT in this package that names `team`, or its rows all land on the
// default and a scoped read of it is a scoped read of nothing. One writer
// that names the key satisfies the whole table, so a second writer on the
// same table that drops it still passes here; the per-statement half is
// the scope guard beside this one, once its backlog is gone.
func TestTenantWriters_EveryTenantTableHasAWriterThatSetsTheKey(t *testing.T) {
	written, inserts := tablesInsertedWithTeam(t)
	if inserts < 30 {
		t.Fatalf("guard found only %d INSERTs into tenant-owned tables; it is no longer reading this package", inserts)
	}
	for _, table := range tenantTables {
		if written[table] {
			continue
		}
		if slices.Contains(tablesWithNoTeamWriter, table) {
			continue
		}
		t.Errorf("no INSERT in this package sets %s.team, so every row lands on %q "+
			"and any predicate reading the column answers about that team only.\n"+
			"Give the table a writer, the way createRunTx carries the team and CreateNode "+
			"takes it off the run that owns the node.", table, DefaultTeam)
	}
	for _, table := range tablesWithNoTeamWriter {
		if !slices.Contains(tenantTables, table) {
			t.Errorf("tablesWithNoTeamWriter names %q, which is not a tenant-owned table", table)
		}
		if written[table] {
			t.Errorf("tablesWithNoTeamWriter still carries %q, which now has a writer; "+
				"delete the line and lower tablesWithNoTeamWriterSize", table)
		}
	}
}

func TestTenantWriters_BacklogOnlyShrinks(t *testing.T) {
	if len(tablesWithNoTeamWriter) != tablesWithNoTeamWriterSize {
		t.Fatalf("tablesWithNoTeamWriter has %d entries and tablesWithNoTeamWriterSize says %d; "+
			"giving a table a writer lowers both, and nothing raises them",
			len(tablesWithNoTeamWriter), tablesWithNoTeamWriterSize)
	}
	seen := map[string]bool{}
	for _, table := range tablesWithNoTeamWriter {
		if seen[table] {
			t.Errorf("tablesWithNoTeamWriter names %q twice", table)
		}
		seen[table] = true
	}
}

// safety: reads the INSERT column lists rather than the WHERE clauses,
// because a write carries its key there and nowhere else, and returns the
// count so an empty parse fails loudly instead of passing every table.
func tablesInsertedWithTeam(t *testing.T) (map[string]bool, int) {
	t.Helper()
	written := map[string]bool{}
	inserts := 0
	for _, stmt := range tenantSQLStatements(t) {
		for _, m := range insertColumnsRe.FindAllStringSubmatch(stmt.text, -1) {
			table := strings.ToLower(insertTarget(m[0]))
			if !slices.Contains(tenantTables, table) {
				continue
			}
			inserts++
			if slices.ContainsFunc(strings.Split(m[1], ","), func(c string) bool {
				return strings.EqualFold(strings.Trim(strings.TrimSpace(c), `"`), "team")
			}) {
				written[table] = true
			}
		}
	}
	return written, inserts
}

func insertTarget(match string) string {
	fields := strings.Fields(strings.ReplaceAll(match, "(", " ("))
	if len(fields) < 3 {
		return ""
	}
	return fields[2]
}
