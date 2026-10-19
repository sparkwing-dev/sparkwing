package store

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func providerPaymentFixture(t *testing.T) (*Store, *Tenant, time.Time) {
	t.Helper()
	s, tenant, now := cardCollectionFixture(t)
	rewriteSchemaExec(t, s.db, "UPDATE credit_checkouts SET amount_micro=?", 100*MicroCreditsPerCent)
	return s, tenant, now
}

func providerPaymentEvent(now time.Time, kind capability.PaymentEventKind) capability.PaymentEvent {
	return capability.PaymentEvent{EventID: "event-" + string(kind), ObjectID: "object-" + string(kind), PaymentID: "payment", Mode: capability.PaymentTest, Kind: kind, Currency: "usd", AmountMinor: 100, ChargeID: "provider-charge", Created: now.Unix()}
}

func providerCheckoutPaid(t *testing.T, s *Store) {
	t.Helper()
	event := capability.PaymentEvent{EventID: "checkout-paid", CheckoutID: "provider-session", PaymentID: "payment", Currency: "usd", AmountMinor: 100, Mode: capability.PaymentTest, Kind: capability.PaymentCheckoutPaid}
	if _, err := s.settlePaymentFacts(t.Context(), event); err != nil {
		t.Fatal(err)
	}
}

func providerCollectionEvent(t *testing.T, s *Store, now time.Time, kind capability.PaymentEventKind) capability.PaymentEvent {
	t.Helper()
	cardCollectionDebt(t, s, "debt", 15000)
	work := cardCollectionDue(t, s, now)
	if err := s.AsOperator().RecordAttemptIntent(t.Context(), work.AttemptID, "payment", work.Customer, now); err != nil {
		t.Fatal(err)
	}
	e := providerPaymentEvent(now, kind)
	e.ObjectID, e.AmountMinor, e.CustomerID, e.MethodID = e.PaymentID, work.AmountCents, "cus_1", "pm_1"
	e.Status, e.Fingerprint = "succeeded", "fp_1"
	if kind == capability.PaymentCardFailed {
		e.Status, e.Fingerprint, e.ChargeID = "requires_payment_method", "", ""
	}
	return e
}

func TestProviderPaymentsCommitFinancialStateWithReceipt(t *testing.T) {
	for _, kind := range []capability.PaymentEventKind{capability.PaymentCardSetup, capability.PaymentCardPaid, capability.PaymentCardFailed, capability.PaymentRefund, capability.PaymentWarningEvent, capability.PaymentDisputeOpened, capability.PaymentDisputeClosed} {
		t.Run(string(kind), func(t *testing.T) {
			s, _, now := providerPaymentFixture(t)
			e := providerPaymentEvent(now, kind)
			switch kind {
			case capability.PaymentCardSetup:
				rewriteSchemaExec(t, s.db, `INSERT INTO payment_correlations(payment_mode,provider_id,kind,team,customer_id) VALUES ('test',?,'setup','a','new-customer')`, e.ObjectID)
				e.PaymentID, e.Currency, e.AmountMinor, e.ChargeID = "", "", 0, ""
				e.CustomerID, e.MethodID, e.Fingerprint, e.Brand, e.Last4, e.Status = "new-customer", "new-method", "new-card", "visa", "4242", "succeeded"
				e.Created++
			case capability.PaymentCardPaid, capability.PaymentCardFailed:
				e = providerCollectionEvent(t, s, now, kind)
			default:
				providerCheckoutPaid(t, s)
				switch kind {
				case capability.PaymentRefund:
					e.AmountMinor, e.Status = 25, "succeeded"
				case capability.PaymentWarningEvent:
					e.AmountMinor, e.Currency, e.Fingerprint, e.Actionable = 0, "", "fp_1", true
				case capability.PaymentDisputeOpened:
					e.Reason, e.Status = "fraudulent", "needs_response"
				case capability.PaymentDisputeClosed:
					e.Reason, e.Status = "fraudulent", "lost"
				}
			}
			before := paymentMutationSnapshot(t, s)
			rewriteSchemaExec(t, s.db, `CREATE TRIGGER refuse_provider_receipt BEFORE INSERT ON payment_events BEGIN SELECT RAISE(ABORT,'receipt unavailable'); END`)
			if got, err := s.settlePaymentFacts(t.Context(), e); err == nil || got != (PaymentSettlement{}) {
				t.Fatalf("mutation survived missing receipt: %+v: %v", got, err)
			}
			if after := paymentMutationSnapshot(t, s); !reflect.DeepEqual(before, after) {
				t.Fatalf("receipt failure changed financial state: before=%v after=%v", before, after)
			}
			rewriteSchemaExec(t, s.db, "DROP TRIGGER refuse_provider_receipt")
			first, err := s.settlePaymentFacts(t.Context(), e)
			if err != nil || first.Team != "a" {
				t.Fatalf("settle: %+v: %v", first, err)
			}
			after := paymentMutationSnapshot(t, s)
			if reflect.DeepEqual(before, after) {
				t.Fatal("event did not change financial state")
			}
			for i := 0; i < 3; i++ {
				if i == 1 {
					e.EventID += "-second-provider-event"
				}
				got, err := s.settlePaymentFacts(t.Context(), e)
				if err != nil || got.Team != first.Team {
					t.Fatalf("replay: %+v: %v", got, err)
				}
				if !reflect.DeepEqual(after, paymentMutationSnapshot(t, s)) {
					t.Fatal("replayed object changed financial state")
				}
			}
			blobAccountingDiskStore(t, s)
			if !reflect.DeepEqual(after, paymentMutationSnapshot(t, s)) {
				t.Fatal("financial result did not survive reopening")
			}
		})
	}
}

