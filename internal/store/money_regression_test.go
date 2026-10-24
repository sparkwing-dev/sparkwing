package store

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestDeletionRetainsFailedRefundAfterCheckoutExpiry(t *testing.T) {
	s, _, now := providerPaymentFixture(t)
	rewriteSchemaExec(t, s.db, `INSERT INTO payment_cards(fingerprint,first_team,payment_intent,pinned_at) VALUES ('pinned','b','original',1)`)
	event := capability.PaymentEvent{EventID: "refused", CheckoutID: "provider-session", PaymentID: "payment", Currency: "usd", AmountMinor: 100, Mode: capability.PaymentTest, Kind: capability.PaymentCheckoutPaid, Fingerprint: "pinned"}
	if _, err := s.settlePaymentFacts(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	if err := s.AsOperator().MarkCardRefundMade(t.Context(), "a", "payment-0", "refund"); err != nil {
		t.Fatal(err)
	}
	refund := providerPaymentEvent(now, capability.PaymentRefund)
	refund.ObjectID, refund.Status = "refund", "failed"
	if _, err := s.settlePaymentFacts(t.Context(), refund); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `UPDATE credit_checkouts SET expires_at=1`)
	if _, _, err := s.AsOperator().RequestTeamDeletion(t.Context(), "a"); err == nil {
		t.Fatal("deletion discarded an unfinished refund after checkout expiry")
	}
	rewriteSchemaCount(t, s.db, "card_refunds", 1)
}

func TestRevokedStandingReportsCreditFreeze(t *testing.T) {
	s := authorityStore(t)
	rewriteSchemaExec(t, s.db, `UPDATE teams SET billing_trust='revoked' WHERE name='a'`)
	tenant := &Tenant{s: s, team: "a"}
	freeze, err := tenant.CreditFreeze(t.Context())
	if err != nil || !freeze.Frozen {
		t.Fatalf("revoked freeze=%+v: %v", freeze, err)
	}
	standing, err := tenant.BillingStanding(t.Context())
	if err != nil || standing.HoldCause != holdCauseRevoked {
		t.Fatalf("standing=%+v: %v", standing, err)
	}
}

func TestPaymentModeScopesBalanceStandingAndHistory(t *testing.T) {
	s, tenant, _ := providerPaymentFixture(t)
	providerCheckoutPaid(t, s)
	for _, mode := range []capability.PaymentMode{capability.PaymentLive, capability.PaymentTest} {
		if err := s.AsOperator().SetPaymentMode(t.Context(), mode); err != nil {
			t.Fatal(err)
		}
		want := int64(0)
		if mode == capability.PaymentTest {
			want = 100 * MicroCreditsPerCent
		}
		balance, err := tenant.CreditBalanceMicro(t.Context())
		if err != nil || balance != want {
			t.Errorf("%s balance=%d: %v; want %d", mode, balance, err, want)
		}
		state, err := tenant.CreditState(t.Context(), time.Hour)
		if err != nil || state.BalanceMicro != want {
			t.Errorf("%s state=%+v: %v", mode, state, err)
		}
		history, err := tenant.ListCreditGrants(t.Context(), 10)
		if err != nil || (len(history) == 0) != (want == 0) {
			t.Errorf("%s history=%+v: %v", mode, history, err)
		}
		standing, err := tenant.BillingStanding(t.Context())
		if err != nil || standing.History.PaidMicro != want {
			t.Errorf("%s standing=%+v: %v", mode, standing, err)
		}
	}
}

func TestOperatorGrantRequiresKeyAndAllowsFreeCorrection(t *testing.T) {
	s := authorityStore(t)
	tenant := &Tenant{s: s, team: "a"}
	req := CreditGrantRequest{Kind: CreditGrantFree, AmountMicro: 100}
	if _, err := tenant.RecordCreditGrant(t.Context(), req); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("unkeyed grant: %v", err)
	}
	req.Reference = "mistake"
	if _, err := tenant.RecordCreditGrant(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	correction := CreditGrantRequest{Kind: creditGrantReversal, AmountMicro: -100, Reference: "correction", Reverses: "mistake"}
	for replay := range 2 {
		result, err := tenant.RecordCreditGrant(t.Context(), correction)
		if err != nil || result.Created != (replay == 0) {
			t.Fatalf("correction=%+v: %v", result, err)
		}
	}
	balance, err := tenant.CreditBalanceMicro(t.Context())
	if err != nil || balance != 0 {
		t.Fatalf("corrected balance=%d: %v", balance, err)
	}
	correction.Reference = "over-correction"
	if _, err := tenant.RecordCreditGrant(t.Context(), correction); err == nil {
		t.Fatal("reversed more than the free grant")
	}
}

func TestReversalMustReferenceExistingGrant(t *testing.T) {
	s := authorityStore(t)
	rewriteSchemaRefuses(t, s.db, `INSERT INTO credit_grants(team,id,kind,amount_micro,reference,reverses,created_at) VALUES ('a','bad','reversal',-1,'refund','missing',1)`)
}

