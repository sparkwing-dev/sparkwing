package store_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func blockFinancialLedger(t *testing.T, ctx context.Context, s *store.Store, team store.Team) *runBlocker {
	t.Helper()
	s.DB().SetMaxOpenConns(8)
	b := &runBlocker{t: t, ctx: ctx, db: s.DB(), done: make(chan error, 2)}
	var err error
	b.tx, err = s.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.tx.Rollback() })
	if _, err := b.tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "sparkwing/ledger/"+string(team)); err != nil {
		t.Fatal(err)
	}
	if err := b.tx.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&b.pid); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestPostgresConcurrencyMaintenanceLocksTeamBeforeCapacity(t *testing.T) {
	f := newStoreRecoveryFixtureAt(t, true, storetest.NewPostgres(t), func(plan *nodeproto.PlanDoc) {
		plan.Jobs[0].Concurrency = []nodeproto.ConcurrencySpec{{Group: "key", Scope: new("global"), Capacity: new(1)}}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	b := blockFinancialLedger(t, ctx, f.s, f.tenant.Team())
	if _, err := b.tx.ExecContext(ctx, `SELECT name FROM teams WHERE name=$1 FOR UPDATE`, f.tenant.Team()); err != nil {
		t.Fatal(err)
	}
	b.start(func() error {
		_, err := f.s.AsOperator().MaintainConcurrency(ctx, f.lease)
		return err
	})
	b.waitBlocked(1)
	if b.held(`SELECT key FROM concurrency_entries WHERE key='job:global:key' FOR UPDATE`) {
		t.Fatal("maintenance holds capacity while waiting for its team")
	}
	b.release(1)
}

func TestPostgresSettlementsLockAllWarningTeamsBeforeLedger(t *testing.T) {
	s := storetest.OpenPostgres(t)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	if err := s.AsOperator().SetPaymentMode(ctx, capability.PaymentTest); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO teams(name,created_at,updated_at,card_customer,card_payment_method,card_fingerprint,refill_enabled,refill_amount_cents) VALUES ('a',1,1,'customer','method','card',1,1000),('z',1,1,'customer','method','card',1,1000)`,
		`INSERT INTO credit_checkouts(id,team,session_id,payment_mode,currency,amount_minor,amount_micro,opened_at,expires_at) VALUES ('one','a','session-one','test','usd',100,100000000,1,2),('two','a','session-two','test','usd',100,100000000,1,2)`,
	} {
		if _, err := s.DB().ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	paid := capability.PaymentEvent{EventID: "paid-one", CheckoutID: "session-one", PaymentID: "payment-one", Mode: capability.PaymentTest, Kind: capability.PaymentCheckoutPaid, Currency: "usd", AmountMinor: 100, Fingerprint: "card"}
	if err := s.TestOnlySettleRefund(ctx, paid); err != nil {
		t.Fatal(err)
	}
	b := blockFinancialLedger(t, ctx, s, "a")
	warning := capability.PaymentEvent{EventID: "warning", ObjectID: "warning", PaymentID: "payment-one", Mode: capability.PaymentTest, Kind: capability.PaymentWarningEvent, Fingerprint: "card", Actionable: true, Created: time.Now().Unix()}
	b.start(func() error { return s.TestOnlySettleRefund(ctx, warning) })
	b.waitBlocked(1)
	for _, team := range []string{"a", "z"} {
		if !b.held(`SELECT name FROM teams WHERE name='` + team + `' FOR KEY SHARE`) {
			t.Fatalf("warning waits for ledger without FOR UPDATE on team %s", team)
		}
	}
	paid.EventID, paid.CheckoutID, paid.PaymentID, paid.Fingerprint = "paid-two", "session-two", "payment-two", ""
	b.start(func() error { return s.TestOnlySettleRefund(ctx, paid) })
	b.waitBlocked(2)
	b.release(2)
	var balance int64
	if err := s.DB().QueryRowContext(ctx, `SELECT SUM(amount_micro) FROM credit_grants WHERE team='a'`).Scan(&balance); err != nil || balance != 100000000 {
		t.Fatalf("balance=%d: %v", balance, err)
	}
	var cards, refunds, receipts int
	if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM teams WHERE card_fingerprint<>'' OR refill_enabled<>0`).Scan(&cards); err != nil || cards != 0 {
		t.Fatalf("saved cards=%d: %v", cards, err)
	}
	if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM card_refunds WHERE team='a' AND payment_intent='payment-one' AND status='due'`).Scan(&refunds); err != nil || refunds != 1 {
		t.Fatalf("refund obligations=%d: %v", refunds, err)
	}
	if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM payment_events`).Scan(&receipts); err != nil || receipts != 3 {
		t.Fatalf("receipts=%d: %v", receipts, err)
	}
}

func TestPostgresDirectGrantRacingTeamPurge(t *testing.T) {
	s := storetest.OpenPostgres(t)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	if _, err := s.DB().ExecContext(ctx, `INSERT INTO teams(name,created_at,updated_at) VALUES ('a',1,1)`); err != nil {
		t.Fatal(err)
	}
	tenant, err := s.ForTeam(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	b := blockFinancialLedger(t, ctx, s, "a")
	b.start(func() error {
		result, err := tenant.RecordCreditGrant(ctx, store.CreditGrantRequest{Kind: store.CreditGrantFree, AmountMicro: 1, Reference: "grant"})
		if err == nil && !result.Created {
			return fmt.Errorf("grant was not created: %+v", result)
		}
		return err
	})
	b.waitBlocked(1)
	if !b.held(`SELECT name FROM teams WHERE name='a' FOR KEY SHARE`) {
		t.Fatal("direct grant waits for ledger without FOR UPDATE on team")
	}
	b.start(func() error {
		if _, _, err := s.AsOperator().RequestTeamDeletion(ctx, "a"); err != nil {
			return err
		}
		if ok, err := s.AsOperator().ClaimTeamDeletion(ctx, "a", "purger", time.Minute); err != nil || !ok {
			return fmt.Errorf("claim=%v: %w", ok, err)
		}
		return s.AsOperator().PurgeTeam(ctx, "a", "purger", time.Hour)
	})
	b.waitBlocked(2)
	b.release(2)
	for table, column := range map[string]string{"teams": "name", "credit_grants": "team"} {
		var count int
		if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table+` WHERE `+column+`='a'`).Scan(&count); err != nil || count != 0 {
			t.Errorf("%s rows=%d: %v", table, count, err)
		}
	}
	if err := s.AsOperator().SetPaymentMode(ctx, capability.PaymentLive); err == nil {
		t.Fatal("purge erased the payment mode freeze")
	}
}
