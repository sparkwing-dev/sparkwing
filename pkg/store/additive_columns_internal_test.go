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
