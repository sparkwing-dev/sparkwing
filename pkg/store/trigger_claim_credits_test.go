package store_test

import (
	"context"

	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func pendingTrigger(t *testing.T, st *store.Store, id string) {
	t.Helper()
	if err := teststore.ForTeam(t, st, store.DefaultTeam).TestOnlySeedTrigger(context.Background(), store.Trigger{
		ID: id, Pipeline: "build", Status: "pending", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
}

func unmeteredClaimant(t *testing.T, s *store.Store, principal string) store.ClaimIdentity {
	t.Helper()
	_, tok, err := s.CreateTokenWith(context.Background(), principal, store.TokenKindRunner,
		[]string{"triggers.claim"}, 0, time.Now(), store.TokenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return store.ClaimIdentity{Principal: principal, TokenPrefix: tok.Prefix}
}

func balance(t *testing.T, st *store.Store) int64 {
	t.Helper()
	b, err := tenant(t, st, store.DefaultTeam).CreditBalanceMicro(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func pastTriggerMinimum(extra time.Duration) time.Time {
	return time.Now().Add(-store.MinBillableSeconds*time.Second - extra)
}

func backdateTriggerClaim(t *testing.T, st *store.Store, id string, reservedAt, lease time.Time) {
	t.Helper()
	query, args := `UPDATE triggers SET credit_reserved_at = ? WHERE id = ?`, []any{reservedAt.UnixNano(), id}
	if !lease.IsZero() {
		query = `UPDATE triggers SET credit_reserved_at = ?, lease_expires_at = ? WHERE id = ?`
		args = []any{reservedAt.UnixNano(), lease.UnixNano(), id}
	}
	if _, err := st.DB().Exec(storetest.Rebind(st, query), args...); err != nil {
		t.Fatal(err)
	}
}
