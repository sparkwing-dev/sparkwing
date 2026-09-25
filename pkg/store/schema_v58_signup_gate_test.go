package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

// Accounts that exist before the gate arrives must come through the upgrade
// admitted: the new column defaults to "never waitlisted".
func TestSchemaV58KeepsExistingAccountsAdmitted(t *testing.T) {
	ctx := context.Background()
	target := storetest.New(t)
	st, err := target.TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`DROP INDEX IF EXISTS idx_accounts_waitlisted`,
		`DROP INDEX IF EXISTS idx_accounts_created`,
		`DROP TABLE signup_gate`,
		`ALTER TABLE accounts DROP COLUMN waitlisted_at`,
		`ALTER TABLE accounts DROP COLUMN waitlist_reason`,
		`DELETE FROM sparkwing_schema_version WHERE version >= 58`,
	} {
		if _, err := st.DB().ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if _, err := st.DB().ExecContext(ctx, storetest.Rebind(st, `
		INSERT INTO accounts (id, email, email_verified, name, active_team, created_at, updated_at)
		VALUES ('acct-old', 'old@example.com', 1, 'Old', '', 1, 1)`)); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	up, err := target.TryOpen()
	if err != nil {
		t.Fatalf("upgrade to v58: %v", err)
	}
	defer func() { _ = up.Close() }()
	if got := readSchemaVersion(t, up.DB()); got != store.ExpectedSchemaVersion() {
		t.Fatalf("schema version = %d, want %d", got, store.ExpectedSchemaVersion())
	}
	acct, err := up.Account(ctx, "acct-old")
	if err != nil || acct.Waitlisted {
		t.Fatalf("existing account after the upgrade = %+v, %v", acct, err)
	}
	if waiting, err := up.WaitlistedAccounts(ctx, 0); err != nil || len(waiting) != 0 {
		t.Fatalf("waitlist after the upgrade = %+v, %v", waiting, err)
	}
	if _, err := up.SetSignUpMode(ctx, store.SignUpWaitlist, "", "root", time.Now()); err != nil {
		t.Fatal(err)
	}
}
