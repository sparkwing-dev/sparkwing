package store_test

import (
	"context"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestV92NodeBounceTeamUpgradesSQLite(t *testing.T) {
	target := storetest.NewSQLite(t)
	st, err := target.TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	seedV92PreTeamBounce(t, st)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	up, err := target.TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = up.Close() }()
	assertV92NodeBounceTeam(t, up)
}

func TestV92NodeBounceTeamUpgradesPostgres(t *testing.T) {
	ctx := context.Background()
	scoped := pgTestSchemaDSN(t)
	st, err := store.OpenPostgres(ctx, scoped)
	if err != nil {
		t.Fatal(err)
	}
	seedV92PreTeamBounce(t, st)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	up, err := store.OpenPostgres(ctx, scoped)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = up.Close() }()
	assertV92NodeBounceTeam(t, up)
}

// safety: before v92 a bounce request was written with no team, so the seeded
// row is moved back to the default team the column defaulted to.
func seedV92PreTeamBounce(t *testing.T, st *store.Store) {
	t.Helper()
	ctx := t.Context()
	acme := tenantFor(t, st, "acme")
	seedTenantRun(t, acme, "run-a", "deploy")
	if err := st.CreateNode(ctx, store.Node{RunID: "run-a", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := acme.StartNode(ctx, "run-a", "build"); err != nil {
		t.Fatal(err)
	}
	if _, err := acme.RequestNodeBounce(ctx, "run-a", "build", "alice"); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`UPDATE node_bounces SET team = 'default'`,
		`DELETE FROM sparkwing_schema_version WHERE version = 92`,
	} {
		if _, err := st.DB().ExecContext(ctx, statement); err != nil {
			t.Fatalf("downgrade v92 with %q: %v", statement, err)
		}
	}
}

func assertV92NodeBounceTeam(t *testing.T, st *store.Store) {
	t.Helper()
	ctx := t.Context()
	if version, err := st.CurrentSchemaVersion(ctx); err != nil || version != store.ExpectedSchemaVersion() {
		t.Fatalf("schema version = %d, %v; want %d", version, err, store.ExpectedSchemaVersion())
	}
	acme, err := st.ForTeam(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	pending, err := acme.PendingNodeBounce(ctx, "run-a", "build")
	if err != nil || pending == nil || pending.Seq != 1 {
		t.Fatalf("the pre-v92 request after the upgrade = %+v, %v; want acme's seq 1", pending, err)
	}
	next, err := acme.RequestNodeBounce(ctx, "run-a", "build", "alice")
	if err != nil || next.Seq != 2 {
		t.Fatalf("the next bounce = %+v, %v; want seq 2", next, err)
	}
}
