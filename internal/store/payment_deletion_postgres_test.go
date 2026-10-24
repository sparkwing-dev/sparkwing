package store_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func TestPostgresWarningArrivingDuringPurgeCannotOrphanRefund(t *testing.T) {
	s := storetest.OpenPostgres(t)
	s.DB().SetMaxOpenConns(8)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	for _, query := range []string{
		`INSERT INTO teams(name,created_at,updated_at) VALUES ('a',1,1)`,
		`INSERT INTO credit_checkouts(id,team,session_id,payment_mode,currency,amount_minor,amount_micro,opened_at,expires_at) VALUES ('checkout','a','session','test','usd',100,100000000,1,2)`,
	} {
		if _, err := s.DB().ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	paid := capability.PaymentEvent{EventID: "paid", CheckoutID: "session", PaymentID: "payment", Mode: capability.PaymentTest, Kind: capability.PaymentCheckoutPaid, Currency: "usd", AmountMinor: 100}
	if err := s.TestOnlySettleRefund(ctx, paid); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AsOperator().RequestTeamDeletion(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.AsOperator().ClaimTeamDeletion(ctx, "a", "purger", time.Minute); err != nil || !ok {
		t.Fatalf("claim=%v: %v", ok, err)
	}
	b := &runBlocker{t: t, ctx: ctx, db: s.DB(), done: make(chan error, 2)}
	var err error
	b.tx, err = s.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.tx.Rollback() }()
	// Pause after purge eligibility, before the checkout can be removed.
	if _, err := b.tx.ExecContext(ctx, `LOCK TABLE credit_checkouts IN SHARE MODE`); err != nil {
		t.Fatal(err)
	}
	if err := b.tx.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&b.pid); err != nil {
		t.Fatal(err)
	}
	b.start(func() error { return s.AsOperator().PurgeTeam(ctx, "a", "purger", time.Hour) })
	b.waitBlocked(1)
	warning := capability.PaymentEvent{EventID: "warning", ObjectID: "warning", PaymentID: "payment", Mode: capability.PaymentTest, Kind: capability.PaymentWarningEvent, Actionable: true, Created: time.Now().Unix()}
	b.start(func() error {
		err := s.TestOnlySettleRefund(ctx, warning)
		if !errors.Is(err, store.ErrTeamBeingDeleted) {
			return fmt.Errorf("warning during purge: %v", err)
		}
		return nil
	})
	b.waitBlocked(2)
	b.release(2)
	for _, table := range []string{"credit_checkouts", "credit_grants", "card_refunds", "payment_warnings"} {
		var count int
		if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&count); err != nil || count != 0 {
			t.Errorf("%s retained %d rows: %v", table, count, err)
		}
	}
}