func TestProviderPaymentAssociationCannotChangeProperty(t *testing.T) {
	s, _, now := providerPaymentFixture(t)
	e := providerCollectionEvent(t, s, now, capability.PaymentCardPaid)
	before := paymentMutationSnapshot(t, s)
	property := func(seed uint32, field uint8) bool {
		changed := e
		suffix := fmt.Sprintf("-other-%d", seed)
		switch field % 6 {
		case 0:
			changed.Mode = capability.PaymentLive
		case 1:
			changed.CustomerID += suffix
		case 2:
			changed.PaymentID += suffix
			changed.ObjectID = changed.PaymentID
		case 3:
			changed.Currency = "eur"
		case 4:
			changed.Kind = capability.PaymentEventKind(suffix)
		case 5:
			changed.AmountMinor = 0
		}
		got, err := s.settlePaymentFacts(t.Context(), changed)
		return err != nil && got == (PaymentSettlement{}) && reflect.DeepEqual(before, paymentMutationSnapshot(t, s))
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 64, Rand: rand.New(rand.NewSource(740))}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.settlePaymentFacts(t.Context(), e); err != nil {
		t.Fatal(err)
	}
	after := paymentMutationSnapshot(t, s)
	e.Kind, e.EventID, e.Status, e.DeclineCode = capability.PaymentCardFailed, "late-failure", "requires_payment_method", "declined"
	if _, err := s.settlePaymentFacts(t.Context(), e); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, paymentMutationSnapshot(t, s)) {
		t.Fatal("late failure changed paid attempt")
	}
}

func TestProviderPaymentRejectsConflictingPersistedPayerProperty(t *testing.T) {
	s, _, now := providerPaymentFixture(t)
	paid := providerCollectionEvent(t, s, now, capability.PaymentCardPaid)
	if _, err := s.settlePaymentFacts(t.Context(), paid); err != nil {
		t.Fatal(err)
	}
	before := paymentMutationSnapshot(t, s)
	property := func(seed uint32, foreignTeam bool) bool {
		team, mode := "a", "live"
		if foreignTeam {
			team, mode = "b", "test"
		}
		rewriteSchemaExec(t, s.db, "UPDATE payment_correlations SET team=?,payment_mode=? WHERE provider_id='payment'", team, mode)
		defer rewriteSchemaExec(t, s.db, "UPDATE payment_correlations SET team='a',payment_mode='test' WHERE provider_id='payment'")
		kinds := []capability.PaymentEventKind{capability.PaymentCardPaid, capability.PaymentCardFailed, capability.PaymentRefund, capability.PaymentWarningEvent, capability.PaymentDisputeOpened, capability.PaymentDisputeClosed}
		e := providerPaymentEvent(now, kinds[seed%uint32(len(kinds))])
		e.EventID, e.ObjectID, e.Status = fmt.Sprintf("conflicting-payer-%d", seed), fmt.Sprintf("conflicting-object-%d", seed), "succeeded"
		if e.Kind == capability.PaymentCardPaid || e.Kind == capability.PaymentCardFailed {
			e.ObjectID, e.AmountMinor = e.PaymentID, paid.AmountMinor
		}
		if e.Kind == capability.PaymentWarningEvent {
			e.AmountMinor, e.Currency, e.Actionable = 0, "", true
		}
		switch e.Kind {
		case capability.PaymentCardFailed:
			e.Status = "requires_payment_method"
		case capability.PaymentDisputeOpened:
			e.Status = "needs_response"
		case capability.PaymentDisputeClosed:
			e.Status = "won"
		}
		got, err := s.settlePaymentFacts(t.Context(), e)
		return err != nil && got == (PaymentSettlement{}) && reflect.DeepEqual(before, paymentMutationSnapshot(t, s))
	}
	for seed := range uint32(6) {
		for _, foreign := range []bool{false, true} {
			if !property(seed, foreign) {
				t.Fatalf("conflicting payer committed: kind=%d foreign_team=%t", seed, foreign)
			}
		}
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 32, Rand: rand.New(rand.NewSource(745))}); err != nil {
		t.Fatal(err)
	}
}

