package store

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestLateBoundRefillOutcomesKeepTheirSpendingWindows(t *testing.T) {
	for _, window := range []struct {
		name    string
		delay   time.Duration
		trusted bool
		week    int64
		month   int64
		limit   string
	}{
		{"day", 25 * time.Hour, false, 0, 0, refillLimitStandingDaily},
		{"week", 8 * refillDay, true, 1_000, 0, refillLimitTeamWeekly},
		{"month", 31 * refillDay, true, 0, 1_000, refillLimitTeamMonthly},
	} {
		for _, outcome := range []string{"granted", "refused", "refunded", "declined"} {
			t.Run(window.name+"/"+outcome, func(t *testing.T) {
				s, tenant, now := refillFixture(t, window.trusted)
				settings := RefillSettings{
					Enabled: true, ThresholdCents: 1_000_000, AmountCents: 1_000,
					WeekLimitCents: window.week, MonthLimitCents: window.month,
				}
				if err := setRefill(t, s, "a", settings); err != nil {
					t.Fatal(err)
				}
				first := dueRefillsFor(t, s, "a")
				if len(first) != 1 {
					t.Fatalf("initial refill = %+v", first)
				}
				if err := s.AsOperator().RecordRefillIntent(t.Context(), "a", first[0].ID, "pi_late"); err != nil {
					t.Fatal(err)
				}
				now = now.Add(window.delay)
				s.clock = func() time.Time { return now }
				pending := dueRefillsFor(t, s, "a")
				if len(pending) != 1 || pending[0].ID != first[0].ID {
					t.Fatalf("bound refill lost its slot = %+v", pending)
				}
				paid := refillPaid(now, "pi_late", 1_000)
				switch outcome {
				case "refused":
					rewriteSchemaExec(t, s.db, `INSERT INTO payment_warnings(payment_mode,warning_id,payment_intent,actionable,created_at) VALUES ('test','issfr_late',?,1,1)`, paid.PaymentID)
				case "declined":
					paid.Kind, paid.DeclineCode = capability.PaymentCardFailed, "insufficient_funds"
				}
				settled, err := s.settlePaymentFacts(t.Context(), paid)
				if err != nil {
					t.Fatal(err)
				}
				if outcome == "refused" && settled.Refused != CardRefusedWarning {
					t.Fatalf("refill refusal = %+v", settled)
				}
				if outcome == "refunded" {
					refund := providerPaymentEvent(now, capability.PaymentRefund)
					refund.PaymentID, refund.ObjectID, refund.Status = "pi_late", "re_late", cardRefundSucceeded
					refund.AmountMinor = 1_000
					if _, err := s.settlePaymentFacts(t.Context(), refund); err != nil {
						t.Fatal(err)
					}
				}
				wantCount, wantCents := int64(1), int64(1_000)
				if window.name == "day" && outcome == "granted" {
					// The first paid grant earns Established's two refills per day.
					next := dueRefillsFor(t, s, "a")
					if len(next) != 1 {
						t.Fatalf("second Established refill = %+v", next)
					}
					payRefill(t, s, next[0], now)
					wantCount, wantCents = 2, 2_000
				}
				tenant.s.SetTestClock(t, func() time.Time { return now })
				st, err := tenant.SpendStanding(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				if st.Refill.DayCount != wantCount || st.Refill.WeekCents != wantCents || st.Refill.MonthCents != wantCents || st.Refill.Limit != window.limit {
					t.Fatalf("late %s escaped %s window: %+v", outcome, window.name, st.Refill)
				}
				if next := dueRefillsFor(t, s, "a"); len(next) != 0 {
					t.Fatalf("late %s allowed another refill: %+v", outcome, next)
				}
			})
		}
	}
}

func TestLatePaidRefillReplacesItsEarlierFailureWindow(t *testing.T) {
	for _, refused := range []bool{false, true} {
		t.Run(map[bool]string{false: "granted", true: "refused"}[refused], func(t *testing.T) {
			s, tenant, now := refillFixture(t, true)
			openRefill(t, s, "a", 1_000, "pi_recovered")
			if err := setRefill(t, s, "a", RefillSettings{Enabled: true, ThresholdCents: 1_000_000, AmountCents: 1_000, MonthLimitCents: 1_000}); err != nil {
				t.Fatal(err)
			}
			failed := refillPaid(now, "pi_recovered", 1_000)
			failed.EventID, failed.Kind = "failed-first", capability.PaymentCardFailed
			failed.Status, failed.DeclineCode = "requires_payment_method", "insufficient_funds"
			if _, err := s.settlePaymentFacts(t.Context(), failed); err != nil {
				t.Fatal(err)
			}
			now = now.Add(32 * refillDay)
			s.clock = func() time.Time { return now }
			if refused {
				rewriteSchemaExec(t, s.db, `INSERT INTO payment_warnings(payment_mode,warning_id,payment_intent,actionable,created_at) VALUES ('test','issfr_recovered','pi_recovered',1,1)`)
			}
			settled, err := s.settlePaymentFacts(t.Context(), refillPaid(now, "pi_recovered", 1_000))
			if err != nil || refused && settled.Refused != CardRefusedWarning || !refused && !settled.Granted {
				t.Fatalf("late paid outcome = %+v: %v", settled, err)
			}
			tenant.s.SetTestClock(t, func() time.Time { return now })
			st, err := tenant.SpendStanding(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if st.Refill.DayCount != 1 || st.Refill.WeekCents != 1_000 || st.Refill.MonthCents != 1_000 || st.Refill.Limit != refillLimitTeamMonthly {
				t.Fatalf("late paid outcome retained its earlier failure window: %+v", st.Refill)
			}
			if next := dueRefillsFor(t, s, "a"); len(next) != 0 {
				t.Fatalf("late paid outcome allowed another refill: %+v", next)
			}
			closedAt := now.UnixNano()
			now = now.Add(time.Hour)
			replayed := refillPaid(now, "pi_recovered", 1_000)
			replayed.EventID = "paid-again"
			if _, err := s.settlePaymentFacts(t.Context(), replayed); err != nil {
				t.Fatal(err)
			}
			var repeatedClosedAt int64
			if err := s.db.QueryRow(`SELECT closed_at FROM credit_checkouts WHERE team='a' AND payment_id='pi_recovered'`).Scan(&repeatedClosedAt); err != nil || repeatedClosedAt != closedAt {
				t.Fatalf("repeated paid outcome changed closure from %d to %d: %v", closedAt, repeatedClosedAt, err)
			}
		})
	}
}
