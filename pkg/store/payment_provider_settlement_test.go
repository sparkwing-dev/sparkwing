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
	s, tenant, now := cardFixture(t)
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

func providerRefillEvent(t *testing.T, s *Store, now time.Time, kind capability.PaymentEventKind) capability.PaymentEvent {
	t.Helper()
	openRefill(t, s, "a", 15000, "payment")
	e := refillPaid(now, "payment", 15000)
	e.EventID, e.Kind = "event-"+string(kind), kind
	if kind == capability.PaymentCardFailed {
		e.Status, e.Fingerprint, e.ChargeID, e.DeclineCode = "requires_payment_method", "", "", "card_declined"
	}
	return e
}

func TestLostDisputeBeforeDelayedPaymentReversesGrant(t *testing.T) {
	for _, paidFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("paid_first=%t", paidFirst), func(t *testing.T) {
			s, tenant, now := providerPaymentFixture(t)
			payment := providerRefillEvent(t, s, now, capability.PaymentCardPaid)
			dispute := providerPaymentEvent(now, capability.PaymentDisputeClosed)
			dispute.AmountMinor, dispute.Status = payment.AmountMinor, "lost"
			events := []capability.PaymentEvent{dispute, payment}
			if paidFirst {
				events[0], events[1] = events[1], events[0]
			}
			for _, event := range events {
				if _, err := s.settlePaymentFacts(t.Context(), event); err != nil {
					t.Fatal(err)
				}
				blobAccountingDiskStore(t, s)
			}
			if _, err := tenant.ReleaseCreditFreezes(t.Context(), dispute.ObjectID, "operator", "reviewed", now); err != nil {
				t.Fatal(err)
			}
			for _, event := range events {
				if _, err := s.settlePaymentFacts(t.Context(), event); err != nil {
					t.Fatal(err)
				}
			}
			if balance, err := tenant.CreditBalanceMicro(t.Context()); err != nil || balance != 0 {
				t.Fatalf("lost dispute left spendable credits after hold release: %d: %v", balance, err)
			}
			rewriteSchemaCount(t, s.db, "credit_grants", 2)
		})
	}
}

