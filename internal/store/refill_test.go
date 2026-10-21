package store

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func dueRefillsFor(t *testing.T, s *Store, team Team) []Refill {
	t.Helper()
	due, err := s.AsOperator().DueRefills(t.Context())
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
	return s.inTx(t.Context(), func(tx *storeTx) error { return setRefillTx(t.Context(), tx, team, settings, "owner") })
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
	if r.PaymentIntent == "" {
		r.PaymentIntent = "pi_" + r.ID
		if err := s.AsOperator().RecordRefillIntent(t.Context(), r.Team, r.ID, r.PaymentIntent); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.settlePaymentFacts(t.Context(), refillPaid(now, r.PaymentIntent, r.AmountCents)); err != nil {
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
	if len(first) != 1 || first[0].AmountCents != 2_000 || first[0].Customer != "cus_1" || first[0].PaymentMethod != "pm_1" {
		t.Fatalf("refill = %+v", first)
	}
	if again := dueRefillsFor(t, s, "a"); len(again) != 1 || again[0].ID != first[0].ID {
		t.Fatalf("a second refill while one is open: %+v", again)
	}
	if err := s.AsOperator().RecordRefillIntent(t.Context(), "a", first[0].ID, "pi_first"); err != nil {
		t.Fatal(err)
	}
	if err := s.AsOperator().RecordRefillIntent(t.Context(), "a", first[0].ID, "pi_other"); err == nil {
		t.Fatal("a refill bound a second payment")
	}
	if balance, err := tenant.CreditBalanceMicro(t.Context()); err != nil || balance != 0 {
		t.Fatalf("balance before the webhook = %d, %v", balance, err)
	}
	for range 2 {
		if _, err := s.settlePaymentFacts(t.Context(), refillPaid(now, "pi_first", 2_000)); err != nil {
			t.Fatal(err)
		}
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
		{"standing size", false, RefillSettings{Enabled: true, ThresholdCents: 500, AmountCents: 6_000}, 0, RefillLimitStandingSize},
		{"standing daily", false, RefillSettings{Enabled: true, ThresholdCents: 1_000_000, AmountCents: 1_000}, 2, RefillLimitStandingDaily},
		{"team weekly", true, RefillSettings{Enabled: true, ThresholdCents: 1_000_000, AmountCents: 1_000, WeekLimitCents: 1_500}, 1, RefillLimitTeamWeekly},
		{"team monthly", true, RefillSettings{Enabled: true, ThresholdCents: 1_000_000, AmountCents: 1_000, MonthLimitCents: 2_500}, 2, RefillLimitTeamMonthly},
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
	if err := s.AsOperator().RecordRefillIntent(t.Context(), "a", due[0].ID, "pi_declined"); err != nil {
		t.Fatal(err)
	}
	failed := refillPaid(now, "pi_declined", 1_000)
	failed.Kind, failed.Status, failed.DeclineCode, failed.Fingerprint = capability.PaymentCardFailed, "requires_payment_method", "insufficient_funds", ""
	if got, err := s.settlePaymentFacts(t.Context(), failed); err != nil || got.RefillFailed != "insufficient_funds" {
		t.Fatalf("failed refill = %+v, %v", got, err)
	}
	if again := dueRefillsFor(t, s, "a"); len(again) != 0 {
		t.Fatalf("refill after a failure: %+v", again)
	}
	if err := tenant.TestOnlySaveCard(t.Context(), Card{Customer: "cus_1", PaymentMethod: "pm_2", Fingerprint: "fp_2", Brand: "visa", Last4: "1111"}, "billing", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if again := dueRefillsFor(t, s, "a"); len(again) != 1 || again[0].PaymentMethod != "pm_2" {
		t.Fatalf("refill after a new card: %+v", again)
	}
}

// A refill whose payment was never created within Stripe's idempotency window is closed and replaced, and an early
// fraud warning on the saved card turns refills off with the card.
func TestRefillExpiresAndAWarnedCardTurnsRefillOff(t *testing.T) {
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
	if _, err := s.AsOperator().TestOnlyRecordPaymentWarning(t.Context(), PaymentWarning{WarningID: "efw", PaymentIntent: "pi_elsewhere", Fingerprint: "fp_1", Actionable: true}, now); err != nil {
		t.Fatal(err)
	}
	tenant.s.SetTestClock(t, func() time.Time { return now })
	st, err := tenant.SpendStanding(t.Context())
	if err != nil || st.Refill.Enabled || st.Card.PaymentMethod != "" {
		t.Fatalf("after a warning on the saved card: refill %+v, card %+v, %v", st.Refill, st.Card, err)
	}
}

// Renewal pays the next slice ahead, so a running job's charges lead its usage by one slice, and settlement returns
// the unused part.
func TestRenewalPaysTheNextSliceAndSettlementReturnsTheRest(t *testing.T) {
	s, demand, now := meteredUnitFixture(t, true)
	claim, _ := meteredUnitClaim(t, s, demand)
	unitBillingTotal(t, s, 600, false)
	*now = now.Add(20 * time.Second)
	if _, err := s.AsOperator().RenewUnit(t.Context(), claim.Award.CapabilityPrefix, false); err != nil {
		t.Fatal(err)
	}
	unitBillingTotal(t, s, 1_200, false)
	*now = now.Add(90 * time.Second)
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

func TestARefusedRefillStopsRefillsAcrossWorkerPasses(t *testing.T) {
	s, tenant, now := refillFixture(t, true)
	if err := setRefill(t, s, "a", RefillSettings{Enabled: true, ThresholdCents: 1_000_000, AmountCents: 1_000}); err != nil {
		t.Fatal(err)
	}
	due := dueRefillsFor(t, s, "a")
	if len(due) != 1 {
		t.Fatalf("refill = %+v", due)
	}
	if err := s.AsOperator().RecordRefillIntent(t.Context(), "a", due[0].ID, "pi_pinned"); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `INSERT INTO payment_cards(fingerprint,first_team,payment_intent,pinned_at) VALUES ('fp_1','b','pi_b',1)`)
	got, err := s.settlePaymentFacts(t.Context(), refillPaid(now, "pi_pinned", 1_000))
	if err != nil || got.Refused != CardRefusedPinned || got.Granted {
		t.Fatalf("refill on a pinned card = %+v, %v", got, err)
	}
	for pass := range 3 {
		now = now.Add(30 * time.Second)
		s.clock = func() time.Time { return now }
		if again := dueRefillsFor(t, s, "a"); len(again) != 0 {
			t.Fatalf("pass %d charged the refused card again: %+v", pass, again)
		}
	}
	st, err := tenant.SpendStanding(t.Context())
	if err != nil || st.Card.PaymentMethod != "" || st.Refill.Enabled || st.Refill.Failure != CardRefusedPinned || st.Refill.DayCount != 1 || st.Refill.WeekCents != 1_000 {
		t.Fatalf("after a refused refill: card %+v refill %+v, %v", st.Card, st.Refill, err)
	}
	rewriteSchemaExec(t, s.db, `INSERT INTO teams(name,created_at,updated_at,card_customer,card_payment_method,card_fingerprint,refill_enabled,refill_threshold_cents,refill_amount_cents) VALUES ('c',1,1,'cus_c','pm_c','fp_1',1,1000000,1000)`)
	if pinned := dueRefillsFor(t, s, "c"); len(pinned) != 0 {
		t.Fatalf("a card pinned elsewhere was eligible for a refill: %+v", pinned)
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

func TestABoundRefillKeepsItsSlotUntilItsOutcome(t *testing.T) {
	s, tenant, now := refillFixture(t, true)
	if err := setRefill(t, s, "a", RefillSettings{Enabled: true, ThresholdCents: 1_000_000, AmountCents: 1_000}); err != nil {
		t.Fatal(err)
	}
	first := dueRefillsFor(t, s, "a")
	if len(first) != 1 {
		t.Fatalf("refill = %+v", first)
	}
	if err := s.AsOperator().RecordRefillIntent(t.Context(), "a", first[0].ID, "pi_late"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(refillKeyLife + 2*time.Hour)
	s.clock = func() time.Time { return now }
	again := dueRefillsFor(t, s, "a")
	if len(again) != 1 || again[0].ID != first[0].ID || again[0].PaymentIntent != "pi_late" {
		t.Fatalf("a bound refill gave up its slot: %+v", again)
	}
	tenant.s.SetTestClock(t, func() time.Time { return now })
	st, err := tenant.SpendStanding(t.Context())
	if err != nil || !st.Refill.Open || st.Refill.DayCount != 0 || st.Refill.WeekCents != 1_000 {
		t.Fatalf("bound refill state = %+v, %v", st.Refill, err)
	}
	now = now.Add(refillMonth)
	s.clock = func() time.Time { return now }
	if st, err = tenant.SpendStanding(t.Context()); err != nil || !st.Refill.Open || st.Refill.MonthCents != 0 {
		t.Fatalf("old bound refill state = %+v, %v", st.Refill, err)
	}
	if due := dueRefillsFor(t, s, "a"); len(due) != 1 || due[0].ID != first[0].ID {
		t.Fatalf("old bound refill gave up its slot: %+v", due)
	}
	payRefill(t, s, again[0], now)
	rewriteSchemaCount(t, s.db, "credit_checkouts WHERE kind='refill'", 1)
}

func TestAnUnboundRefillRechecksItsCardsPin(t *testing.T) {
	s, tenant, now := refillFixture(t, true)
	if err := setRefill(t, s, "a", RefillSettings{Enabled: true, ThresholdCents: 1_000_000, AmountCents: 1_000}); err != nil {
		t.Fatal(err)
	}
	if due := dueRefillsFor(t, s, "a"); len(due) != 1 {
		t.Fatalf("refill = %+v", due)
	}
	rewriteSchemaExec(t, s.db, `INSERT INTO payment_cards(fingerprint,first_team,payment_intent,pinned_at) VALUES ('fp_1','b','pi_b',1)`)
	if due := dueRefillsFor(t, s, "a"); len(due) != 0 {
		t.Fatalf("an unbound refill retried a newly pinned card: %+v", due)
	}
	tenant.s.SetTestClock(t, func() time.Time { return now })
	st, err := tenant.SpendStanding(t.Context())
	if err != nil || st.Card.PaymentMethod != "" || st.Refill.Enabled || st.Refill.Open || st.Refill.DayCount != 0 {
		t.Fatalf("after the pin refusal: card %+v refill %+v, %v", st.Card, st.Refill, err)
	}
}

func TestTurningRefillOffClosesOnlyAnUnboundReservation(t *testing.T) {
	for _, bound := range []bool{false, true} {
		t.Run(map[bool]string{false: "unbound", true: "bound"}[bound], func(t *testing.T) {
			s, tenant, now := refillFixture(t, true)
			settings := RefillSettings{Enabled: true, ThresholdCents: 1_000_000, AmountCents: 1_000}
			if err := setRefill(t, s, "a", settings); err != nil {
				t.Fatal(err)
			}
			due := dueRefillsFor(t, s, "a")
			if len(due) != 1 {
				t.Fatalf("refill = %+v", due)
			}
			if bound {
				if err := s.AsOperator().RecordRefillIntent(t.Context(), "a", due[0].ID, "payment"); err != nil {
					t.Fatal(err)
				}
			}
			settings.Enabled = false
			if err := setRefill(t, s, "a", settings); err != nil {
				t.Fatal(err)
			}
			again := dueRefillsFor(t, s, "a")
			if (len(again) == 1) != bound {
				t.Fatalf("refill after opt-out = %+v", again)
			}
			tenant.s.SetTestClock(t, func() time.Time { return now })
			st, err := tenant.SpendStanding(t.Context())
			if err != nil || st.Refill.Open != bound || !st.Refill.FailedAt.IsZero() {
				t.Fatalf("refill after opt-out = %+v, %v", st.Refill, err)
			}
		})
	}
}

func TestARefillReplaysItsOwnParametersUntilReplaced(t *testing.T) {
	s, tenant, now := refillFixture(t, true)
	rewriteSchemaExec(t, s.db, `INSERT INTO accounts(id,email,email_verified,created_at,updated_at) VALUES ('owner','owner@example.com',1,1,1)`)
	rewriteSchemaExec(t, s.db, `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('a','owner','owner',1)`)
	if err := setRefill(t, s, "a", RefillSettings{Enabled: true, ThresholdCents: 1_000_000, AmountCents: 1_000}); err != nil {
		t.Fatal(err)
	}
	first := dueRefillsFor(t, s, "a")
	if len(first) != 1 || first[0].PaymentMethod != "pm_1" || first[0].ReceiptEmail != "owner@example.com" {
		t.Fatalf("refill = %+v", first)
	}
	rewriteSchemaExec(t, s.db, `UPDATE accounts SET email='new-owner@example.com' WHERE id='owner'`)
	if err := setRefill(t, s, "a", RefillSettings{Enabled: true, ThresholdCents: 1_000_000, AmountCents: 2_000}); err != nil {
		t.Fatal(err)
	}
	if replay := dueRefillsFor(t, s, "a"); len(replay) != 1 || replay[0] != first[0] {
		t.Fatalf("an unbound refill changed its request: %+v", replay)
	}
	if err := tenant.TestOnlySaveCard(t.Context(), Card{Customer: "cus_1", PaymentMethod: "pm_2", Fingerprint: "fp_1", Brand: "visa", Last4: "4242"}, "billing", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	replaced := dueRefillsFor(t, s, "a")
	if len(replaced) != 1 || replaced[0].ID == first[0].ID || replaced[0].PaymentMethod != "pm_2" {
		t.Fatalf("an unbound refill was not replaced for the new card: %+v", replaced)
	}
	rewriteSchemaCount(t, s.db, "credit_checkouts WHERE kind='refill' AND failure='replaced' AND payment_id=''", 1)
	if err := s.AsOperator().RecordRefillIntent(t.Context(), "a", replaced[0].ID, "pi_bound"); err != nil {
		t.Fatal(err)
	}
	if err := tenant.TestOnlySaveCard(t.Context(), Card{Customer: "cus_1", PaymentMethod: "pm_3", Fingerprint: "fp_1", Brand: "visa", Last4: "4242"}, "billing", now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	bound := dueRefillsFor(t, s, "a")
	if len(bound) != 1 || bound[0] != (Refill{Team: "a", ID: replaced[0].ID, AmountCents: 2_000, Customer: "cus_1", PaymentMethod: "pm_2", ReceiptEmail: "new-owner@example.com", PaymentIntent: "pi_bound"}) {
		t.Fatalf("a bound refill changed its request: %+v", bound)
	}
}

func TestAFailedRefillWaitsForANewCardOrAddFunds(t *testing.T) {
	s, tenant, now := refillFixture(t, true)
	settings := RefillSettings{Enabled: true, ThresholdCents: 1_000_000, AmountCents: 1_000}
	if err := setRefill(t, s, "a", settings); err != nil {
		t.Fatal(err)
	}
	due := dueRefillsFor(t, s, "a")
	if err := s.AsOperator().RecordRefillIntent(t.Context(), "a", due[0].ID, "pi_declined"); err != nil {
		t.Fatal(err)
	}
	failed := refillPaid(now, "pi_declined", 1_000)
	failed.Kind, failed.Status, failed.DeclineCode, failed.Fingerprint = capability.PaymentCardFailed, "requires_payment_method", "insufficient_funds", ""
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
	rewriteSchemaCount(t, s.db, "business_events WHERE kind='"+BusinessEventRefillSettings+"'", 2)
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
	r := openRefill(t, s, "a", 1_000, "pi_delayed_failure")
	now = now.Add(time.Minute)
	s.clock = func() time.Time { return now }
	addFunds(t, s, 100, "pi_manual_recovery")
	failed := refillPaid(now.Add(-time.Minute), r.PaymentIntent, 1_000)
	failed.Kind, failed.Status, failed.DeclineCode, failed.Fingerprint = capability.PaymentCardFailed, "requires_payment_method", "insufficient_funds", ""
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
	r := openRefill(t, s, "a", 1_000, "pi_new_failure")
	failed := refillPaid(now, r.PaymentIntent, 1_000)
	failed.Kind, failed.Status, failed.DeclineCode, failed.Fingerprint = capability.PaymentCardFailed, "requires_payment_method", "insufficient_funds", ""
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
	r := openRefill(t, s, "a", 1_000, "payment")
	payRefill(t, s, r, now)
	refunded := providerPaymentEvent(now, capability.PaymentRefund)
	refunded.PaymentID, refunded.ObjectID, refunded.Status = "payment", "re_refill", CardRefundSucceeded
	refunded.AmountMinor = 1_000
	if _, err := s.settlePaymentFacts(t.Context(), refunded); err != nil {
		t.Fatal(err)
	}
	tenant.s.SetTestClock(t, func() time.Time { return now })
	st, err := tenant.SpendStanding(t.Context())
	if err != nil || st.Refill.DayCount != 1 || st.Refill.WeekCents != 1_000 || st.Refill.MonthCents != 1_000 || st.Refill.Limit != RefillLimitStandingDaily {
		t.Fatalf("a refunded refill escaped the windows: %+v, %v", st.Refill, err)
	}
	if due := dueRefillsFor(t, s, "a"); len(due) != 0 {
		t.Fatalf("refunding a refill freed its daily allowance: %+v", due)
	}
}

func TestASavedCardRecoversAFailedRefillWithoutClearingItsWindows(t *testing.T) {
	s, tenant, now := refillFixture(t, true)
	r := openRefill(t, s, "a", 1_000, "payment")
	setupCreated := now.Add(time.Minute)
	now = now.Add(2 * time.Minute)
	s.clock = func() time.Time { return now }
	failed := refillPaid(now, r.PaymentIntent, 1_000)
	failed.Kind, failed.Status, failed.DeclineCode, failed.Fingerprint = capability.PaymentCardFailed, "requires_payment_method", "insufficient_funds", ""
	if _, err := s.settlePaymentFacts(t.Context(), failed); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	s.clock = func() time.Time { return now }
	stale := Card{Customer: "cus_2", PaymentMethod: "pm_stale", Fingerprint: "fp_2", Brand: "visa", Last4: "4242", AddedAt: setupCreated.Add(-2 * time.Minute)}
	if err := tenant.TestOnlySaveCard(t.Context(), stale, "billing", now); !errors.Is(err, ErrStaleCardSetup) {
		t.Fatalf("stale card setup = %v", err)
	}
	tenant.s.SetTestClock(t, func() time.Time { return now })
	if st, err := tenant.SpendStanding(t.Context()); err != nil || st.Refill.Failure != "insufficient_funds" {
		t.Fatalf("a stale setup recovered the failure: %+v, %v", st.Refill, err)
	}
	if err := tenant.TestOnlySaveCard(t.Context(), Card{Customer: "cus_2", PaymentMethod: "pm_2", Fingerprint: "fp_2", Brand: "visa", Last4: "4242", AddedAt: setupCreated}, "billing", now); err != nil {
		t.Fatal(err)
	}
	tenant.s.SetTestClock(t, func() time.Time { return now })
	st, err := tenant.SpendStanding(t.Context())
	if err != nil || st.Refill.Failure != "" || st.Refill.DayCount != 1 || st.Refill.WeekCents != 1_000 {
		t.Fatalf("after saving a replacement card: %+v, %v", st.Refill, err)
	}
	if due := dueRefillsFor(t, s, "a"); len(due) != 1 || due[0].PaymentMethod != "pm_2" {
		t.Fatalf("refill did not resume on the replacement card: %+v", due)
	}
	rewriteSchemaCount(t, s.db, "credit_checkouts WHERE failure='insufficient_funds' AND refill_blocked=0", 1)
}

func TestALateRefillFailureDoesNotBlockTheReplacementCard(t *testing.T) {
	s, tenant, now := refillFixture(t, true)
	r := openRefill(t, s, "a", 1_000, "payment")
	if err := tenant.TestOnlySaveCard(t.Context(), Card{Customer: "cus_2", PaymentMethod: "pm_2", Fingerprint: "fp_2", Brand: "visa", Last4: "4242"}, "billing", now); err != nil {
		t.Fatal(err)
	}
	failed := refillPaid(now, r.PaymentIntent, 1_000)
	failed.Kind, failed.Status, failed.DeclineCode, failed.Fingerprint = capability.PaymentCardFailed, "requires_payment_method", "insufficient_funds", ""
	if _, err := s.settlePaymentFacts(t.Context(), failed); err != nil {
		t.Fatal(err)
	}
	tenant.s.SetTestClock(t, func() time.Time { return now })
	st, err := tenant.SpendStanding(t.Context())
	if err != nil || st.Card.PaymentMethod != "pm_2" || st.Refill.Failure != "" {
		t.Fatalf("late failure blocked the replacement: card %+v refill %+v, %v", st.Card, st.Refill, err)
	}
	if due := dueRefillsFor(t, s, "a"); len(due) != 1 || due[0].PaymentMethod != "pm_2" {
		t.Fatalf("refill did not use the replacement card: %+v", due)
	}
	rewriteSchemaCount(t, s.db, "credit_checkouts WHERE failure='insufficient_funds' AND refill_blocked=0", 1)
}

func TestSavingTheSameMethodDoesNotRecoverAFailedRefill(t *testing.T) {
	s, tenant, now := refillFixture(t, true)
	r := openRefill(t, s, "a", 1_000, "pi_failed_method")
	failed := refillPaid(now, r.PaymentIntent, 1_000)
	failed.Kind, failed.Status, failed.DeclineCode, failed.Fingerprint = capability.PaymentCardFailed, "requires_payment_method", "insufficient_funds", ""
	if _, err := s.settlePaymentFacts(t.Context(), failed); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	s.clock = func() time.Time { return now }
	if err := tenant.TestOnlySaveCard(t.Context(), Card{Customer: r.Customer, PaymentMethod: r.PaymentMethod, Fingerprint: "fp_1", Brand: "visa", Last4: "4242"}, "billing", now); err != nil {
		t.Fatal(err)
	}
	tenant.s.SetTestClock(t, func() time.Time { return now })
	st, err := tenant.SpendStanding(t.Context())
	if err != nil || st.Refill.Failure != "insufficient_funds" {
		t.Fatalf("unchanged method cleared refill failure: %+v, %v", st.Refill, err)
	}
	if due := dueRefillsFor(t, s, "a"); len(due) != 0 {
		t.Fatalf("unchanged method restarted refills: %+v", due)
	}
}

func TestARefusedAddFundsDisablesOnlyItsSavedCard(t *testing.T) {
	for _, replaced := range []bool{false, true} {
		t.Run(map[bool]string{false: "same card", true: "replacement card"}[replaced], func(t *testing.T) {
			s, tenant, now := refillFixture(t, true)
			if err := setRefill(t, s, "a", RefillSettings{Enabled: true, ThresholdCents: 1_000, AmountCents: 1_000}); err != nil {
				t.Fatal(err)
			}
			if replaced {
				if err := tenant.TestOnlySaveCard(t.Context(), Card{Customer: "cus_2", PaymentMethod: "pm_2", Fingerprint: "fp_2", Brand: "visa", Last4: "4242"}, "billing", now.Add(time.Minute)); err != nil {
					t.Fatal(err)
				}
			}
			rewriteSchemaExec(t, s.db, `INSERT INTO payment_cards(fingerprint,first_team,payment_intent,pinned_at) VALUES ('fp_1','b','pi_b',1)`)
			paid := capability.PaymentEvent{EventID: "manual-paid", CheckoutID: "provider-session", PaymentID: "manual-payment", Currency: "usd", AmountMinor: 100, Mode: capability.PaymentTest, Kind: capability.PaymentCheckoutPaid, Fingerprint: "fp_1"}
			if got, err := s.settlePaymentFacts(t.Context(), paid); err != nil || got.Refused != CardRefusedPinned {
				t.Fatalf("Add funds on a pinned card = %+v, %v", got, err)
			}
			tenant.s.SetTestClock(t, func() time.Time { return now })
			st, err := tenant.SpendStanding(t.Context())
			if err != nil || (st.Card.PaymentMethod != "") != replaced || st.Refill.Enabled != replaced {
				t.Fatalf("after the refused Add funds: card %+v refill %+v, %v", st.Card, st.Refill, err)
			}
		})
	}
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
			if err != nil || st.Refill.Limit != RefillLimitPaymentSize {
				t.Fatalf("legacy refill=%+v: %v", st.Refill, err)
			}
			if due := dueRefillsFor(t, s, "a"); len(due) != 0 {
				t.Fatalf("oversized refill charged: %+v", due)
			}
		})
	}
}

func TestRefillDoesNotConfirmOversizedReservations(t *testing.T) {
	for _, bound := range []bool{false, true} {
		t.Run(map[bool]string{false: "unbound", true: "bound"}[bound], func(t *testing.T) {
			s, _, _ := refillFixture(t, true)
			if err := setRefill(t, s, "a", RefillSettings{Enabled: true, ThresholdCents: 500, AmountCents: 1000}); err != nil {
				t.Fatal(err)
			}
			first := dueRefillsFor(t, s, "a")
			if len(first) != 1 {
				t.Fatalf("first refill=%+v", first)
			}
			if bound {
				if err := s.AsOperator().RecordRefillIntent(t.Context(), "a", first[0].ID, "pi_legacy"); err != nil {
					t.Fatal(err)
				}
			}
			rewriteSchemaExec(t, s.db, `UPDATE credit_checkouts SET amount_minor=?,amount_micro=? WHERE id=?`, MaxCardPaymentCents+1, (MaxCardPaymentCents+1)*MicroCreditsPerCent, first[0].ID)
			due := dueRefillsFor(t, s, "a")
			if bound {
				if len(due) != 1 || due[0].ID != first[0].ID {
					t.Fatalf("bound oversized refill lost its reconciliation slot: %+v", due)
				}
				if allowed, err := s.AsOperator().CanConfirmRefill(t.Context(), due[0]); err != nil || allowed {
					t.Fatalf("oversized refill confirmation = %v, %v", allowed, err)
				}
				return
			}
			for _, refill := range due {
				if refill.AmountCents > MaxCardPaymentCents || refill.ID == first[0].ID {
					t.Fatalf("oversized reservation retried: %+v", refill)
				}
			}
		})
	}
}

func TestBoundRefillConfirmationRechecksEligibility(t *testing.T) {
	for _, restriction := range []string{"none", "disabled", "hold", "blocked", "changed card", "weekly limit", "monthly limit", "standing limit"} {
		t.Run(restriction, func(t *testing.T) {
			s, tenant, now := refillFixture(t, true)
			settings := RefillSettings{Enabled: true, ThresholdCents: 1_000_000, AmountCents: 1_000}
			if err := setRefill(t, s, "a", settings); err != nil {
				t.Fatal(err)
			}
			r := dueRefillsFor(t, s, "a")[0]
			r.PaymentIntent = "pi_unconfirmed"
			if err := s.AsOperator().RecordRefillIntent(t.Context(), r.Team, r.ID, r.PaymentIntent); err != nil {
				t.Fatal(err)
			}
			switch restriction {
			case "disabled":
				settings.Enabled = false
			case "weekly limit":
				settings.WeekLimitCents = 999
			case "monthly limit":
				settings.MonthLimitCents = 999
			case "hold":
				rewriteSchemaExec(t, s.db, `INSERT INTO credit_freezes(dispute_id,team,reason,cause,placed_by,created_at) VALUES ('operator:test','a','review','operator','owner',1)`)
			case "blocked":
				rewriteSchemaExec(t, s.db, `INSERT INTO payment_warnings (payment_mode,warning_id,payment_intent,fingerprint,actionable,created_at) VALUES ('test','warning','earlier','fp_1',1,1)`)
			case "changed card":
				rewriteSchemaExec(t, s.db, `UPDATE teams SET card_payment_method='replacement' WHERE name='a'`)
			case "standing limit":
				rewriteSchemaExec(t, s.db, `UPDATE teams SET billing_trust='' WHERE name='a'`)
				rewriteSchemaExec(t, s.db, `UPDATE credit_checkouts SET amount_minor=6000,amount_micro=? WHERE id=?`, 6000*MicroCreditsPerCent, r.ID)
			}
			if err := setRefill(t, s, "a", settings); err != nil {
				t.Fatal(err)
			}
			if allowed, err := s.AsOperator().CanConfirmRefill(t.Context(), r); err != nil || allowed != (restriction == "none") {
				t.Fatalf("confirmation allowed=%v, %v", allowed, err)
			}
			if again := dueRefillsFor(t, s, "a"); len(again) != 1 || again[0].ID != r.ID {
				t.Fatalf("reservation lost before payment outcome: %+v", again)
			}
			if restriction != "none" {
				if err := s.AsOperator().RecordRefillCanceled(t.Context(), r); err != nil {
					t.Fatal(err)
				}
				var payment, failure string
				var closed int64
				if err := s.db.QueryRow(`SELECT payment_id,closed_at,failure FROM credit_checkouts WHERE id=?`, r.ID).Scan(&payment, &closed, &failure); err != nil || payment != r.PaymentIntent || closed == 0 || failure != "canceled" {
					t.Fatalf("canceled reservation = %q %d %q, %v", payment, closed, failure, err)
				}
				tenant.s.SetTestClock(t, func() time.Time { return now })
				state, err := tenant.SpendStanding(t.Context())
				if err != nil || state.Refill.DayCount != 0 || state.Refill.WeekCents != 0 || state.Refill.MonthCents != 0 {
					t.Fatalf("canceled payment consumed refill limits: %+v, %v", state.Refill, err)
				}
			}
		})
	}
}

func TestDueRefillsKeepsSuccessfulTeamsAroundFailure(t *testing.T) {
	for _, fault := range []string{"a", "b", "constraint", "database", "deletion"} {
		t.Run(fault, func(t *testing.T) {
			s, _, now := refillFixture(t, true)
			b := &Tenant{s: s, team: "b"}
			if err := b.TestOnlySaveCard(t.Context(), Card{Customer: "cus_b", PaymentMethod: "pm_b", Fingerprint: "fp_b", Brand: "visa", Last4: "4242"}, "billing", now); err != nil {
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
				rewriteSchemaExec(t, s.db, "UPDATE teams SET card_added_at='damaged' WHERE name=?", fault)
			}
			due, err := s.AsOperator().DueRefills(t.Context())
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