func TestProviderRefundIdentityAndConservationProperty(t *testing.T) {
	s, tenant, now := providerPaymentFixture(t)
	providerCheckoutPaid(t, s)
	remaining := int64(100)
	property := func(seed uint32) bool {
		e := providerPaymentEvent(now, capability.PaymentRefund)
		e.ObjectID, e.EventID, e.AmountMinor, e.Status = fmt.Sprintf("refund-%d", seed), fmt.Sprintf("refund-event-%d", seed), int64(seed%100+1), "succeeded"
		if _, err := s.settlePaymentFacts(t.Context(), e); err != nil {
			t.Log(err)
			return false
		}
		remaining -= min(remaining, e.AmountMinor)
		for _, id := range []string{e.EventID, e.EventID + "-updated"} {
			e.EventID = id
			if _, err := s.settlePaymentFacts(t.Context(), e); err != nil {
				t.Log(err)
				return false
			}
		}
		before := paymentMutationSnapshot(t, s)
		e.AmountMinor++
		e.EventID += "-conflict"
		if _, err := s.settlePaymentFacts(t.Context(), e); err == nil {
			return false
		}
		if !reflect.DeepEqual(before, paymentMutationSnapshot(t, s)) {
			return false
		}
		balance, err := tenant.CreditBalanceMicro(t.Context())
		return err == nil && balance == remaining*MicroCreditsPerCent
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 32, Rand: rand.New(rand.NewSource(741))}); err != nil {
		t.Fatal(err)
	}
}

