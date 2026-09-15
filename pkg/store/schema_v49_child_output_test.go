package store_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func downgradeChildOutputGrantToV48(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, statement := range []string{
		`ALTER TABLE triggers DROP COLUMN requested_output_node_id`,
		`DELETE FROM sparkwing_requirements WHERE name = 'bound-child-output-grants'`,
		`DELETE FROM sparkwing_schema_version WHERE version >= 49`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
}

func assertChildOutputGrantPersists(t *testing.T, st *store.Store) {
	t.Helper()
	ctx := context.Background()
	if err := st.CreateTrigger(ctx, store.Trigger{
		ID: "child", Pipeline: "demo", Status: "pending", CreatedAt: time.Now(),
		ParentRunID: "parent", ParentNodeID: "build", RequestedOutputNodeID: "artifact",
	}); err != nil {
		t.Fatal(err)
	}
	trigger, err := st.GetTrigger(ctx, "child")
	if err != nil {
		t.Fatal(err)
	}
	if trigger.RequestedOutputNodeID != "artifact" {
		t.Fatalf("requested output = %q, want artifact", trigger.RequestedOutputNodeID)
	}
}

func TestSchemaV49UpgradesChildOutputGrantSQLite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v48.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	downgradeChildOutputGrantToV48(t, st.DB())
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	up, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = up.Close() }()
	assertChildOutputGrantPersists(t, up)
}

func TestSchemaV49UpgradesChildOutputGrantPostgres(t *testing.T) {
	dsn := pgTestSchemaDSN(t)
	ctx := context.Background()
	st, err := store.OpenPostgres(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	downgradeChildOutputGrantToV48(t, st.DB())
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	up, err := store.OpenPostgres(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = up.Close() }()
	assertChildOutputGrantPersists(t, up)
}
