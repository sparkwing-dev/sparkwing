package store

import "slices"

// TenantTablesForTest names the tenant-owned tables for the external
// test package, which needs them to reproduce the shape a store had
// before the tenant key was added.
func TenantTablesForTest() []string { return slices.Clone(tenantTables) }
