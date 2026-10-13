package store

import (
	"slices"
)

// AllTenantTablesForTest names every tenant-owned table, for the external
// test package to prove a team purge leaves none of them holding the team.
func AllTenantTablesForTest() []string { return slices.Clone(tenantTables) }
