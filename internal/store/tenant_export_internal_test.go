package store

import (
	"slices"
)

// AllTenantTablesForTest names every tenant-owned table, for the external
// test package to check purge scope.
func AllTenantTablesForTest() []string { return slices.Clone(tenantTables) }

// RetainedTenantTablesForTest names financial tables that outlive team deletion.
func RetainedTenantTablesForTest() []string {
	var names []string
	for _, table := range schemaTables {
		if table.RetainOnTeamDeletion {
			names = append(names, table.Name)
		}
	}
	return names
}
