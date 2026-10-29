package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// collectDueRefills runs a refill pass that charges nothing and returns the refills it authorized.
func collectDueRefills(ctx context.Context, op *Operator) ([]Refill, error) {
	var due []Refill
	err := op.EachDueRefill(ctx, func(r Refill) { due = append(due, r) })
	return due, err
}

func dueRefillsFor(t *testing.T, s *Store, team Team) []Refill {
	t.Helper()
	due, err := collectDueRefills(t.Context(), s.AsOperator())
	if err != nil {
		t.Fatal(err)
	}
	var out []Refill
	for _, r := range due {
		if r.Team == team {
			out = append(out, r)
		}
	}
	return out
}

func setRefill(t *testing.T, s *Store, team Team, settings RefillSettings) error {
	t.Helper()
	return s.inTx(t.Context(), func(tx *storeTx) error {
		return setRefillTx(t.Context(), tx, team, settings, auditAccountActor("owner"))
	})
}

func refillFixture(t *testing.T, trusted bool) (*Store, *Tenant, time.Time) {
	t.Helper()
	s, tenant, now := cardFixture(t)
	if !trusted {
		if _, _, err := tenant.TestOnlySetBillingTrust(t.Context(), BillingTrustChange{Trust: BillingTrustAutomatic, Actor: "console", Reason: "automatic"}, now); err != nil {
			t.Fatal(err)
		}
	}
	return s, tenant, now
}

func payRefill(t *testing.T, s *Store, r Refill, now time.Time) {
	t.Helper()
	if _, err := s.settlePaymentFacts(t.Context(), refillPaid(now, r, "pi_"+r.ID)); err != nil {
		t.Fatal(err)
	}
}

