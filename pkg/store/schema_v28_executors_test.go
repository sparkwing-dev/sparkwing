package store_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestSchemaV28MigratesActualV27SQLiteShape(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schema-v27.db")
	ctx := context.Background()
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`DROP TABLE executors`,
		`ALTER TABLE nodes DROP COLUMN claim_executor`,
		`ALTER TABLE nodes DROP COLUMN claim_cores`,
		`ALTER TABLE nodes DROP COLUMN claim_memory_bytes`,
		`ALTER TABLE nodes DROP COLUMN claim_reservation`,
		`ALTER TABLE nodes DROP COLUMN claim_slot`,
		`DELETE FROM sparkwing_schema_version WHERE version >= 28`,
	} {
		if _, err := st.DB().ExecContext(ctx, stmt); err != nil {
			t.Fatalf("downgrade with %q: %v", stmt, err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	up, err := store.Open(path)
	if err != nil {
		t.Fatalf("open v27 shape at schema %d: %v", store.ExpectedSchemaVersion(), err)
	}
	defer up.Close()
	var tableSQL string
	if err := up.DB().QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'executors'`).Scan(&tableSQL); err != nil {
		t.Fatalf("executors table: %v", err)
	}
	if !strings.Contains(tableSQL, "token_prefix") || !strings.Contains(strings.ToUpper(tableSQL), "UNIQUE") {
		t.Fatalf("executors credential binding is not unique: %s", tableSQL)
	}
	rows, err := up.DB().QueryContext(ctx, `PRAGMA table_info(nodes)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var cid, notnull, pk int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &kind, &notnull, &defaultValue, &pk); err != nil {
			t.Fatal(err)
		}
		columns[name] = true
	}
	for _, name := range []string{"claim_executor", "claim_cores", "claim_memory_bytes", "claim_reservation", "claim_slot"} {
		if !columns[name] {
			t.Errorf("migrated nodes missing %s", name)
		}
	}
}
