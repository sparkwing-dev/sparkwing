package store_test

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestPostgresRefundCallbackWaitsForMidflightBinding(t *testing.T) {
	for _, outcome := range []string{"failed", "canceled"} {
		t.Run(outcome, func(t *testing.T) {
			s := storetest.NewPostgres(t).Open(t)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			now := time.Now().UTC()
			for _, query := range []string{
				`INSERT INTO teams(name,created_at,updated_at) VALUES ('a',1,1)`,
				`INSERT INTO settings(key,value) VALUES ('payment_mode','"test"')`,
				`INSERT INTO credit_checkouts(id,team,session_id,payment_mode,payment_id,currency,amount_minor,amount_micro,opened_at,expires_at) VALUES ('checkout','a','session','test','payment','usd',100,100000000,1,2)`,
				`INSERT INTO card_refunds(payment_mode,team,payment_intent,reason,amount_micro,created_at) VALUES ('test','a','payment','warning',100000000,1)`,
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
			if _, err := blocker.ExecContext(ctx, `SELECT team FROM card_refunds WHERE team='a' FOR UPDATE`); err != nil {
				t.Fatal(err)
			}
			var pid int
			if err := blocker.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
				t.Fatal(err)
			}
			binder := make(chan error, 1)
			callback := make(chan error, 1)
			go func() { binder <- s.AsOperator().MarkCardRefundMade(ctx, "a", "payment-0", "refund", "pending", now) }()
			waitBlocked := func(want int) {
				t.Helper()
				for {
					select {
					case err := <-binder:
						t.Fatalf("binding finished before release: %v", err)
					default:
					}
					select {
					case err := <-callback:
						t.Fatalf("callback finished before binding: %v", err)
					default:
					}
					var count int
					if err := s.DB().QueryRowContext(ctx, `WITH RECURSIVE blocked AS (
      SELECT pid FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid))
      UNION SELECT a.pid FROM pg_stat_activity a JOIN blocked b ON b.pid=ANY(pg_blocking_pids(a.pid))
     ) SELECT COUNT(*) FROM blocked`, pid).Scan(&count); err != nil {
						t.Fatal(err)
					}
					if count >= want {
						return
					}
					runtime.Gosched()
				}
			}
			waitBlocked(1)
			event := capability.PaymentEvent{EventID: "callback", ObjectID: "refund", PaymentID: "payment", Mode: capability.PaymentTest, Kind: capability.PaymentRefund, Currency: "usd", AmountMinor: 100, Status: outcome, Created: now.Unix()}
			go func() { callback <- s.TestOnlySettleRefund(ctx, event) }()
			waitBlocked(2)
			if err := blocker.Rollback(); err != nil {
				t.Fatal(err)
			}
			for _, done := range []<-chan error{binder, callback} {
				if err := <-done; err != nil {
					t.Fatal(err)
				}
			}
			// Distinct provider deliveries can describe the same terminal outcome.
			event.EventID = "callback-again"
			if err := s.TestOnlySettleRefund(ctx, event); err != nil {
				t.Fatal(err)
			}
			if err := s.AsOperator().MarkCardRefundMade(ctx, "a", "payment-0", "refund", "pending", now); err != nil {
				t.Fatal(err)
			}
			due, err := s.AsOperator().DueCardRefunds(ctx, now.Add(2*time.Hour))
			if err != nil || len(due) != 1 || due[0].Key != "payment-1" {
				t.Fatalf("terminal callback lost or applied twice: %+v, %v", due, err)
			}
		})
	}
}