// A refill is opened only for an opted-in team below its threshold, one at a time; only its signed payment grants credits.
func TestRefillIsOptInBelowTheThresholdAndPaysOnlyFromTheWebhook(t *testing.T) {
	s, tenant, now := refillFixture(t, true)
	if due := dueRefillsFor(t, s, "a"); len(due) != 0 {
		t.Fatalf("refill without opting in: %+v", due)
	}
	if err := setRefill(t, s, "a", RefillSettings{Enabled: true, ThresholdCents: 0, AmountCents: 2_000}); err != nil {
		t.Fatal(err)
	}
	if due := dueRefillsFor(t, s, "a"); len(due) != 0 {
		t.Fatalf("refill at a balance not below the threshold: %+v", due)
	}
	if err := setRefill(t, s, "a", RefillSettings{Enabled: true, ThresholdCents: 500, AmountCents: 2_000}); err != nil {
		t.Fatal(err)
	}
	first := dueRefillsFor(t, s, "a")
	if len(first) != 1 || first[0].AmountCents != 2_000 || first[0].Customer != "cus_1" {
		t.Fatalf("refill = %+v", first)
	}
	if again := dueRefillsFor(t, s, "a"); len(again) != 1 || again[0].ID != first[0].ID {
		t.Fatalf("a second refill while one is open: %+v", again)
	}
	if balance, err := tenant.CreditBalanceMicro(t.Context()); err != nil || balance != 0 {
		t.Fatalf("balance before the webhook = %d, %v", balance, err)
	}
	for range 2 {
		if _, err := s.settlePaymentFacts(t.Context(), refillPaid(now, first[0], "pi_first")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.settlePaymentFacts(t.Context(), refillPaid(now, first[0], "pi_other")); err == nil {
		t.Fatal("a refill funded a second payment")
	}
	if balance, err := tenant.CreditBalanceMicro(t.Context()); err != nil || balance != 2_000*MicroCreditsPerCent {
		t.Fatalf("balance after the refill = %d, %v", balance, err)
	}
	if due := dueRefillsFor(t, s, "a"); len(due) != 0 {
		t.Fatalf("refill above the threshold: %+v", due)
	}
}

// A refill that would pass the team's own limits or its standing's caps is not made, and the state names the limit.
func TestRefillLimitsStopTheNextRefill(t *testing.T) {
	for _, tc := range []struct {
		name     string
		trusted  bool
		settings RefillSettings
		paid     int
		want     string
	}{
		{"standing size", false, RefillSettings{Enabled: true, ThresholdCents: 500, AmountCents: 6_000}, 0, refillLimitStandingSize},
		{"standing daily", false, RefillSettings{Enabled: true, ThresholdCents: 1_000_000, AmountCents: 1_000}, 2, refillLimitStandingDaily},
		{"team weekly", true, RefillSettings{Enabled: true, ThresholdCents: 1_000_000, AmountCents: 1_000, WeekLimitCents: 1_500}, 1, refillLimitTeamWeekly},
		{"team monthly", true, RefillSettings{Enabled: true, ThresholdCents: 1_000_000, AmountCents: 1_000, MonthLimitCents: 2_500}, 2, refillLimitTeamMonthly},
		{"within limits", true, RefillSettings{Enabled: true, ThresholdCents: 1_000_000, AmountCents: 1_000, WeekLimitCents: 5_000}, 2, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, tenant, now := refillFixture(t, tc.trusted)
			if err := setRefill(t, s, "a", tc.settings); err != nil {
				t.Fatal(err)
			}
			for i := range tc.paid {
				due := dueRefillsFor(t, s, "a")
				if len(due) != 1 {
					t.Fatalf("refill %d not opened: %+v", i, due)
				}
				payRefill(t, s, due[0], now)
				if tc.name == "team monthly" {
					now = now.Add(8 * 24 * time.Hour)
					s.clock = func() time.Time { return now }
				}
			}
			st, err := tenant.SpendStanding(t.Context())
			if err != nil || st.Refill.Limit != tc.want {
				t.Fatalf("limit = %q, %v; want %q (%+v)", st.Refill.Limit, err, tc.want, st.Refill)
			}
			if due := dueRefillsFor(t, s, "a"); (len(due) == 0) != (tc.want != "") {
				t.Fatalf("refills %+v with limit %q", due, tc.want)
			}
		})
	}
}

// A failed refill waits for a saved card or successful Add funds; settings cannot recover it.
func TestRefillSettingsAndFailure(t *testing.T) {
	s, tenant, now := refillFixture(t, true)
	if err := setRefill(t, s, "a", RefillSettings{Enabled: true, ThresholdCents: 500, AmountCents: 999}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a $9.99 refill = %v", err)
	}
	if err := setRefill(t, s, "a", RefillSettings{Enabled: true, ThresholdCents: 500, AmountCents: MaxCardPaymentCents + 1}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("an oversized refill = %v", err)
	}
	rewriteSchemaExec(t, s.db, `INSERT INTO teams(name,created_at,updated_at) VALUES ('cardless',1,1)`)
	if err := setRefill(t, s, "cardless", RefillSettings{Enabled: true, ThresholdCents: 500, AmountCents: 1_000}); !errors.Is(err, ErrNoCard) {
		t.Fatalf("refill without a card = %v", err)
	}
	if err := setRefill(t, s, "a", RefillSettings{Enabled: true, ThresholdCents: 500, AmountCents: 1_000}); err != nil {
		t.Fatal(err)
	}
	due := dueRefillsFor(t, s, "a")
	if len(due) != 1 {
		t.Fatalf("refill = %+v", due)
	}
	failed := refillPaid(now, due[0], "pi_declined")
	failed.Kind, failed.Status, failed.DeclineCode = capability.PaymentCardFailed, "requires_payment_method", "insufficient_funds"
	if got, err := s.settlePaymentFacts(t.Context(), failed); err != nil || got.RefillFailed != "insufficient_funds" {
		t.Fatalf("failed refill = %+v, %v", got, err)
	}
	if again := dueRefillsFor(t, s, "a"); len(again) != 0 {
		t.Fatalf("refill after a failure: %+v", again)
	}
	if err := tenant.TestOnlySaveCard(t.Context(), Card{Customer: "cus_1"}, "billing", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if again := dueRefillsFor(t, s, "a"); len(again) != 1 {
		t.Fatalf("refill after a new card: %+v", again)
	}
}

// A refill no outcome settled within Stripe's idempotency window is closed and replaced; a late payment still credits it.
func TestAnUnsettledRefillExpiresAndALatePaymentStillCredits(t *testing.T) {
	s, tenant, now := refillFixture(t, true)
	if err := setRefill(t, s, "a", RefillSettings{Enabled: true, ThresholdCents: 500, AmountCents: 1_000}); err != nil {
		t.Fatal(err)
	}
	first := dueRefillsFor(t, s, "a")
	now = now.Add(refillKeyLife + time.Minute)
	s.clock = func() time.Time { return now }
	if replaced := dueRefillsFor(t, s, "a"); len(replaced) != 1 || replaced[0].ID == first[0].ID {
		t.Fatalf("expired refill not replaced: %+v", replaced)
	}
	rewriteSchemaCount(t, s.db, "credit_checkouts WHERE kind='refill' AND failure='expired' AND payment_id=''", 1)
	if got, err := s.settlePaymentFacts(t.Context(), refillPaid(now, first[0], "pi_late")); err != nil || !got.Granted {
		t.Fatalf("late payment of an expired refill = %+v, %v", got, err)
	}
	if balance, err := tenant.CreditBalanceMicro(t.Context()); err != nil || balance != 1_000*MicroCreditsPerCent {
		t.Fatalf("balance after the late payment = %d, %v", balance, err)
	}
}

// Renewal pays the next slice ahead, so a running job's charges lead its usage by one slice, and settlement returns
// the unused part. The first renewal starts compute, and the admitted slice already covers its minimum.
func TestRenewalPaysTheNextSliceAndSettlementReturnsTheRest(t *testing.T) {
	s, demand, now := meteredUnitFixture(t, true)
	claim, _ := meteredUnitClaim(t, s, demand)
	unitBillingTotal(t, s, 600, false)
	*now = now.Add(20 * time.Second)
	if _, err := s.AsOperator().RenewUnit(t.Context(), claim.Award.CapabilityPrefix, false); err != nil {
		t.Fatal(err)
	}
	unitBillingTotal(t, s, 600, false)
	*now = now.Add(40 * time.Second)
	if _, err := s.AsOperator().RenewUnit(t.Context(), claim.Award.CapabilityPrefix, false); err != nil {
		t.Fatal(err)
	}
	unitBillingTotal(t, s, 1_000, false)
	*now = now.Add(50 * time.Second)
	if _, err := s.AsOperator().RenewUnit(t.Context(), claim.Award.CapabilityPrefix, false); err != nil {
		t.Fatal(err)
	}
	unitBillingTotal(t, s, 1_500, false)
	if err := s.ReleaseUnit(t.Context(), claim, claim.Award.CapabilityPrefix); err != nil {
		t.Fatal(err)
	}
	unitBillingTotal(t, s, 900, true)
}

func addFunds(t *testing.T, s *Store, cents int64, payment string) {
	t.Helper()
	rewriteSchemaExec(t, s.db, `UPDATE credit_checkouts SET amount_minor=?, amount_micro=? WHERE id='checkout'`, cents, cents*MicroCreditsPerCent)
	paid := capability.PaymentEvent{EventID: "add-" + payment, CheckoutID: "provider-session", PaymentID: payment, Currency: "usd", AmountMinor: cents, Mode: capability.PaymentTest, Kind: capability.PaymentCheckoutPaid}
	if got, err := s.settlePaymentFacts(t.Context(), paid); err != nil || !got.Granted {
		t.Fatalf("add funds = %+v, %v", got, err)
	}
}

// An early fraud warning that names a refill before its payment settles refuses that payment and holds the team, so no
// later pass charges again.
func TestARefusedRefillStopsRefillsAcrossWorkerPasses(t *testing.T) {
	s, tenant, now := refillFixture(t, true)
	if err := setRefill(t, s, "a", RefillSettings{Enabled: true, ThresholdCents: 1_000_000, AmountCents: 1_000}); err != nil {
		t.Fatal(err)
	}
	due := dueRefillsFor(t, s, "a")
	if len(due) != 1 {
		t.Fatalf("refill = %+v", due)
	}
	if _, err := s.AsOperator().TestOnlyRecordPaymentWarning(t.Context(), paymentWarning{WarningID: "issfr_pinned", PaymentIntent: "pi_pinned", Actionable: true}, now); err != nil {
		t.Fatal(err)
	}
	got, err := s.settlePaymentFacts(t.Context(), refillPaid(now, due[0], "pi_pinned"))
	if err != nil || got.Refused != CardRefusedWarning || got.Granted {
		t.Fatalf("refill named by a warning = %+v, %v", got, err)
	}
	for pass := range 3 {
		now = now.Add(30 * time.Second)
		s.clock = func() time.Time { return now }
		if again := dueRefillsFor(t, s, "a"); len(again) != 0 {
			t.Fatalf("pass %d charged the refused card again: %+v", pass, again)
		}
	}
	st, err := tenant.SpendStanding(t.Context())
	if err != nil || st.Billing.HoldCause != HoldCauseWarning || st.Refill.Failure != CardRefusedWarning || st.Refill.DayCount != 1 || st.Refill.WeekCents != 1_000 {
		t.Fatalf("after a refused refill: standing %+v refill %+v, %v", st.Billing, st.Refill, err)
	}
}

func TestRefillCapsFollowHistoryNotTheBalance(t *testing.T) {
	s, tenant, now := refillFixture(t, false)
	tenant.s.SetTestClock(t, func() time.Time { return now })
	st, err := tenant.SpendStanding(t.Context())
	if err != nil || st.Refill.Caps != (RefillCaps{AmountCents: 5_000, PerDay: 1, WeekCents: 10_000}) {
		t.Fatalf("without paid history: caps %+v, %v", st.Refill.Caps, err)
	}
	addFunds(t, s, 10_000, "pi_add")
	if err := setRefill(t, s, "a", RefillSettings{Enabled: true, ThresholdCents: 1_000_000, AmountCents: 6_000}); err != nil {
		t.Fatal(err)
	}
	tenant.s.SetTestClock(t, func() time.Time { return now })
	st, err = tenant.SpendStanding(t.Context())
	if err != nil || st.Billing.Tier != "Trusted" || st.Billing.History.Tier != "Established" || st.Refill.Caps != (RefillCaps{AmountCents: 20_000, PerDay: 2, WeekCents: 50_000}) || st.Refill.Limit != "" {
		t.Fatalf("after $100 Add funds: tier %s history %s caps %+v limit %q, %v", st.Billing.Tier, st.Billing.History.Tier, st.Refill.Caps, st.Refill.Limit, err)
	}
	for range 2 {
		due := dueRefillsFor(t, s, "a")
		if len(due) != 1 {
			t.Fatalf("refill within Established caps = %+v", due)
		}
		payRefill(t, s, due[0], now)
	}
	if again := dueRefillsFor(t, s, "a"); len(again) != 0 {
		t.Fatalf("a third refill in one day at Established caps: %+v", again)
	}
}

// An open refill is retried with its own request after the team changes its refill amount, until its key life.
func TestAnOpenRefillIsRetriedAsOpenedUntilItsKeyLife(t *testing.T) {
	s, tenant, now := refillFixture(t, true)
	settings := RefillSettings{Enabled: true, ThresholdCents: 1_000_000, AmountCents: 1_000}
	if err := setRefill(t, s, "a", settings); err != nil {
		t.Fatal(err)
	}
	first := dueRefillsFor(t, s, "a")
	if len(first) != 1 || first[0].Customer != "cus_1" {
		t.Fatalf("refill = %+v", first)
	}
	settings.AmountCents = 2_000
	if err := setRefill(t, s, "a", settings); err != nil {
		t.Fatal(err)
	}
	now = now.Add(refillKeyLife - time.Minute)
	s.clock = func() time.Time { return now }
	again := dueRefillsFor(t, s, "a")
	if len(again) != 1 || again[0] != first[0] {
		t.Fatalf("an open refill changed its request: %+v, want %+v", again, first)
	}
	tenant.s.SetTestClock(t, func() time.Time { return now })
	if st, err := tenant.SpendStanding(t.Context()); err != nil || !st.Refill.Open || st.Refill.WeekCents != 1_000 {
		t.Fatalf("open refill state = %+v, %v", st.Refill, err)
	}
	payRefill(t, s, again[0], now)
	rewriteSchemaCount(t, s.db, "credit_checkouts WHERE kind='refill'", 1)
}

// Every retry rechecks eligibility, setting the refill's own reservation aside; a refill the team may no longer be
// charged for is withdrawn without another call, and a payment an earlier call made still credits it.
func TestAnOpenRefillIsWithdrawnWhenNoLongerEligible(t *testing.T) {
	for _, restriction := range []string{"none", "disabled", "hold", "changed customer", "weekly limit", "monthly limit", "funded", "threshold lowered"} {
		t.Run(restriction, func(t *testing.T) {
			s, tenant, now := refillFixture(t, true)
			settings := RefillSettings{Enabled: true, ThresholdCents: 1_000_000, AmountCents: 1_000, WeekLimitCents: 1_000}
			if err := setRefill(t, s, "a", settings); err != nil {
				t.Fatal(err)
			}
			first := dueRefillsFor(t, s, "a")
			if len(first) != 1 {
				t.Fatalf("refill = %+v", first)
			}
			switch restriction {
			case "disabled":
				settings.Enabled = false
			case "hold":
				rewriteSchemaExec(t, s.db, `INSERT INTO credit_freezes(dispute_id,team,reason,cause,placed_by,created_at) VALUES ('operator:test','a','review','operator','owner',1)`)
			case "changed customer":
				if err := tenant.TestOnlySaveCard(t.Context(), Card{Customer: "cus_2"}, "billing", now.Add(time.Minute)); err != nil {
					t.Fatal(err)
				}
			case "weekly limit":
				settings.WeekLimitCents = 999
			case "monthly limit":
				settings.MonthLimitCents = 999
			case "funded":
				settings.ThresholdCents = 1_500
				addFunds(t, s, 2_000, "pi_topup")
			case "threshold lowered":
				settings.ThresholdCents = 0
			}
			if err := setRefill(t, s, "a", settings); err != nil {
				t.Fatal(err)
			}
			s.SetTestClock(t, func() time.Time { return now })
			again := dueRefillsFor(t, s, "a")
			if restriction == "none" {
				if len(again) != 1 || again[0] != first[0] {
					t.Fatalf("an eligible open refill was not retried: %+v", again)
				}
				return
			}
			if len(again) != 0 {
				t.Fatalf("an ineligible open refill was retried: %+v", again)
			}
			rewriteSchemaCount(t, s.db, "credit_checkouts WHERE kind='refill' AND failure='withdrawn' AND payment_id=''", 1)
			if restriction == "hold" {
				return
			}
			before, err := tenant.CreditBalanceMicro(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			payRefill(t, s, first[0], now)
			if balance, err := tenant.CreditBalanceMicro(t.Context()); err != nil || balance != before+1_000*MicroCreditsPerCent {
				t.Fatalf("a payment an earlier call made was not credited: %d, %v", balance, err)
			}
		})
	}
}

func TestAFailedRefillWaitsForANewCardOrAddFunds(t *testing.T) {
	s, tenant, now := refillFixture(t, true)
	settings := RefillSettings{Enabled: true, ThresholdCents: 1_000_000, AmountCents: 1_000}
	if err := setRefill(t, s, "a", settings); err != nil {
		t.Fatal(err)
	}
	due := dueRefillsFor(t, s, "a")
	failed := refillPaid(now, due[0], "pi_declined")
	failed.Kind, failed.Status, failed.DeclineCode = capability.PaymentCardFailed, "requires_payment_method", "insufficient_funds"
	if _, err := s.settlePaymentFacts(t.Context(), failed); err != nil {
		t.Fatal(err)
	}
	settings.WeekLimitCents = 50_000
	if err := setRefill(t, s, "a", settings); err != nil {
		t.Fatal(err)
	}
	if again := dueRefillsFor(t, s, "a"); len(again) != 0 {
		t.Fatalf("a settings change lifted a failed refill: %+v", again)
	}
	tenant.s.SetTestClock(t, func() time.Time { return now })
	if st, err := tenant.SpendStanding(t.Context()); err != nil || st.Refill.DayCount != 1 || st.Refill.WeekCents != 1_000 || st.Refill.MonthCents != 1_000 {
		t.Fatalf("a failed refill escaped the windows: %+v, %v", st.Refill, err)
	}
	rewriteSchemaCount(t, s.db, "audit_events WHERE kind='"+businessEventRefillSettings+"'", 2)
	addFunds(t, s, 100, "pi_manual")
	tenant.s.SetTestClock(t, func() time.Time { return now })
	if st, err := tenant.SpendStanding(t.Context()); err != nil || st.Refill.Failure != "" {
		t.Fatalf("a paid Add funds kept the refill failure: %+v, %v", st.Refill, err)
	}
	if again := dueRefillsFor(t, s, "a"); len(again) != 1 {
		t.Fatalf("refills did not resume after Add funds: %+v", again)
	}
	rewriteSchemaCount(t, s.db, "credit_checkouts WHERE failure='insufficient_funds' AND refill_blocked=0", 1)
}

func TestALateRefillFailureDoesNotUndoNewerAddFunds(t *testing.T) {
	s, tenant, now := refillFixture(t, true)
	r := openRefill(t, s, "a", 1_000)
	now = now.Add(time.Minute)
	s.clock = func() time.Time { return now }
	addFunds(t, s, 100, "pi_manual_recovery")
	failed := refillPaid(now.Add(-time.Minute), r, "pi_delayed_failure")
	failed.Kind, failed.Status, failed.DeclineCode = capability.PaymentCardFailed, "requires_payment_method", "insufficient_funds"
	if _, err := s.settlePaymentFacts(t.Context(), failed); err != nil {
		t.Fatal(err)
	}
	tenant.s.SetTestClock(t, func() time.Time { return now })
	st, err := tenant.SpendStanding(t.Context())
	if err != nil || st.Refill.Failure != "" {
		t.Fatalf("delayed failure undid Add funds recovery: %+v, %v", st.Refill, err)
	}
	if due := dueRefillsFor(t, s, "a"); len(due) != 1 {
		t.Fatalf("refills did not resume after Add funds: %+v", due)
	}
}

func TestAddFundsDoesNotRecoverANewerRefillFailure(t *testing.T) {
	s, tenant, now := refillFixture(t, true)
	addFunds(t, s, 100, "pi_manual_before_refill")
	now = now.Add(time.Minute)
	s.clock = func() time.Time { return now }
	r := openRefill(t, s, "a", 1_000)
	failed := refillPaid(now, r, "pi_new_failure")
	failed.Kind, failed.Status, failed.DeclineCode = capability.PaymentCardFailed, "requires_payment_method", "insufficient_funds"
	if _, err := s.settlePaymentFacts(t.Context(), failed); err != nil {
		t.Fatal(err)
	}
	tenant.s.SetTestClock(t, func() time.Time { return now })
	st, err := tenant.SpendStanding(t.Context())
	if err != nil || st.Refill.Failure != "insufficient_funds" {
		t.Fatalf("earlier Add funds hid a newer failure: %+v, %v", st.Refill, err)
	}
}

func TestARefundedRefillStillCountsAgainstEveryWindow(t *testing.T) {
	s, tenant, now := refillFixture(t, false)
	r := openRefill(t, s, "a", 1_000)
	payRefill(t, s, r, now)
	refunded := providerPaymentEvent(now, capability.PaymentRefund)
	refunded.PaymentID, refunded.ObjectID, refunded.Status = "pi_"+r.ID, "re_refill", cardRefundSucceeded
	refunded.AmountMinor = 1_000
	if _, err := s.settlePaymentFacts(t.Context(), refunded); err != nil {
		t.Fatal(err)
	}
	tenant.s.SetTestClock(t, func() time.Time { return now })
	st, err := tenant.SpendStanding(t.Context())
	if err != nil || st.Refill.DayCount != 1 || st.Refill.WeekCents != 1_000 || st.Refill.MonthCents != 1_000 || st.Refill.Limit != refillLimitStandingDaily {
		t.Fatalf("a refunded refill escaped the windows: %+v, %v", st.Refill, err)
	}
	if due := dueRefillsFor(t, s, "a"); len(due) != 0 {
		t.Fatalf("refunding a refill freed its daily allowance: %+v", due)
	}
}

func TestASavedCardRecoversAFailedRefillWithoutClearingItsWindows(t *testing.T) {
	s, tenant, now := refillFixture(t, true)
	r := openRefill(t, s, "a", 1_000)
	setupCreated := now.Add(time.Minute)
	now = now.Add(2 * time.Minute)
	s.clock = func() time.Time { return now }
	failed := refillPaid(now, r, "payment")
	failed.Kind, failed.Status, failed.DeclineCode = capability.PaymentCardFailed, "requires_payment_method", "insufficient_funds"
	if _, err := s.settlePaymentFacts(t.Context(), failed); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	s.clock = func() time.Time { return now }
	stale := Card{Customer: "cus_2", AddedAt: setupCreated.Add(-2 * time.Minute)}
	if err := tenant.TestOnlySaveCard(t.Context(), stale, "billing", now); !errors.Is(err, errStaleCardSetup) {
		t.Fatalf("stale card setup = %v", err)
	}
	tenant.s.SetTestClock(t, func() time.Time { return now })
	if st, err := tenant.SpendStanding(t.Context()); err != nil || st.Refill.Failure != "insufficient_funds" {
		t.Fatalf("a stale setup recovered the failure: %+v, %v", st.Refill, err)
	}
	if err := tenant.TestOnlySaveCard(t.Context(), Card{Customer: "cus_2", AddedAt: setupCreated}, "billing", now); err != nil {
		t.Fatal(err)
	}
	tenant.s.SetTestClock(t, func() time.Time { return now })
	st, err := tenant.SpendStanding(t.Context())
	if err != nil || st.Refill.Failure != "" || st.Refill.DayCount != 1 || st.Refill.WeekCents != 1_000 {
		t.Fatalf("after saving a replacement card: %+v, %v", st.Refill, err)
	}
	if due := dueRefillsFor(t, s, "a"); len(due) != 1 || due[0].Customer != "cus_2" {
		t.Fatalf("refill did not resume on the replacement card: %+v", due)
	}
	rewriteSchemaCount(t, s.db, "credit_checkouts WHERE failure='insufficient_funds' AND refill_blocked=0", 1)
}

func TestALateRefillFailureDoesNotBlockTheReplacementCard(t *testing.T) {
	s, tenant, now := refillFixture(t, true)
	r := openRefill(t, s, "a", 1_000)
	if err := tenant.TestOnlySaveCard(t.Context(), Card{Customer: "cus_2"}, "billing", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	failed := refillPaid(now, r, "payment")
	failed.Kind, failed.Status, failed.DeclineCode = capability.PaymentCardFailed, "requires_payment_method", "insufficient_funds"
	if _, err := s.settlePaymentFacts(t.Context(), failed); err != nil {
		t.Fatal(err)
	}
	tenant.s.SetTestClock(t, func() time.Time { return now })
	st, err := tenant.SpendStanding(t.Context())
	if err != nil || st.Card.Customer != "cus_2" || st.Refill.Failure != "" {
		t.Fatalf("late failure blocked the replacement: card %+v refill %+v, %v", st.Card, st.Refill, err)
	}
	if due := dueRefillsFor(t, s, "a"); len(due) != 1 || due[0].Customer != "cus_2" {
		t.Fatalf("refill did not use the replacement card: %+v", due)
	}
	rewriteSchemaCount(t, s.db, "credit_checkouts WHERE failure='insufficient_funds' AND refill_blocked=0", 1)
}

func TestRefillGlobalMaximumAppliesWithAndWithoutStandingCaps(t *testing.T) {
	for _, high := range []bool{false, true} {
		t.Run(map[bool]string{false: "New", true: "High"}[high], func(t *testing.T) {
			s, tenant, now := refillFixture(t, high)
			settings := RefillSettings{Enabled: true, ThresholdCents: 500, AmountCents: MaxCardPaymentCents}
			if err := setRefill(t, s, "a", settings); err != nil {
				t.Fatal(err)
			}
			settings.AmountCents++
			if err := setRefill(t, s, "a", settings); !errors.Is(err, ErrInvalidInput) || !strings.Contains(err.Error(), "contact us") {
				t.Fatalf("oversized refill: %v", err)
			}
			rewriteSchemaExec(t, s.db, `UPDATE teams SET refill_amount_cents=? WHERE name='a'`, MaxCardPaymentCents+1)
			tenant.s.SetTestClock(t, func() time.Time { return now })
			st, err := tenant.SpendStanding(t.Context())
			if err != nil || st.Refill.Limit != refillLimitPaymentSize {
				t.Fatalf("legacy refill=%+v: %v", st.Refill, err)
			}
			if due := dueRefillsFor(t, s, "a"); len(due) != 0 {
				t.Fatalf("oversized refill charged: %+v", due)
			}
		})
	}
}

// DueRefills opens a refill only for a team that is opted in, not blocked by a failed refill, not held, within every
// limit and below its threshold, all checked in the transaction that opens it.
func TestARefillOpensOnlyForAnEligibleTeam(t *testing.T) {
	for _, restriction := range []string{"none", "disabled", "failed", "hold", "weekly limit", "monthly limit", "standing limit", "above threshold"} {
		t.Run(restriction, func(t *testing.T) {
			s, _, now := refillFixture(t, restriction == "standing limit")
			settings := RefillSettings{Enabled: true, ThresholdCents: 1_000_000, AmountCents: 1_000}
			switch restriction {
			case "disabled":
				settings.Enabled = false
			case "failed":
				r := openRefill(t, s, "a", 1_000)
				if _, err := s.AsOperator().RecordRefillFailed(t.Context(), r, "card_declined"); err != nil {
					t.Fatal(err)
				}
			case "hold":
				rewriteSchemaExec(t, s.db, `INSERT INTO credit_freezes(dispute_id,team,reason,cause,placed_by,created_at) VALUES ('operator:test','a','review','operator','owner',1)`)
			case "weekly limit":
				settings.WeekLimitCents = 999
			case "monthly limit":
				settings.MonthLimitCents = 999
			case "standing limit":
				rewriteSchemaExec(t, s.db, `UPDATE teams SET billing_trust='' WHERE name='a'`)
				settings.AmountCents = 6_000
			case "above threshold":
				settings.ThresholdCents = 0
			}
			if err := setRefill(t, s, "a", settings); err != nil {
				t.Fatal(err)
			}
			s.SetTestClock(t, func() time.Time { return now })
			if due := dueRefillsFor(t, s, "a"); (len(due) == 1) != (restriction == "none") {
				t.Fatalf("refills under %s: %+v", restriction, due)
			}
		})
	}
}

func TestDueRefillsKeepsSuccessfulTeamsAroundFailure(t *testing.T) {
	for _, fault := range []string{"a", "b", "constraint", "database", "deletion"} {
		t.Run(fault, func(t *testing.T) {
			s, _, now := refillFixture(t, true)
			b := &Tenant{s: s, team: "b"}
			if err := b.TestOnlySaveCard(t.Context(), Card{Customer: "cus_b"}, "billing", now); err != nil {
				t.Fatal(err)
			}
			for _, team := range []Team{"a", "b"} {
				if err := setRefill(t, s, team, RefillSettings{Enabled: true, ThresholdCents: 500, AmountCents: 1000}); err != nil {
					t.Fatal(err)
				}
			}
			switch fault {
			case "database":
				rewriteSchemaExec(t, s.db, "DROP TABLE credit_checkouts")
			case "constraint":
				rewriteSchemaExec(t, s.db, "CREATE TRIGGER reject_refill BEFORE INSERT ON credit_checkouts WHEN NEW.team='b' BEGIN SELECT RAISE(ABORT,'unavailable'); END")
				s.SetTestClock(t, func() time.Time { return now })
			case "deletion":
				if _, _, err := s.AsOperator().RequestTeamDeletion(t.Context(), "a"); err != nil {
					t.Fatal(err)
				}
			default:
				rewriteSchemaExec(t, s.db, "CREATE TRIGGER reject_refill BEFORE INSERT ON credit_checkouts WHEN NEW.team='"+fault+"' BEGIN SELECT RAISE(ABORT,'unavailable'); END")
			}
			due, err := collectDueRefills(t.Context(), s.AsOperator())
			if fault == "database" {
				if err == nil || len(due) != 0 {
					t.Fatalf("database failure: due=%+v, %v", due, err)
				}
				return
			}
			if err != nil || len(due) != 1 {
				t.Fatalf("due=%+v, %v", due, err)
			}
			want := Team("a")
			if fault == "a" || fault == "deletion" {
				want = "b"
			}
			if due[0].Team != want {
				t.Fatalf("due=%+v", due)
			}
			var count int
			if err := s.db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM credit_checkouts WHERE kind='refill'").Scan(&count); err != nil || count != 1 {
				t.Fatalf("refills=%d, %v", count, err)
			}
		})
	}
}

// A refill in flight holds the team's single refill slot until the transaction that grants it closes it, so no pass
// within its 23-hour key life opens a second one before its webhook; a webhook later than that is the runbook case. After the grant, another refill opens only while the balance is still below
// the threshold: $10 of Add funds and a $10 refill reach $20, which a $25 threshold refills again to $30.
func TestARefillInFlightBlocksASecondUntilItsGrantLands(t *testing.T) {
	for _, tc := range []struct {
		thresholdCents int64
		second         bool
	}{{1_500, false}, {2_500, true}} {
		s, tenant, now := refillFixture(t, true)
		addFunds(t, s, 1_000, "pi_add")
		if err := setRefill(t, s, "a", RefillSettings{Enabled: true, ThresholdCents: tc.thresholdCents, AmountCents: 1_000}); err != nil {
			t.Fatal(err)
		}
		first := dueRefillsFor(t, s, "a")
		if len(first) != 1 {
			t.Fatalf("threshold %d: refill = %+v", tc.thresholdCents, first)
		}
		for pass := range 5 {
			if again := dueRefillsFor(t, s, "a"); len(again) != 1 || again[0] != first[0] {
				t.Fatalf("threshold %d: pass %d before the grant opened another refill: %+v", tc.thresholdCents, pass, again)
			}
		}
		rewriteSchemaCount(t, s.db, "credit_checkouts WHERE kind='refill'", 1)
		payRefill(t, s, first[0], now)
		if balance, err := tenant.CreditBalanceMicro(t.Context()); err != nil || balance != 2_000*MicroCreditsPerCent {
			t.Fatalf("threshold %d: balance after the refill = %d, %v", tc.thresholdCents, balance, err)
		}
		if next := dueRefillsFor(t, s, "a"); (len(next) == 1) != tc.second || len(next) == 1 && next[0].ID == first[0].ID {
			t.Fatalf("threshold %d: refill after the grant = %+v, want a new one %t", tc.thresholdCents, next, tc.second)
		}
	}
}

// A refill the checkout service answered as charged keeps counting against the limits after a recheck withdraws it,
// so turning refills off and on before its webhook cannot charge past a weekly limit.
func TestAWithdrawnChargedRefillStillCountsAgainstTheLimits(t *testing.T) {
	s, tenant, now := refillFixture(t, true)
	settings := RefillSettings{Enabled: true, ThresholdCents: 1_000_000, AmountCents: 1_000, WeekLimitCents: 1_500}
	if err := setRefill(t, s, "a", settings); err != nil {
		t.Fatal(err)
	}
	first := dueRefillsFor(t, s, "a")
	if len(first) != 1 {
		t.Fatalf("refill = %+v", first)
	}
	if err := s.AsOperator().RecordRefillCharged(t.Context(), first[0], "pi_charged"); err != nil {
		t.Fatal(err)
	}
	settings.Enabled = false
	if err := setRefill(t, s, "a", settings); err != nil {
		t.Fatal(err)
	}
	if due := dueRefillsFor(t, s, "a"); len(due) != 0 {
		t.Fatalf("a disabled team was charged: %+v", due)
	}
	rewriteSchemaCount(t, s.db, "credit_checkouts WHERE kind='refill' AND failure='withdrawn' AND payment_id='pi_charged'", 1)
	settings.Enabled = true
	if err := setRefill(t, s, "a", settings); err != nil {
		t.Fatal(err)
	}
	tenant.s.SetTestClock(t, func() time.Time { return now })
	if st, err := tenant.SpendStanding(t.Context()); err != nil || st.Refill.WeekCents != 1_000 || st.Refill.Limit != refillLimitTeamWeekly {
		t.Fatalf("a withdrawn charged refill left the windows: %+v, %v", st.Refill, err)
	}
	if due := dueRefillsFor(t, s, "a"); len(due) != 0 {
		t.Fatalf("a second refill passed the weekly limit before the first webhook: %+v", due)
	}
	if got, err := s.settlePaymentFacts(t.Context(), refillPaid(now, first[0], "pi_charged")); err != nil || !got.Granted {
		t.Fatalf("the charged refill's webhook = %+v, %v", got, err)
	}
	if balance, err := tenant.CreditBalanceMicro(t.Context()); err != nil || balance != 1_000*MicroCreditsPerCent {
		t.Fatalf("the charged refill's webhook did not credit it: %d, %v", balance, err)
	}
}