func TestProviderWarningBeforeCollectionAndCloseBeforeOpen(t *testing.T) {
	t.Run("warning", func(t *testing.T) {
		s, tenant, now := providerPaymentFixture(t)
		payment := providerCollectionEvent(t, s, now, capability.PaymentCardPaid)
		warning := providerPaymentEvent(now, capability.PaymentWarningEvent)
		warning.AmountMinor, warning.Currency, warning.Fingerprint = 0, "", "fp_1"
		for _, actionable := range []bool{false, true, false} {
			warning.Actionable = actionable
			warning.EventID += fmt.Sprint(actionable)
			if _, err := s.settlePaymentFacts(t.Context(), warning); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.settlePaymentFacts(t.Context(), payment); err != nil {
			t.Fatal(err)
		}
		freeze, err := tenant.CreditFreeze(t.Context())
		if err != nil || !freeze.Frozen {
			t.Fatalf("warned payment did not hold team: %+v: %v", freeze, err)
		}
		var warned int
		if err := s.db.QueryRow("SELECT COUNT(*) FROM card_attempts WHERE status='warned'").Scan(&warned); err != nil || warned != 1 {
			t.Fatalf("warned attempts=%d: %v", warned, err)
		}
	})
	t.Run("dispute", func(t *testing.T) {
		s, tenant, now := providerPaymentFixture(t)
		providerCheckoutPaid(t, s)
		e := providerPaymentEvent(now, capability.PaymentDisputeClosed)
		e.Reason, e.Status = "fraudulent", "won"
		if _, err := s.settlePaymentFacts(t.Context(), e); err != nil {
			t.Fatal(err)
		}
		e.Kind, e.EventID, e.Status = capability.PaymentDisputeOpened, "late-open", "needs_response"
		if _, err := s.settlePaymentFacts(t.Context(), e); err != nil {
			t.Fatal(err)
		}
		freeze, err := tenant.CreditFreeze(t.Context())
		if err != nil || freeze.Frozen {
			t.Fatalf("late open revived closed dispute: %+v: %v", freeze, err)
		}
	})
}

func TestProviderSetupReplayCannotRestoreAnOlderCard(t *testing.T) {
	s, tenant, now := providerPaymentFixture(t)
	setup := func(id, method string, created int64) capability.PaymentEvent {
		rewriteSchemaExec(t, s.db, `INSERT INTO payment_correlations(payment_mode,provider_id,kind,team,customer_id) VALUES ('test',?,'setup','a','cus_1')`, id)
		return capability.PaymentEvent{EventID: id + "-event", ObjectID: id, Mode: capability.PaymentTest, Kind: capability.PaymentCardSetup, CustomerID: "cus_1", MethodID: method, Fingerprint: method, Brand: "visa", Last4: "4242", Created: created, Status: "succeeded"}
	}
	old := setup("old", "old-method", now.Unix()+1)
	newer := setup("new", "new-method", now.Unix()+2)
	for _, e := range []capability.PaymentEvent{old, newer} {
		if _, err := s.settlePaymentFacts(t.Context(), e); err != nil {
			t.Fatal(err)
		}
	}
	old.EventID += "-duplicate"
	old.Created = now.Unix() + 3
	if got, err := s.settlePaymentFacts(t.Context(), old); err != nil || !got.Replayed {
		t.Fatalf("old object replay: %+v: %v", got, err)
	}
	standing, err := tenant.SpendStanding(t.Context(), now)
	if err != nil || standing.Card.PaymentMethod != "new-method" {
		t.Fatalf("replay replaced current card: %+v: %v", standing.Card, err)
	}
	stale := setup("stale", "stale-method", now.Unix()+1)
	if _, err := s.settlePaymentFacts(t.Context(), stale); err != ErrStaleCardSetup {
		t.Fatalf("stale setup: %v", err)
	}
}

func TestProviderWarningProtectionIsMonotonicProperty(t *testing.T) {
	s, _, now := providerPaymentFixture(t)
	providerCheckoutPaid(t, s)
	index := 0
	property := func(seed uint32) bool {
		index++
		e := providerPaymentEvent(now, capability.PaymentWarningEvent)
		e.ObjectID, e.AmountMinor, e.Currency, e.Fingerprint = fmt.Sprintf("warning-%d", index), 0, "", "fp_1"
		seen := false
		for step := 0; step < 8; step++ {
			e.EventID = fmt.Sprintf("%s-event-%d", e.ObjectID, step)
			e.Actionable = seed&(1<<step) != 0
			seen = seen || e.Actionable
			if _, err := s.settlePaymentFacts(t.Context(), e); err != nil {
				t.Log(err)
				return false
			}
			var actionable int
			if err := s.db.QueryRow("SELECT actionable FROM payment_warnings WHERE warning_id=?", e.ObjectID).Scan(&actionable); err != nil {
				t.Log(err)
				return false
			}
			if (actionable != 0) != seen {
				return false
			}
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 32, Rand: rand.New(rand.NewSource(742))}); err != nil {
		t.Fatal(err)
	}
}

func TestProviderPaymentModeSwitchCannotReassignAnExistingPayment(t *testing.T) {
	s, _, now := providerPaymentFixture(t)
	payment := providerCollectionEvent(t, s, now, capability.PaymentCardPaid)
	if _, err := s.settlePaymentFacts(t.Context(), payment); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, "UPDATE settings SET value=? WHERE key='payment_mode'", []byte(`"live"`))
	before := paymentMutationSnapshot(t, s)
	for _, kind := range []capability.PaymentEventKind{capability.PaymentRefund, capability.PaymentDisputeOpened, capability.PaymentWarningEvent} {
		e := providerPaymentEvent(now, kind)
		e.Mode = capability.PaymentLive
		switch kind {
		case capability.PaymentRefund:
			e.Status = "succeeded"
		case capability.PaymentDisputeOpened:
			e.Status, e.Reason = "needs_response", "fraudulent"
		case capability.PaymentWarningEvent:
			e.AmountMinor, e.Currency, e.Fingerprint, e.Actionable = 0, "", "fp_1", true
		}
		if got, err := s.settlePaymentFacts(t.Context(), e); err == nil || got != (PaymentSettlement{}) {
			t.Fatalf("mode switch reassigned %s: %+v: %v", kind, got, err)
		}
	}
	if !reflect.DeepEqual(before, paymentMutationSnapshot(t, s)) {
		t.Fatal("mode switch changed financial state")
	}
}

