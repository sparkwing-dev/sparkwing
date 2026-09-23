package store

import (
	"strings"
	"testing"
)

// safety: every schema version that declares no requirement must appear here, and a
// version adding no column maps to nil, so a new migration fails the test below until
// its author has classified it.
var additiveColumnSources = map[int][]map[string]string{
	1:  columnSpecMaps(),
	2:  columnSpecMaps(),
	3:  columnSpecMaps(),
	4:  nil,
	5:  columnSpecMaps(),
	6:  columnSpecMaps(),
	7:  nil,
	8:  {pipelineProfilesCPUMeasuredCols},
	9:  {pipelineProfilesWaitCols},
	10: {pipelineProfilesContendedCols},
	11: {pipelineProfilesVersioningCols},
	12: {triggerRepoInheritedCols},
	13: {triggerSubmissionCols},
	14: {pipelineProfilesSustainedCols},
	15: {nodesUsageCols, nodeMetricsCPUTimeCols},
	16: nil,
	17: nil,
	18: nil,
	19: {usersScopesCols},
	20: {nodeDispatchRedactionCols},
	23: {secretsSharedCols, triggerClaimOwnerCols},
	24: {triggerWebhookDeliveryCols},
	25: {triggerWebhookReplayKeyCols},
	// safety: v28 adds a foreign key and an index, not a column, and this
	// guard only inspects columns; an older binary never writes a
	// node_metrics row for a run that does not exist, so the constraint it
	// has never heard of cannot refuse one of its inserts.
	28: nil,
	29: {nodesOrderCols},
	// safety: v32 adds two tables of its own and no column, and nothing older
	// reads them, so an older binary keeps writing the migrated database.
	32: nil,
	// safety: v35 adds two tables nothing older reads, plus two defaulted
	// columns, so an older binary keeps writing the migrated database.
	35: {tokensMeteredCols, nodesCreditCols},
	36: {nodePlacementCols, nodePlacementColsPostgres},
	// safety: v37 adds one table nothing older reads and no column, so an
	// older binary keeps writing the migrated database.
	37: nil,
	// safety: v38 adds three tables nothing older reads and two indexes, and
	// no column, so an older binary keeps writing the migrated database.
	38: nil,
	// safety: v39 adds indexes and no column, so an older binary keeps
	// writing the migrated database and never sees them.
	39: nil,
	// safety: v40 adds indexes and, with a default each, every column those
	// indexes name, so an older binary keeps writing the migrated database.
	40: {computeGuardRunsCols, computeGuardNodesCols},
	// safety: v41 adds one table nothing older reads and no column, so an
	// older binary keeps writing the migrated database.
	41: nil,
	// safety: v42 adds the reversed-reference column with a default and a
	// unique index over references a grant already carries, so an older
	// binary keeps writing the migrated database.
	42: {creditGrantReversesCols},
	// safety: v43 adds one index and no column, so an older binary keeps
	// writing the migrated database.
	43: nil,
	// safety: v44 is permanently spent and adds no column, so an older binary
	// keeps writing the migrated database.
	44: nil,
	// safety: v45 adds one defaulted column, so an older binary keeps writing
	// the migrated database and simply never stamps it.
	45: {nodesCreditExhaustionCols},
	// safety: v46 adds the cpu class and the rate each charge was billed at,
	// and the class a node was claimed at, all defaulted, so an older binary
	// keeps writing the migrated database.
	46: {creditChargeClassCols, nodesCreditClassCols},
	// safety: v47 adds the storage allowance beside the quota, and the team
	// and bytes a storage charge billed, all defaulted, so an older binary
	// keeps writing the migrated database.
	47: {storageQuotaAllowanceCols, creditChargeStorageCols},
	// safety: v49 adds the tenant key to every tenant-owned table with a
	// default naming the team the existing rows are backfilled into, so an
	// older binary keeps writing the migrated database and its inserts land
	// in that team.
	49: {teamColumn},
	// safety: v50 adds the per-team credit exhaustion marker with a default,
	// and moves the deployment-wide stamp into it, so an older binary keeps
	// writing the migrated database and simply never stamps the column.
	50: {teamsCreditExhaustedCols},
	// safety: v52 adds the identity tables and columns with defaults, so an
	// older binary keeps writing sessions, tokens and teams it never tags
	// with an account, and runs whose event counters it never bumps.
	52: {teamIdentityCols, sessionAccountCols, tokenCreatorCols, runEventUsageCols},
	// safety: v53 adds a trigger's open credit reservation with a default of
	// none, so an older binary keeps claiming and finishing triggers; it opens
	// no reservation and the next claim overwrites one it left open.
	53: {triggersCreditCols},
	// safety: v54 adds the GitHub App tables and no column, and nothing older
	// reads them, so an older binary keeps writing the migrated database.
	54: nil,
	// safety: v55 adds a nullable emailed_at to invitations and a defaulted
	// teams_created to accounts, which an older binary leaves at their
	// defaults, and tables it never reads.
	55: {invitationEmailCols, accountTeamsCreatedCols},
	// safety: v56 adds the free_slots table and an index, and nothing older
	// reads them, so an older binary keeps writing the migrated database.
	56: nil,
	// safety: v57 is reserved and intentionally empty.
	57: nil,
	// safety: v58 adds an account's waitlist stamp with a default of never
	// waitlisted, so an older binary keeps creating and reading accounts; an
	// account it creates is admitted, as it would have been before the gate.
	58: {accountWaitlistCols},
	// safety: v59 adds one defaulted node column an older binary never names
	// and rewrites one setting's value in place, so an older binary keeps
	// writing the migrated database; a node it claims bills from execution
	// start, as it always did.
	59: {nodesCreditBillingCols},
	60: nil,
	61: nil,
	// safety: v62 adds the git credential and GitHub App extra repository
	// tables and no column, and nothing older reads them, so an older binary
	// keeps writing the migrated database.
	62: nil,
	// safety: v63 adds defaulted trigger columns an older binary never names
	// and an index, so an older binary keeps writing the migrated database.
	63: {triggerGitHubCheckRunCols},
	// safety: v64 adds a defaulted identities column an older binary never
	// names and two tables nothing older reads, so an older binary keeps
	// writing the migrated database.
	64: {identityLinkedCols},
	65: {githubAppBranchFilterCols},
}

