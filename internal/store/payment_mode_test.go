package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func (o *Operator) setPaymentMode(ctx context.Context, mode capability.PaymentMode) error {
	_, err := o.ConfigureDeployment(ctx, mode, DefaultFreeTeamSlots, func() error { return nil })
	return err
}

func TestPaymentModeTestActivityCanSwitchToLive(t *testing.T) {
	for _, activity := range []string{"grant", "charge", "checkout", "refill"} {
		t.Run(activity, func(t *testing.T) {
			s := authorityStore(t)
			if err := s.AsOperator().setPaymentMode(t.Context(), capability.PaymentTest); err != nil {
				t.Fatal(err)
			}
			switch activity {
			case "grant":
				_, err := (&Tenant{s: s, team: "a"}).RecordCreditGrant(t.Context(), CreditGrantRequest{Kind: CreditGrantFree, AmountMicro: 1, Reference: "first", CreatedBy: "operator"})
				if err != nil {
					t.Fatal(err)
				}
			case "charge":
				err := s.inTx(t.Context(), func(tx *storeTx) error {
					l, err := lockLedgerTx(t.Context(), tx, "a")
					if err != nil {
						return err
					}
					return l.insertCharge(t.Context(), CreditCharge{ID: "first", Team: "a", Kind: CreditChargeStorage, AmountMicro: 1, ChargedAt: s.now()})
				})
				if err != nil {
					t.Fatal(err)
				}
			case "checkout":
				if err := s.inTx(t.Context(), func(tx *storeTx) error {
					_, err := (&Tenant{s: s, team: "a"}).openCreditCheckoutTx(t.Context(), tx, MinCardChargeCents*MicroCreditsPerCent, s.now(), time.Minute)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			case "refill":
				rewriteSchemaExec(t, s.db, `UPDATE teams SET card_customer='customer',card_added_at=1,refill_enabled=1,refill_threshold_cents=1000,refill_amount_cents=1000 WHERE name='a'`)
				if err := s.inTx(t.Context(), func(tx *storeTx) error {
					refill, err := dueRefillTx(t.Context(), tx, "a")
					if err == nil && refill == nil {
						t.Fatal("no refill reserved")
					}
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			if activity == "refill" {
				rewriteSchemaExec(t, s.db, `UPDATE credit_checkouts SET closed_at=? WHERE kind='refill'`, s.now().UnixNano())
			}
			if err := s.AsOperator().setPaymentMode(t.Context(), capability.PaymentLive); err != nil {
				t.Fatalf("live startup after %s: %v", activity, err)
			}
			if err := s.AsOperator().setPaymentMode(t.Context(), capability.PaymentLive); err != nil {
				t.Fatalf("second live startup: %v", err)
			}
			if _, err := s.AsOperator().ConfigureDeployment(t.Context(), capability.PaymentTest, DefaultFreeTeamSlots, func() error { t.Fatal("published a forbidden mode change"); return nil }); err == nil || !strings.Contains(err.Error(), "live to test") {
				t.Fatalf("test startup after live: %v", err)
			}
		})
	}
}

func TestFreshStoreCanSwitchPaymentMode(t *testing.T) {
	s := authorityStore(t)
	for _, mode := range []capability.PaymentMode{capability.PaymentTest, capability.PaymentLive, capability.PaymentLive} {
		if err := s.AsOperator().setPaymentMode(t.Context(), mode); err != nil {
			t.Fatal(err)
		}
	}
}

func TestUnconfiguredLedgerCanSwitchToLive(t *testing.T) {
	s := authorityStore(t)
	if _, err := (&Tenant{s: s, team: "a"}).RecordCreditGrant(t.Context(), CreditGrantRequest{Kind: CreditGrantFree, AmountMicro: 1, Reference: "first", CreatedBy: "operator"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AsOperator().setPaymentMode(t.Context(), capability.PaymentLive); err != nil {
		t.Fatalf("switch unconfigured ledger: %v", err)
	}
}

func TestFixedPaymentModeLedgerReports(t *testing.T) {
	for _, mode := range []capability.PaymentMode{capability.PaymentTest, capability.PaymentLive} {
		t.Run(string(mode), func(t *testing.T) {
			s := authorityStore(t)
			tenant := &Tenant{s: s, team: "a"}
			if err := s.AsOperator().setPaymentMode(t.Context(), mode); err != nil {
				t.Fatal(err)
			}
			if _, err := tenant.RecordCreditGrant(t.Context(), CreditGrantRequest{Kind: CreditGrantFree, AmountMicro: 1000, Reference: "grant", CreatedBy: "operator"}); err != nil {
				t.Fatal(err)
			}
			if err := s.inTx(t.Context(), func(tx *storeTx) error {
				l, err := lockLedgerTx(t.Context(), tx, "a")
				if err != nil {
					return err
				}
				return l.insertCharge(t.Context(), CreditCharge{Team: "a", ID: "charge", Kind: CreditChargeStorage, AmountMicro: 100, ChargedAt: s.now()})
			}); err != nil {
				t.Fatal(err)
			}
			state, err := tenant.CreditState(t.Context(), time.Hour)
			if err != nil || state.BalanceMicro != 900 || state.StorageChargedMicro != 100 || state.BurnMicro != 100 {
				t.Fatalf("state=%+v: %v", state, err)
			}
			grants, err := tenant.ListCreditGrants(t.Context(), 10)
			if err != nil || len(grants) != 1 {
				t.Fatalf("grants=%+v: %v", grants, err)
			}
			charges, err := tenant.ListCreditCharges(t.Context(), 10)
			if err != nil || len(charges) != 1 || charges[0].AmountMicro != 100 {
				t.Fatalf("charges=%+v: %v", charges, err)
			}
			totals, err := s.AsOperator().CreditLedgerTotals(t.Context())
			if err != nil || totals.BalanceMicro != 900 {
				t.Fatalf("totals=%+v: %v", totals, err)
			}
			id, err := tenant.HoldByOperator(t.Context(), "operator", "review")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tenant.ReleaseCreditFreezes(t.Context(), id, "operator", "resolved"); err != nil {
				t.Fatal(err)
			}
			standing, err := tenant.BillingStanding(t.Context())
			if err != nil || standing.History.LastIncidentAt.IsZero() {
				t.Fatalf("standing=%+v: %v", standing, err)
			}
		})
	}
}

func TestLivePaymentModeSurvivesPurge(t *testing.T) {
	s := authorityStore(t)
	tenant := &Tenant{s: s, team: "a"}
	if err := s.AsOperator().setPaymentMode(t.Context(), capability.PaymentLive); err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.RecordCreditGrant(t.Context(), CreditGrantRequest{Kind: CreditGrantFree, AmountMicro: 1, Reference: "first", CreatedBy: "operator"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AsOperator().RequestTeamDeletion(t.Context(), "a"); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.AsOperator().ClaimTeamDeletion(t.Context(), "a", "purger", time.Minute); err != nil || !ok {
		t.Fatalf("claim=%v: %v", ok, err)
	}
	if err := s.AsOperator().PurgeTeam(t.Context(), "a", "purger", time.Hour); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaCount(t, s.db, "credit_grants", 1)
	if err := s.AsOperator().setPaymentMode(t.Context(), capability.PaymentTest); err == nil || !strings.Contains(err.Error(), "live to test") {
		t.Fatalf("test startup after purge: %v", err)
	}
}

func TestLivePaymentModeCannotReturnToTestWithoutActivity(t *testing.T) {
	s := authorityStore(t)
	if err := s.AsOperator().setPaymentMode(t.Context(), capability.PaymentLive); err != nil {
		t.Fatal(err)
	}
	if err := s.AsOperator().setPaymentMode(t.Context(), capability.PaymentTest); err == nil || !strings.Contains(err.Error(), "live to test") {
		t.Fatalf("test startup after live: %v", err)
	}
}

func TestLiveStartExcludesTestLedger(t *testing.T) {
	s := authorityStore(t)
	tenant := &Tenant{s: s, team: "a"}
	grant := CreditGrantRequest{Kind: CreditGrantFree, AmountMicro: 1000, Reference: "same-key", CreatedBy: "operator"}
	if _, err := tenant.RecordCreditGrant(t.Context(), grant); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `INSERT INTO credit_charges(team,id,run_id,node_id,capability_prefix,kind,seconds,amount_micro,charged_at) VALUES ('a','test-charge','','','','storage',0,100,?)`, s.now().UnixNano())
	if err := s.AsOperator().setPaymentMode(t.Context(), capability.PaymentLive); err != nil {
		t.Fatal(err)
	}
	if balance, err := tenant.CreditBalanceMicro(t.Context()); err != nil || balance != 0 {
		t.Errorf("initial live balance=%d: %v", balance, err)
	}
	grant.AmountMicro = 500
	if result, err := tenant.RecordCreditGrant(t.Context(), grant); err != nil || !result.Created {
		t.Fatalf("live grant using a test reference=%+v: %v", result, err)
	}
	if err := s.inTx(t.Context(), func(tx *storeTx) error {
		l, err := lockLedgerTx(t.Context(), tx, "a")
		if err != nil {
			return err
		}
		return l.insertCharge(t.Context(), CreditCharge{ID: "live-charge", Team: "a", Kind: CreditChargeStorage, AmountMicro: 100, ChargedAt: s.now()})
	}); err != nil {
		t.Fatal(err)
	}
	grants, err := tenant.ListCreditGrants(t.Context(), 10)
	if err != nil || len(grants) != 1 || grants[0].AmountMicro != 500 {
		t.Fatalf("live grant history=%+v: %v", grants, err)
	}
	charges, err := tenant.ListCreditCharges(t.Context(), 10)
	if err != nil || len(charges) != 1 || charges[0].ID != "live-charge" {
		t.Fatalf("live charge history=%+v: %v", charges, err)
	}
	for range 2 {
		if err := s.AsOperator().setPaymentMode(t.Context(), capability.PaymentLive); err != nil {
			t.Fatal(err)
		}
		state, err := tenant.CreditState(t.Context(), time.Hour)
		if err != nil || state.GrantedMicro != 500 || state.BalanceMicro != 400 || state.StorageChargedMicro != 100 || state.BurnMicro != 100 {
			t.Errorf("live state=%+v: %v", state, err)
		}
		totals, err := s.AsOperator().CreditLedgerTotals(t.Context())
		if err != nil || totals.BalanceMicro != 400 {
			t.Errorf("live totals=%+v: %v", totals, err)
		}
	}
	rewriteSchemaCount(t, s.db, "credit_grants WHERE payment_mode='test'", 1)
}

func TestLiveCutoverExcludesTestStandingHoldsRefillsAndCards(t *testing.T) {
	s := authorityStore(t)
	tenant := &Tenant{s: s, team: "a"}
	if err := s.AsOperator().setPaymentMode(t.Context(), capability.PaymentTest); err != nil {
		t.Fatal(err)
	}
	now := s.now()
	rewriteSchemaExec(t, s.db, `INSERT INTO credit_grants(team,id,kind,amount_micro,reference,created_at) VALUES ('a','test-paid','paid',?,'test-paid',?)`, int64(60000)*MicroCreditsPerCent, now.Add(-31*24*time.Hour).UnixNano())
	rewriteSchemaExec(t, s.db, `INSERT INTO credit_charges(team,id,run_id,node_id,capability_prefix,kind,seconds,amount_micro,charged_at) VALUES ('a','test-charge','','','','storage',0,100,?)`, now.UnixNano())
	hold, err := tenant.HoldByOperator(t.Context(), "operator", "test review")
	if err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `UPDATE teams SET card_customer='test-customer',card_added_at=1,refill_enabled=1,refill_threshold_cents=1000,refill_amount_cents=1000 WHERE name='a'`)
	rewriteSchemaExec(t, s.db, `INSERT INTO credit_checkouts(id,team,session_id,payment_mode,currency,amount_minor,amount_micro,opened_at,expires_at,customer_id,kind,closed_at) VALUES ('test-open','a','test-open','test','usd',1000,?, ?,?,'test-customer','refill',1)`, 1000*MicroCreditsPerCent, now.UnixNano(), now.Add(time.Hour).UnixNano())
	rewriteSchemaExec(t, s.db, `INSERT INTO credit_checkouts(id,team,session_id,payment_mode,currency,amount_minor,amount_micro,opened_at,expires_at,customer_id,kind,closed_at,failure,refill_blocked,payment_id) VALUES ('test-failed','a','test-failed','test','usd',1000,?, ?,?,'test-customer','refill',?,'declined',1,'test-payment')`, 1000*MicroCreditsPerCent, now.UnixNano(), now.Add(time.Hour).UnixNano(), now.UnixNano())
	rewriteSchemaExec(t, s.db, `INSERT INTO payment_warnings(payment_mode,warning_id,payment_intent,actionable,created_at) VALUES ('test','test-warning','test-payment',1,1)`)
	if err := s.AsOperator().setPaymentMode(t.Context(), capability.PaymentLive); err != nil {
		t.Fatal(err)
	}
	standing, err := tenant.SpendStanding(t.Context())
	if err != nil || standing.BalanceMicro != 0 || standing.Billing.Tier != "New" || standing.Billing.History.PaidMicro != 0 || !standing.Billing.History.FirstPaidAt.IsZero() || !standing.Billing.History.LastIncidentAt.IsZero() || standing.Billing.HoldCause != "" || !standing.Card.AddedAt.IsZero() || standing.Refill.Enabled || standing.Refill.Open || standing.Refill.DayCount != 0 || !standing.Refill.FailedAt.IsZero() {
		t.Fatalf("live standing contains test activity: %+v: %v", standing, err)
	}
	freeze, err := tenant.CreditFreeze(t.Context())
	if err != nil || freeze.Frozen || len(freeze.Disputes) != 0 {
		t.Fatalf("live freeze=%+v: %v", freeze, err)
	}
	if released, err := tenant.ReleaseCreditFreezes(t.Context(), hold, "operator", "live release"); err != nil || released != 0 {
		t.Fatalf("live release changed a test hold: %d: %v", released, err)
	}
	if err := s.inTx(t.Context(), func(tx *storeTx) error {
		if err := saveCardTx(t.Context(), tx, "a", Card{Customer: "live-customer"}, "seti_live", auditActor{system: auditPaymentProvider}, now); err != nil {
			return err
		}
		return setRefillTx(t.Context(), tx, "a", RefillSettings{Enabled: true, ThresholdCents: 1000, AmountCents: 1000}, auditAccountActor("operator"))
	}); err != nil {
		t.Fatal(err)
	}
	// Starting live again must preserve the live card, opt-in and in-flight work.
	for range 2 {
		if err := s.AsOperator().setPaymentMode(t.Context(), capability.PaymentLive); err != nil {
			t.Fatal(err)
		}
		refills, err := s.AsOperator().DueRefills(t.Context())
		if err != nil || len(refills) != 1 || refills[0].Customer != "live-customer" || refills[0].ID == "test-open" {
			t.Fatalf("live refill=%+v: %v", refills, err)
		}
	}
	rewriteSchemaCount(t, s.db, "credit_checkouts WHERE payment_mode='test'", 2)
	rewriteSchemaCount(t, s.db, "credit_freezes WHERE payment_mode='test' AND released_at IS NULL", 1)
}

func TestLiveCutoverRollsBackIfDeploymentPublicationFails(t *testing.T) {
	s := authorityStore(t)
	if _, err := (&Tenant{s: s, team: "a"}).RecordCreditGrant(t.Context(), CreditGrantRequest{Kind: CreditGrantFree, AmountMicro: 1, Reference: "test", CreatedBy: "operator"}); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `UPDATE teams SET card_customer='test-customer',card_added_at=1 WHERE name='a'`)
	rewriteSchemaExec(t, s.db, `INSERT INTO team_storage(team,store,updated_at,storage_charged_through) VALUES ('a','cache',1,7),('a','logs',1,7)`)
	if _, err := s.AsOperator().ConfigureDeployment(t.Context(), capability.PaymentLive, DefaultFreeTeamSlots, func() error { return context.Canceled }); err != context.Canceled {
		t.Fatalf("publication failure=%v", err)
	}
	if err := s.AsOperator().setPaymentMode(t.Context(), capability.PaymentTest); err != nil {
		t.Fatalf("failed cutover persisted live mode: %v", err)
	}
	rewriteSchemaCount(t, s.db, "teams WHERE name='a' AND card_customer='test-customer'", 1)
	rewriteSchemaCount(t, s.db, "team_storage WHERE storage_charged_through=7", 2)
}

func TestSettledTestReservationRefundNeverFundsLiveBalance(t *testing.T) {
	s, demand, _ := billingModelFixture(t)
	claim, _ := meteredUnitClaim(t, s, demand)
	if err := s.ReleaseUnit(t.Context(), claim, claim.Award.CapabilityPrefix); err != nil {
		t.Fatal(err)
	}
	if err := s.AsOperator().setPaymentMode(t.Context(), capability.PaymentLive); err != nil {
		t.Fatal(err)
	}
	totals, err := s.AsOperator().CreditLedgerTotals(t.Context())
	if err != nil || totals.BalanceMicro != 0 || totals.SettledSeconds != 0 {
		t.Fatalf("live totals contain a test award: %+v: %v", totals, err)
	}
	state, err := (&Tenant{s: s, team: "a"}).CreditState(t.Context(), time.Hour)
	if err != nil || state.BalanceMicro != 0 || state.ChargedMicro != 0 {
		t.Fatalf("test refund changed live balance: %+v: %v", state, err)
	}
	rewriteSchemaCount(t, s.db, "credit_charges WHERE kind='refund' AND payment_mode='test'", 1)
}

func TestLiveCutoverRequiresDrainedTestWork(t *testing.T) {
	for _, activity := range []string{"reservation", "award", "refill"} {
		t.Run(activity, func(t *testing.T) {
			s, demand, _ := billingModelFixture(t)
			if err := s.AsOperator().setPaymentMode(t.Context(), capability.PaymentTest); err != nil {
				t.Fatal(err)
			}
			if activity == "refill" {
				rewriteSchemaExec(t, s.db, `UPDATE teams SET refill_enabled=1,refill_threshold_cents=1000,refill_amount_cents=1000 WHERE name='a'`)
				refills, err := s.AsOperator().DueRefills(t.Context())
				if err != nil || len(refills) != 1 {
					t.Fatalf("refills=%+v: %v", refills, err)
				}
			} else {
				if activity == "award" {
					_, actor := meteredUnitClaim(t, s, demand)
					tenant := &Tenant{s: s, team: "a"}
					admitExecution(t, tenant, actor)
					if _, err := tenant.TransitionNode(t.Context(), nodeTransition{Ref: runspec.NodeRef{RunID: "run", NodeID: "job"}, Actor: actor, To: runstate.NodeRunning, Cause: runstate.CauseStart, SourceAttempt: sourceAttempt(0)}); err != nil {
						t.Fatal(err)
					}
				} else if _, err := s.AsOperator().ClaimUnit(t.Context(), demand); err != nil {
					t.Fatal(err)
				}
			}
			_, err := s.AsOperator().ConfigureDeployment(t.Context(), capability.PaymentLive, DefaultFreeTeamSlots, func() error { t.Error("published before draining test work"); return nil })
			if err == nil || !strings.Contains(err.Error(), "drain metered work before switching to live; see the cutover runbook") {
				t.Fatalf("live startup with open %s: %v", activity, err)
			}
			mode, err := scanPaymentMode(s.db.QueryRowContext(t.Context(), `SELECT value FROM settings WHERE key='payment_mode'`))
			if err != nil || mode != capability.PaymentTest {
				t.Fatalf("refused startup changed mode: %s: %v", mode, err)
			}
		})
	}
}

func TestLiveCutoverRestartsStorageAccounting(t *testing.T) {
	s, now := retainedBillingFixture(t)
	retainedBlob(t, s, "a", "a", StorageBytesPerGB, now.Add(-time.Hour))
	retainedBill(t, s)
	*now = now.Add(time.Hour)
	cutover := *now
	if err := s.AsOperator().setPaymentMode(t.Context(), capability.PaymentLive); err != nil {
		t.Fatal(err)
	}
	tenant := &Tenant{s: s, team: "a"}
	if _, err := tenant.RecordCreditGrant(t.Context(), CreditGrantRequest{Kind: CreditGrantFree, AmountMicro: 1000000, Reference: "live", CreatedBy: "operator"}); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(time.Minute)
	if err := s.AsOperator().setPaymentMode(t.Context(), capability.PaymentLive); err != nil {
		t.Fatal(err)
	}
	billed := retainedBill(t, s)
	if len(billed.Charges) != 1 || billed.Charges[0].Seconds != 60 {
		t.Fatalf("live billing includes test time or restarted live watermark: %+v (cutover %s)", billed, cutover)
	}
}
