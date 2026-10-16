package store_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func dollarsMicro(n int64) int64 { return n * 100 * store.MicroCreditsPerCent }

func payTeam(t *testing.T, tenant *store.Tenant, ref string, dollars int64) time.Time {
	t.Helper()
	res, err := tenant.TestOnlySettlePaidCredit(context.Background(), store.CreditGrantRequest{
		Kind: store.CreditGrantPaid, AmountMicro: dollarsMicro(dollars), Reference: ref, CreatedBy: "billing",
	})
	if err != nil {
		t.Fatalf("pay %s: %v", ref, err)
	}
	return res.Grant.CreatedAt
}

func spend(t *testing.T, s *store.Store, team, id string, cents int64) {
	t.Helper()
	now := time.Now().UnixNano()
	amount := cents * store.MicroCreditsPerCent
	if _, err := s.DB().Exec(fmt.Sprintf(
		`INSERT INTO credit_charges (id, run_id, node_id, capability_prefix, kind, seconds, amount_micro, charged_at, team)
		 VALUES ('%s', 'run-x', 'build', 'pfx', 'usage', 60, %d, %d, '%s')`,
		id, amount, now, team)); err != nil {
		t.Fatalf("seed a charge: %v", err)
	}
	if _, err := s.DB().Exec(fmt.Sprintf(
		`INSERT INTO team_spend_days (team, day, amount_micro) VALUES ('%s', %d, %d)
		 ON CONFLICT (team, day) DO UPDATE SET amount_micro = team_spend_days.amount_micro + excluded.amount_micro`,
		team, now/int64(24*time.Hour), amount)); err != nil {
		t.Fatalf("seed a spend day: %v", err)
	}
}

func billingStanding(t *testing.T, tenant *store.Tenant, now time.Time) store.BillingStanding {
	t.Helper()
	b, err := tenant.BillingStanding(context.Background(), now)
	if err != nil {
		t.Fatalf("standing: %v", err)
	}
	return b
}
