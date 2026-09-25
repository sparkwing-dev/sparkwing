package store_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestV73TriggerCreditCursorUpgradeSQLite(t *testing.T) {
	assertV73TriggerCreditCursorUpgrade(t, storetest.NewSQLite(t))
}

func TestV73TriggerCreditCursorUpgradePostgres(t *testing.T) {
	assertV73TriggerCreditCursorUpgrade(t, storetest.NewPostgres(t))
}

func assertV73TriggerCreditCursorUpgrade(t *testing.T, target *storetest.Target) {
	t.Helper()
	st, err := target.TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	downgradeTriggerCreditCursor(t, st)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	up, err := target.TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = up.Close() }()
	if got, err := up.CurrentSchemaVersion(context.Background()); err != nil || got != 73 {
		t.Fatalf("upgraded schema = %d, %v; want 73", got, err)
	}
	rows, err := up.DB().QueryContext(t.Context(),
		`SELECT credit_paid_seconds, credit_paid_amount_micro, credit_reservation_id FROM triggers WHERE 1 = 0`)
	if err != nil {
		t.Fatalf("cursor columns: %v", err)
	}
	_ = rows.Close()
	var indexes int
	indexQuery := `SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_credit_charges_team_amount'`
	if up.Dialect() == store.DialectPostgres {
		indexQuery = `SELECT COUNT(*) FROM pg_indexes WHERE schemaname = current_schema()
			AND tablename = 'credit_charges' AND indexname = 'idx_credit_charges_team_amount'`
	}
	if err := up.DB().QueryRowContext(t.Context(), indexQuery).Scan(&indexes); err != nil || indexes != 1 {
		t.Fatalf("team charge index count = %d, %v; want 1", indexes, err)
	}
	requirements, err := up.Requirements(t.Context())
	if err != nil || !slices.Contains(requirements, "trigger-credit-cursor-v1") {
		t.Fatalf("upgraded requirements = %v, %v", requirements, err)
	}
}

func TestV73TriggerCreditCursorRequiresDrainedClaimsSQLite(t *testing.T) {
	target := storetest.NewSQLite(t)
	st, err := target.TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateTrigger(t.Context(), store.Trigger{
		ID: "active-metered", Pipeline: "build", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	downgradeTriggerCreditCursor(t, st)
	if _, err := st.DB().ExecContext(t.Context(),
		`UPDATE triggers SET status = 'claimed', claimed_at = ?, lease_expires_at = ?, credit_reserved_at = ? WHERE id = ?`,
		time.Now().UnixNano(), time.Now().Add(time.Minute).UnixNano(), time.Now().UnixNano(), "active-metered"); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, err := target.TryOpen(); err == nil || !strings.Contains(err.Error(), "drain 1 active metered trigger") {
		if reopened != nil {
			_ = reopened.Close()
		}
		t.Fatalf("upgrade with active metered claim = %v, want drain refusal", err)
	}
}

func downgradeTriggerCreditCursor(t *testing.T, st *store.Store) {
	t.Helper()
	for _, statement := range []string{
		`DROP INDEX IF EXISTS idx_credit_charges_team_amount`,
		`ALTER TABLE triggers DROP COLUMN credit_paid_seconds`,
		`ALTER TABLE triggers DROP COLUMN credit_paid_amount_micro`,
		`ALTER TABLE triggers DROP COLUMN credit_reservation_id`,
		`DELETE FROM sparkwing_requirements WHERE name = 'trigger-credit-cursor-v1'`,
		`DELETE FROM sparkwing_schema_version WHERE version = 73`,
	} {
		if _, err := st.DB().ExecContext(t.Context(), statement); err != nil {
			t.Fatalf("downgrade v73 with %q: %v", statement, err)
		}
	}
}
