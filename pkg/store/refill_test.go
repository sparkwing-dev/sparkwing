package store

import (
	"errors"
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

// A refill is opened only for an opted-in team whose balance fell below its threshold, one at a time, and moves
// money only when its signed payment settles.
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
			st, err := tenant.SpendStanding(t.Context(), now)
			if err != nil || st.Refill.Limit != tc.want {
				t.Fatalf("limit = %q, %v; want %q (%+v)", st.Refill.Limit, err, tc.want, st.Refill)
			}
			if due := dueRefillsFor(t, s, "a"); (len(due) == 0) != (tc.want != "") {
				t.Fatalf("refills %+v with limit %q", due, tc.want)
			}
		})
	}
}

// Refill settings keep the $10 minimum, refuse turning refills on without a card, and a failed refill stops refills
// until the card or the settings change.
func TestRefillSettingsAndFailure(t *testing.T) {
	s, tenant, now := refillFixture(t, true)
	if err := setRefill(t, s, "a", RefillSettings{Enabled: true, ThresholdCents: 500, AmountCents: 999}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a $9.99 refill = %v", err)
	}
	if err := setRefill(t, s, "a", RefillSettings{Enabled: true, ThresholdCents: 500, AmountCents: MaxRefillCents + 1}); !errors.Is(err, ErrInvalidInput) {
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
	if expired := dueRefillsFor(t, s, "a"); len(expired) != 0 {
		t.Fatalf("an expired refill was retried: %+v", expired)
	}
	if replaced := dueRefillsFor(t, s, "a"); len(replaced) != 1 || replaced[0].ID == first[0].ID {
		t.Fatalf("expired refill not replaced: %+v", replaced)
	}
	if _, err := s.AsOperator().TestOnlyRecordPaymentWarning(t.Context(), PaymentWarning{WarningID: "efw", PaymentIntent: "pi_elsewhere", Fingerprint: "fp_1", Actionable: true}, now); err != nil {
		t.Fatal(err)
	}
	st, err := tenant.SpendStanding(t.Context(), now)
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
	if _, err := s.RenewUnit(t.Context(), claim.Award.CapabilityPrefix, false); err != nil {
		t.Fatal(err)
	}
	unitBillingTotal(t, s, 1_200, false)
	*now = now.Add(90 * time.Second)
	if _, err := s.RenewUnit(t.Context(), claim.Award.CapabilityPrefix, false); err != nil {
		t.Fatal(err)
	}
	unitBillingTotal(t, s, 1_500, false)
	if err := s.ReleaseUnit(t.Context(), claim, claim.Award.CapabilityPrefix); err != nil {
		t.Fatal(err)
	}
	unitBillingTotal(t, s, 900, true)
}