func TestProviderRefundUsesOpaqueVerifiedEvent(t *testing.T) {
	s, tenant, _ := providerPaymentFixture(t)
	providerCheckoutPaid(t, s)
	dir := t.TempDir()
	key, secret := filepath.Join(dir, "key"), filepath.Join(dir, "secret")
	for path, value := range map[string]string{key: "sk_test_synthetic", secret: "whsec_synthetic"} {
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	verifier, err := capability.NewPaymentAuthenticator(capability.PaymentConfig{Mode: capability.PaymentTest, APIKeyFile: key, WebhookSecretFile: secret})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	for _, id := range []string{"refund-created", "refund-created", "refund-updated"} {
		body := fmt.Sprintf(`{"id":%q,"object":"event","type":"refund.updated","created":%d,"livemode":false,"data":{"object":{"id":"verified-refund","object":"refund","livemode":false,"status":"succeeded","currency":"usd","amount":25,"payment_intent":"payment","charge":"provider-charge","metadata":{"team":"b","amount_micro":"999999999999"}}}}`, id, now)
		mac := hmac.New(sha256.New, []byte("whsec_synthetic"))
		if _, err := fmt.Fprintf(mac, "%d.%s", now, body); err != nil {
			t.Fatal(err)
		}
		proof, err := verifier.VerifyPaymentEvent(t.Context(), capability.PaymentPresentation{Body: []byte(body), Signature: fmt.Sprintf("t=%d,v1=%s", now, hex.EncodeToString(mac.Sum(nil)))})
		if err != nil || proof == nil {
			t.Fatalf("verified refund: %+v: %v", proof, err)
		}
		got, err := s.SettlePayment(t.Context(), *proof)
		if err != nil || got.Team != "a" || got.AmountMicro != -25*MicroCreditsPerCent {
			t.Fatalf("settlement trusted metadata: %+v: %v", got, err)
		}
	}
	balance, err := tenant.CreditBalanceMicro(t.Context())
	if err != nil || balance != 75*MicroCreditsPerCent {
		t.Fatalf("verified refund balance=%d: %v", balance, err)
	}
}

func TestProviderPaymentSuccessAfterFailureUsesItsVerifiedAmount(t *testing.T) {
	s, _, now := providerPaymentFixture(t)
	e := providerCollectionEvent(t, s, now, capability.PaymentCardFailed)
	if _, err := s.settlePaymentFacts(t.Context(), e); err != nil {
		t.Fatal(err)
	}
	e.Kind, e.EventID, e.Status = capability.PaymentCardPaid, "paid-after-failure", "succeeded"
	e.AmountMinor++
	e.MethodID, e.Fingerprint, e.ChargeID = "replacement-method", "replacement-fingerprint", "replacement-charge"
	got, err := s.settlePaymentFacts(t.Context(), e)
	if err != nil || got.Team != "a" || got.AmountMicro != e.AmountMinor*MicroCreditsPerCent {
		t.Fatalf("success treated earlier failure as immutable payment facts: %+v: %v", got, err)
	}
	var queued int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM card_refunds WHERE payment_intent=?", e.PaymentID).Scan(&queued); err != nil || queued != 1 {
		t.Fatalf("late success lost its refund obligation: %d: %v", queued, err)
	}
}

func TestProviderSameAmountRetryAfterDeclineSettlesOnceAndClearsTheHold(t *testing.T) {
	s, tenant, now := providerPaymentFixture(t)
	e := providerCollectionEvent(t, s, now, capability.PaymentCardFailed)
	if _, err := s.settlePaymentFacts(t.Context(), e); err != nil {
		t.Fatal(err)
	}
	if freeze, err := tenant.CreditFreeze(t.Context()); err != nil || !freeze.Frozen {
		t.Fatalf("decline did not hold: %+v %v", freeze, err)
	}
	e.Kind, e.EventID, e.Status = capability.PaymentCardPaid, "paid-after-retry", "succeeded"
	e.MethodID, e.Fingerprint, e.ChargeID = "replacement-method", "replacement-fingerprint", "replacement-charge"
	for range 2 {
		if _, err := s.settlePaymentFacts(t.Context(), e); err != nil {
			t.Fatal(err)
		}
	}
	var refunds, paid, grants int
	if err := s.db.QueryRow(`SELECT (SELECT COUNT(*) FROM card_refunds),(SELECT COUNT(*) FROM card_charges WHERE state=?),(SELECT COUNT(*) FROM credit_grants WHERE reference LIKE '%payment%' OR kind='paid')`, CardChargePaid).Scan(&refunds, &paid, &grants); err != nil {
		t.Fatal(err)
	}
	if refunds != 0 || paid != 1 {
		t.Fatalf("retried success: refunds=%d paid charges=%d grants=%d", refunds, paid, grants)
	}
	if freeze, err := tenant.CreditFreeze(t.Context()); err != nil || freeze.Frozen {
		t.Fatalf("retried success left the hold: %+v %v", freeze, err)
	}
	if b, err := tenant.BillingStanding(t.Context(), now); err != nil || b.Tier == "Held" || !b.History.LastIncidentAt.IsZero() {
		t.Fatalf("standing after retry: %+v %v", b, err)
	}
}
