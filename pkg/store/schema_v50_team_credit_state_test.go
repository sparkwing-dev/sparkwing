package store_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

// safety: dropping the column is what makes reopening the database run v50
// against the shape v49 left behind.
func downgradeTeamCreditStateToV49(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	for _, q := range []string{
		`ALTER TABLE teams DROP COLUMN credit_exhausted_at`,
		`DELETE FROM sparkwing_schema_version WHERE version >= 50`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

func metaValue(t *testing.T, st *store.Store, key string) (string, bool) {
	t.Helper()
	var value string
	err := st.DB().QueryRow(storetest.Rebind(st,
		`SELECT value FROM sparkwing_meta WHERE key = ?`), key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", false
	}
	if err != nil {
		t.Fatalf("read %s: %v", key, err)
	}
	return value, true
}

// The install this release upgrades carries one deployment-wide exhaustion
// stamp and one watermark per principal. Both have to arrive on the default
// team in the same migration that starts reading them that way, because a
// stamp left behind reads as a team that never ran out and a watermark left
// behind forgives the interval since the last storage pass.
func TestSchemaV50MovesCreditStateOntoTheTeamRow(t *testing.T) {
	ctx := context.Background()
	target := storetest.New(t)
	st, err := target.TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	downgradeTeamCreditStateToV49(t, st.DB())
	if got := readSchemaVersion(t, st.DB()); got != 49 {
		t.Fatalf("schema version after the downgrade = %d, want 49", got)
	}
	const stamp = 1_700_000_000_000_000_000
	for _, seed := range []struct{ key, value string }{
		{"credit_exhausted_at", "1700000000000000000"},
		{"storage_charged_through/acme", "1699999999000000000"},
	} {
		if _, err := st.DB().ExecContext(ctx, storetest.Rebind(st,
			`INSERT INTO sparkwing_meta (key, value, updated_at) VALUES (?, ?, 1)`),
			seed.key, seed.value); err != nil {
			t.Fatalf("seed %s: %v", seed.key, err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	up, err := target.TryOpen()
	if err != nil {
		t.Fatalf("upgrade v49 to v50: %v", err)
	}
	defer func() { _ = up.Close() }()
	if got := readSchemaVersion(t, up.DB()); got != store.ExpectedSchemaVersion() {
		t.Fatalf("schema version = %d, want %d", got, store.ExpectedSchemaVersion())
	}

	var at int64
	if err := up.DB().QueryRow(storetest.Rebind(up,
		`SELECT credit_exhausted_at FROM teams WHERE name = ?`),
		string(store.DefaultTeam)).Scan(&at); err != nil {
		t.Fatalf("read the default team's stamp: %v", err)
	}
	if at != stamp {
		t.Errorf("the default team's stamp = %d, want the %d it carried in the bag", at, stamp)
	}
	state, err := up.CreditState(ctx, 0)
	if err != nil {
		t.Fatalf("credit state: %v", err)
	}
	if state.ExhaustedAt == nil || state.ExhaustedAt.UnixNano() != stamp {
		t.Errorf("CreditState reports ExhaustedAt %v, want the migrated stamp", state.ExhaustedAt)
	}
	if _, found := metaValue(t, up, "credit_exhausted_at"); found {
		t.Error("the deployment-wide stamp survived in sparkwing_meta")
	}
	if _, found := metaValue(t, up, "storage_charged_through/acme"); found {
		t.Error("the watermark kept its team-less key")
	}
	value, found := metaValue(t, up, "storage_charged_through/"+string(store.DefaultTeam)+"/acme")
	if !found {
		t.Fatal("the watermark did not move under the default team")
	}
	if value != "1699999999000000000" {
		t.Errorf("the moved watermark reads %q, want the instant it was stamped at", value)
	}
}
