package store

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
)

// Every table is owned by a team or by the deployment, and the port
// needs to know which for all of them. A table on neither list has been
// added without that decision being made, and a table on both has had
// it made twice.
func TestEveryTableIsClassifiedExactlyOnce(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	rows, err := s.DB().QueryContext(context.Background(),
		`SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()

	var live []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		live = append(live, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	for _, name := range live {
		tenant := slices.Contains(tenantTables, name)
		operator := slices.Contains(operatorTables, name)
		switch {
		case tenant && operator:
			t.Errorf("table %s is on both tenantTables and operatorTables", name)
		case !tenant && !operator:
			t.Errorf("table %s is on neither tenantTables nor operatorTables; "+
				"classify it before the port reaches it", name)
		}
	}
	for _, name := range slices.Concat(tenantTables, operatorTables) {
		if !slices.Contains(live, name) {
			t.Errorf("table %s is classified but absent from the schema", name)
		}
	}
	if got := len(live); got != len(tenantTables)+len(operatorTables) {
		t.Errorf("schema has %d tables, the two lists name %d",
			got, len(tenantTables)+len(operatorTables))
	}
}

// Every tenant-owned table carries the key, so a scoped query on any of
// them can be written without a join.
func TestEveryTenantTableCarriesTheTeamColumn(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ctx := context.Background()
	tx, err := s.beginTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, table := range tenantTables {
		have, err := tableColumns(ctx, tx, table)
		if err != nil {
			t.Fatalf("read columns of %s: %v", table, err)
		}
		if !have["team"] {
			t.Errorf("tenant-owned table %s has no team column", table)
		}
	}
	for _, table := range operatorTables {
		have, err := tableColumns(ctx, tx, table)
		if err != nil {
			t.Fatalf("read columns of %s: %v", table, err)
		}
		if have["team"] {
			t.Errorf("operator-owned table %s carries a team column", table)
		}
	}
}