func columnSpecMaps() []map[string]string {
	out := make([]map[string]string, 0, len(columnMigrations))
	for _, spec := range columnMigrations {
		out = append(out, spec.cols)
	}
	return out
}

func unwritableColumns(cols map[string]string) map[string]string {
	bad := map[string]string{}
	for name, def := range cols {
		upper := strings.ToUpper(def)
		if strings.Contains(upper, "NOT NULL") && !strings.Contains(upper, "DEFAULT") {
			bad[name] = def
		}
	}
	return bad
}

// A migration that declares no requirement promises that a binary predating it
// keeps writing the migrated database. A column it adds must therefore supply a
// value for the rows that binary inserts without naming it: NOT NULL with no
// DEFAULT breaks that promise at the first insert. A migration that needs such
// a column declares a requirement instead.
func TestAdditiveMigrationsAddNoUnwritableColumn(t *testing.T) {
	for version := 1; version <= expectedSchemaVersion; version++ {
		if len(migrationRequirements[version]) > 0 {
			continue
		}
		sources, ok := additiveColumnSources[version]
		if !ok {
			t.Errorf("schema v%d declares no requirement and is absent from additiveColumnSources; "+
				"add it (nil when it adds no column) so its columns are checked", version)
			continue
		}
		for _, cols := range sources {
			for name, def := range unwritableColumns(cols) {
				t.Errorf("schema v%d adds column %s as %q: NOT NULL with no DEFAULT, "+
					"which an older binary's insert cannot satisfy; give it a default or "+
					"declare a requirement for v%d", version, name, def, version)
			}
		}
	}
}

func TestAdditiveColumnSourcesCoversOnlyNonDeclaringVersions(t *testing.T) {
	for version := range additiveColumnSources {
		if version < 1 || version > expectedSchemaVersion {
			t.Errorf("additiveColumnSources names v%d, outside 1..%d", version, expectedSchemaVersion)
		}
		if len(migrationRequirements[version]) > 0 {
			t.Errorf("v%d declares requirement(s) %v, so it does not belong in additiveColumnSources",
				version, migrationRequirements[version])
		}
	}
}

// The guard has to be able to fail, so drive the same rule with a column an
// older binary could not write.
func TestUnwritableColumnsRejectsNotNullWithoutDefault(t *testing.T) {
	got := unwritableColumns(map[string]string{
		"needs_a_value": "TEXT NOT NULL",
		"defaulted":     "TEXT NOT NULL DEFAULT ''",
		"nullable":      "BLOB",
	})
	if len(got) != 1 || got["needs_a_value"] != "TEXT NOT NULL" {
		t.Fatalf("unwritableColumns = %v, want only needs_a_value", got)
	}
}