func TestBillingModelJobsHaveTheirOwnSpecIdentity(t *testing.T) {
	s, _, _ := billingModelFixture(t)
	billingModelJob(t, s, "second")
	var raw []byte
	if err := s.db.QueryRow(`SELECT spec_json FROM nodes WHERE team='a' AND node_id='second'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var spec struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil || spec.ID != "second" {
		t.Fatalf("second node spec=%s: %v", raw, err)
	}
}

func TestPaymentModeSeparatesChargesFreeGrantsAndIncidents(t *testing.T) {
	s, tenant, now := providerPaymentFixture(t)
	for _, mode := range []capability.PaymentMode{capability.PaymentTest, capability.PaymentLive} {
		if err := s.AsOperator().SetPaymentMode(t.Context(), mode); err != nil {
			t.Fatal(err)
		}
		if _, err := tenant.RecordCreditGrant(t.Context(), CreditGrantRequest{Kind: CreditGrantFree, AmountMicro: 1000, Reference: "same-key"}); err != nil {
			t.Fatal(err)
		}
		amount := int64(100)
		if mode == capability.PaymentLive {
			amount = 200
		}
		if err := s.inTx(t.Context(), func(tx *storeTx) error {
			l, err := lockLedgerTx(t.Context(), tx, "a")
			if err != nil {
				return err
			}
			return l.insertCharge(t.Context(), CreditCharge{ID: string(mode), Team: "a", Kind: CreditChargeStorage, AmountMicro: amount, ChargedAt: now})
		}); err != nil {
			t.Fatal(err)
		}
		if mode == capability.PaymentTest {
			id, err := tenant.HoldByOperator(t.Context(), "operator", "review")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tenant.ReleaseCreditFreezes(t.Context(), id, "operator", "resolved"); err != nil {
				t.Fatal(err)
			}
			if _, err := tenant.HoldByOperator(t.Context(), "operator", "test only"); err != nil {
				t.Fatal(err)
			}
		} else {
			freeze, err := tenant.CreditFreeze(t.Context())
			if err != nil || freeze.Frozen {
				t.Fatalf("test hold leaked into live: %+v: %v", freeze, err)
			}
			standing, err := tenant.BillingStanding(t.Context())
			if err != nil || !standing.History.LastIncidentAt.IsZero() {
				t.Fatalf("test incident leaked into live: %+v: %v", standing, err)
			}
		}
		state, err := tenant.CreditState(t.Context(), time.Hour)
		if err != nil || state.BalanceMicro != 1000-amount || state.StorageChargedMicro != amount || state.BurnMicro != amount {
			t.Fatalf("%s state=%+v: %v", mode, state, err)
		}
		totals, err := s.AsOperator().CreditLedgerTotals(t.Context())
		if err != nil || totals.BalanceMicro != 1000-amount {
			t.Fatalf("%s totals=%+v: %v", mode, totals, err)
		}
		charges, err := tenant.ListCreditCharges(t.Context(), 10)
		if err != nil || len(charges) != 1 || charges[0].AmountMicro != amount {
			t.Fatalf("%s charges=%+v: %v", mode, charges, err)
		}
	}
}

func TestPurgeRetainsRefundQueuedAfterDeletionRequest(t *testing.T) {
	s, _, now := providerPaymentFixture(t)
	rewriteSchemaExec(t, s.db, `UPDATE credit_checkouts SET expires_at=1`)
	if _, _, err := s.AsOperator().RequestTeamDeletion(t.Context(), "a"); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `INSERT INTO card_refunds(payment_mode,team,payment_intent,reason,created_at) VALUES ('test','a','late','refused',?)`, now.UnixNano())
	if ok, err := s.AsOperator().ClaimTeamDeletion(t.Context(), "a", "purger", time.Minute); err != nil || !ok {
		t.Fatalf("claim=%v: %v", ok, err)
	}
	if err := s.AsOperator().PurgeTeam(t.Context(), "a", "purger", time.Hour); err == nil {
		t.Fatal("purge discarded a late refund obligation")
	}
	rewriteSchemaCount(t, s.db, "card_refunds", 1)
}

func TestModeSwitchCannotRefundTestReservationIntoLiveBalance(t *testing.T) {
	s, demand, _ := meteredUnitFixture(t, true)
	claim, _ := meteredUnitClaim(t, s, demand)
	if err := s.AsOperator().SetPaymentMode(t.Context(), capability.PaymentLive); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseUnit(t.Context(), claim, claim.Award.CapabilityPrefix); err != nil {
		t.Fatal(err)
	}
	balance, err := (&Tenant{s: s, team: "a"}).CreditBalanceMicro(t.Context())
	if err != nil || balance != 0 {
		t.Fatalf("test reservation credited live balance=%d: %v", balance, err)
	}
}

func TestModeSwitchCannotRenewTestAwardWithLiveFunds(t *testing.T) {
	s, demand, now := meteredUnitFixture(t, true)
	claim, _ := meteredUnitClaim(t, s, demand)
	if err := s.AsOperator().SetPaymentMode(t.Context(), capability.PaymentLive); err != nil {
		t.Fatal(err)
	}
	tenant := &Tenant{s: s, team: "a"}
	if _, err := tenant.RecordCreditGrant(t.Context(), CreditGrantRequest{Kind: CreditGrantFree, AmountMicro: 100000, Reference: "live-funds"}); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(30 * time.Second)
	beat, err := s.AsOperator().RenewUnit(t.Context(), claim.Award.CapabilityPrefix, true)
	if err == nil && !beat.CancelRequested {
		t.Fatalf("test award renewed with live funds: %+v", beat)
	}
	balance, err := tenant.CreditBalanceMicro(t.Context())
	if err != nil || balance != 100000 {
		t.Fatalf("test award changed live balance=%d: %v", balance, err)
	}
}
