package store_test

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestPostgresPaymentStateSerializesBeforePayerAssociation(t *testing.T) {
	s := storetest.NewPostgres(t).Open(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	for _, query := range []string{
		`INSERT INTO teams(name,created_at,updated_at) VALUES ('a',1,1)`,
		`INSERT INTO credit_checkouts(id,team,session_id,payment_mode,currency,amount_minor,amount_micro,opened_at,expires_at) VALUES ('checkout','a','session','test','usd',100,100000000,1,2)`,
	} {
		if _, err := s.DB().ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	blocker, err := s.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback() }()
	if _, err := blocker.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('sparkwing/payment/test/payment',0))`); err != nil {
		t.Fatal(err)
	}
	var blockerPID int
	if err := blocker.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&blockerPID); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	paid := capability.PaymentEvent{EventID: "paid", CheckoutID: "session", PaymentID: "payment", Mode: capability.PaymentTest, Kind: capability.PaymentCheckoutPaid, Currency: "usd", AmountMinor: 100, Created: now.Unix()}
	refund := capability.PaymentEvent{EventID: "refund-succeeded", ObjectID: "refund", PaymentID: "payment", Mode: capability.PaymentTest, Kind: capability.PaymentRefund, Currency: "usd", AmountMinor: 100, Status: "succeeded", Created: now.Unix()}
	pending := refund
	pending.EventID, pending.Status = "refund-pending", "pending"
	events := []capability.PaymentEvent{refund, paid, pending}
	done := make(chan error, len(events))
	for _, event := range events {
		go func() { done <- s.TestOnlySettleRefund(ctx, event) }()
	}
	for {
		select {
		case err := <-done:
			t.Fatalf("payment event finished before its state lock was released: %v", err)
		default:
		}
		var waiting int
		if err := s.DB().QueryRowContext(ctx, `WITH RECURSIVE blocked AS (
 SELECT pid FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid))
 UNION SELECT a.pid FROM pg_stat_activity a JOIN blocked b ON b.pid=ANY(pg_blocking_pids(a.pid))
) SELECT COUNT(*) FROM blocked`, blockerPID).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting == len(events) {
			break
		}
		runtime.Gosched()
	}
	if err := blocker.Rollback(); err != nil {
		t.Fatal(err)
	}
	for range events {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	for _, event := range append(events, pending) {
		if err := s.TestOnlySettleRefund(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	var balance int64
	if err := s.DB().QueryRowContext(ctx, `SELECT COALESCE(SUM(amount_micro),0) FROM credit_grants WHERE team='a'`).Scan(&balance); err != nil || balance != 0 {
		t.Fatalf("refunded payment retained spendable credit: %d: %v", balance, err)
	}
	var status string
	if err := s.DB().QueryRowContext(ctx, `SELECT status FROM payment_refunds WHERE payment_mode='test' AND kind='refund' AND refund_id='refund'`).Scan(&status); err != nil || status != "succeeded" {
		t.Fatalf("terminal outcome regressed: %q: %v", status, err)
	}
	var grants int
	if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM credit_grants WHERE team='a'`).Scan(&grants); err != nil || grants != 2 {
		t.Fatalf("payment did not settle grant and reversal exactly once: %d: %v", grants, err)
	}
}