func TestPendingDisputeReversalAndDelayedGrantCommitTogether(t *testing.T) {
	s, tenant, now := providerPaymentFixture(t)
	payment := providerRefillEvent(t, s, now, capability.PaymentCardPaid)
	dispute := providerPaymentEvent(now, capability.PaymentDisputeClosed)
	dispute.AmountMinor, dispute.Status = payment.AmountMinor, "lost"
	if _, err := s.settlePaymentFacts(t.Context(), dispute); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `CREATE TRIGGER refuse_reversal BEFORE INSERT ON credit_grants
	    WHEN NEW.kind = 'reversal' BEGIN SELECT RAISE(ABORT,'reversal unavailable'); END`)
	if _, err := s.settlePaymentFacts(t.Context(), payment); err == nil {
		t.Fatal("payment granted without consuming its pending reversal")
	}
	rewriteSchemaCount(t, s.db, "credit_grants", 0)
	rewriteSchemaCount(t, s.db, "payment_events", 1)
	rewriteSchemaExec(t, s.db, "DROP TRIGGER refuse_reversal")
	if _, err := s.settlePaymentFacts(t.Context(), payment); err != nil {
		t.Fatal(err)
	}
	if balance, err := tenant.CreditBalanceMicro(t.Context()); err != nil || balance != 0 {
		t.Fatalf("balance after retry = %d: %v", balance, err)
	}
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
				e = providerRefillEvent(t, s, now, kind)
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
	e := providerRefillEvent(t, s, now, capability.PaymentCardPaid)
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
		payment := providerRefillEvent(t, s, now, capability.PaymentCardPaid)
		warning := providerPaymentEvent(now, capability.PaymentWarningEvent)
		warning.AmountMinor, warning.Currency, warning.Fingerprint = 0, "", "fp_1"
		for _, actionable := range []bool{false, true, false} {
			warning.Actionable = actionable
			warning.EventID += fmt.Sprint(actionable)
			if _, err := s.settlePaymentFacts(t.Context(), warning); err != nil {
				t.Fatal(err)
			}
		}
		settled, err := s.settlePaymentFacts(t.Context(), payment)
		if err != nil || settled.Refused != CardRefusedWarning || settled.Granted {
			t.Fatalf("webhook settlement = %+v, %v; want the warned payment's consequences returned", settled, err)
		}
		freeze, err := tenant.CreditFreeze(t.Context())
		if err != nil || !freeze.Frozen {
			t.Fatalf("warned payment did not hold team: %+v: %v", freeze, err)
		}
		var refused int
		if err := s.db.QueryRow("SELECT COUNT(*) FROM credit_checkouts WHERE kind='refill' AND paid_at IS NULL AND closed_at IS NOT NULL").Scan(&refused); err != nil || refused != 1 {
			t.Fatalf("refused refills=%d: %v", refused, err)
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
	payment := providerRefillEvent(t, s, now, capability.PaymentCardPaid)
	if _, err := s.settlePaymentFacts(t.Context(), payment); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, "UPDATE settings SET value=? WHERE key='payment_mode'", []byte(`"live"`))
	before := paymentMutationSnapshot(t, s)
	for _, kind := range []capability.PaymentEventKind{capability.PaymentRefund, capability.PaymentDisputeOpened} {
		e := providerPaymentEvent(now, kind)
		e.Mode = capability.PaymentLive
		switch kind {
		case capability.PaymentRefund:
			e.Status = "succeeded"
		case capability.PaymentDisputeOpened:
			e.Status, e.Reason = "needs_response", "fraudulent"
		case capability.PaymentWarningEvent:
			e.AmountMinor, e.Currency, e.Actionable = 0, "", true
		}
		got, err := s.settlePaymentFacts(t.Context(), e)
		if kind == capability.PaymentRefund || kind == capability.PaymentWarningEvent {
			if err != nil || got.Team != "" || got.GrantID != "" {
				t.Fatalf("a live %s of a test payment = %+v, %v; want it kept against no live payment", kind, got, err)
			}
			continue
		}
		if err == nil || got != (PaymentSettlement{}) {
			t.Fatalf("mode switch reassigned %s: %+v: %v", kind, got, err)
		}
	}
	after := paymentMutationSnapshot(t, s)
	// The live refund remains an obligation for a future live payment, never the test-mode grant.
	delete(before, "payment_refunds")
	delete(after, "payment_refunds")
	rewriteSchemaCount(t, s.db, "payment_refunds WHERE payment_mode='live' AND kind='refund'", 1)
	if !reflect.DeepEqual(before, after) {
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
		proof, err := verifier.VerifyPaymentEvent(t.Context(), capability.PaymentPresentation{Body: []byte(body), Signature: fmt.Sprintf("t=%d,v1=%s", now, hex.EncodeToString(mac.Sum(nil)))}, capability.PaymentScope{Records: s})
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

// A refill whose payment failed records the failure without holding the team; a later success of the same payment
// grants once and clears the failure, and a success naming another amount is refused.
func TestProviderRefillSuccessAfterFailureGrantsOnce(t *testing.T) {
	s, tenant, now := providerPaymentFixture(t)
	e := providerRefillEvent(t, s, now, capability.PaymentCardFailed)
	got, err := s.settlePaymentFacts(t.Context(), e)
	if err != nil || got.RefillFailed != "card_declined" {
		t.Fatalf("failed refill = %+v, %v", got, err)
	}
	st, err := tenant.SpendStanding(t.Context(), now)
	if err != nil || st.Refill.Failure != "card_declined" || st.Billing.Tier == "Held" {
		t.Fatalf("after a failed refill: %+v, %v; want the failure recorded and no hold", st.Refill, err)
	}
	wrong := e
	wrong.Kind, wrong.EventID, wrong.Status, wrong.AmountMinor = capability.PaymentCardPaid, "paid-wrong", "succeeded", e.AmountMinor+1
	if _, err := s.settlePaymentFacts(t.Context(), wrong); err == nil {
		t.Fatal("a refill paid with another amount was settled")
	}
	e.Kind, e.EventID, e.Status, e.Fingerprint, e.ChargeID = capability.PaymentCardPaid, "paid-after-retry", "succeeded", "fp_1", "provider-charge"
	for range 2 {
		if _, err := s.settlePaymentFacts(t.Context(), e); err != nil {
			t.Fatal(err)
		}
	}
	rewriteSchemaCount(t, s.db, "credit_grants", 1)
	if st, err := tenant.SpendStanding(t.Context(), now); err != nil || st.Refill.Failure != "" || st.BalanceMicro != e.AmountMinor*MicroCreditsPerCent {
		t.Fatalf("after the retried success: %+v, %v", st, err)
	}
}

func TestRecordsPaymentParentResolvesOnlyCommittedObjects(t *testing.T) {
	s, _, _ := providerPaymentFixture(t)
	providerCheckoutPaid(t, s)
	rewriteSchemaExec(t, s.db, `INSERT INTO payment_correlations(payment_mode,provider_id,kind,team,customer_id) VALUES ('test','cs_setup_page','setup_session','a','cus_1')`)
	var session string
	if err := s.db.QueryRow(`SELECT session_id FROM credit_checkouts WHERE team='a' AND session_id<>id LIMIT 1`).Scan(&session); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		route capability.PaymentRoute
		want  bool
	}{
		{capability.PaymentRoute{Mode: capability.PaymentTest, CheckoutID: session}, true},
		{capability.PaymentRoute{Mode: capability.PaymentTest, PaymentID: "payment"}, true},
		{capability.PaymentRoute{Mode: capability.PaymentTest, CheckoutID: "cs_setup_page"}, true},
		{capability.PaymentRoute{Mode: capability.PaymentLive, PaymentID: "payment"}, false},
		{capability.PaymentRoute{Mode: capability.PaymentTest, CheckoutID: "cs_foreign", PaymentID: "pi_foreign", ObjectID: "re_foreign"}, false},
	} {
		got, err := s.RecordsPaymentParent(t.Context(), tc.route)
		if err != nil || got != tc.want {
			t.Fatalf("%+v: %v %v", tc.route, got, err)
		}
	}
}
