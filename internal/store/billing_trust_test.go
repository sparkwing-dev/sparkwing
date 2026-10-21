package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
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

func billingStanding(t *testing.T, tenant *store.Tenant, now time.Time) store.BillingStanding {
	t.Helper()
	b, err := tenant.BillingStanding(context.Background())
	if err != nil {
		t.Fatalf("standing: %v", err)
	}
	return b
}
