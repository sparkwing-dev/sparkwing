package store

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func TestWarningLeavesRunningWorkToLeaseDrain(t *testing.T) {
	s, demand, now := billingModelFixture(t)
	claim, actor := meteredUnitClaim(t, s, demand)
	tenant := &Tenant{s: s, team: "a"}
	admitExecution(t, tenant, actor)
	if _, err := tenant.TransitionNode(t.Context(), nodeTransition{Ref: runspec.NodeRef{RunID: "run", NodeID: "job"}, Actor: actor, To: runstate.NodeRunning, Cause: runstate.CauseStart, SourceAttempt: sourceAttempt(0)}); err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.TestOnlySettlePaidCredit(t.Context(), CreditGrantRequest{Kind: CreditGrantPaid, AmountMicro: 100 * MicroCreditsPerCent, Reference: "payment"}); err != nil {
		t.Fatal(err)
	}
	warning := providerPaymentEvent(*now, capability.PaymentWarningEvent)
	warning.Actionable = true
	if _, err := s.settlePaymentFacts(t.Context(), warning); err != nil {
		t.Fatal(err)
	}
	var drain int64
	if err := s.db.QueryRow(`SELECT drain_deadline_at FROM credit_freezes WHERE team='a'`).Scan(&drain); err != nil || drain != now.Add(SliceSeconds*time.Second).UnixNano() {
		t.Fatalf("financial hold drain=%d: %v", drain, err)
	}
	rewriteSchemaCount(t, s.db, "nodes WHERE cancel_requested_at IS NOT NULL", 0)
	rewriteSchemaCount(t, s.db, "runs WHERE cancel_requested_at IS NOT NULL", 0)
	rewriteSchemaCount(t, s.db, "billing_awards WHERE stop_reason<>'' OR settled_at IS NOT NULL", 0)
	*now = now.Add(10 * time.Second)
	beat, err := s.AsOperator().RenewUnit(t.Context(), claim.Award.CapabilityPrefix, true)
	if err != nil || !beat.CancelRequested || beat.ExpiresAt.After(now.Add(SliceSeconds*time.Second)) {
		t.Fatalf("held work renewal=%+v: %v", beat, err)
	}
	var awardDrain int64
	if err := s.db.QueryRow(`SELECT drain_deadline_at FROM billing_awards WHERE team='a'`).Scan(&awardDrain); err != nil || awardDrain != drain {
		t.Fatalf("renewal moved financial drain from %d to %d: %v", drain, awardDrain, err)
	}
}

func TestPurgeSettlesStartedAwardsBeforeLateWarning(t *testing.T) {
	s, demand, now := billingModelFixture(t)
	_, actor := meteredUnitClaim(t, s, demand)
	tenant := &Tenant{s: s, team: "a"}
	admitExecution(t, tenant, actor)
	if _, err := tenant.TransitionNode(t.Context(), nodeTransition{Ref: runspec.NodeRef{RunID: "run", NodeID: "job"}, Actor: actor, To: runstate.NodeRunning, Cause: runstate.CauseStart, SourceAttempt: sourceAttempt(0)}); err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.TestOnlySettlePaidCredit(t.Context(), CreditGrantRequest{Kind: CreditGrantPaid, AmountMicro: 100 * MicroCreditsPerCent, Reference: "payment"}); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(75 * time.Second)
	purgePaymentTeam(t, s, "a")
	rewriteSchemaCount(t, s.db, "billing_awards WHERE settled_at IS NULL", 0)
	rewriteSchemaCount(t, s.db, "billable_intervals WHERE finished_at IS NULL", 0)
	unitBillingTotal(t, s, 750, true)
	warning := providerPaymentEvent(*now, capability.PaymentWarningEvent)
	warning.Actionable = true
	if _, err := s.settlePaymentFacts(t.Context(), warning); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaCount(t, s.db, "credit_freezes WHERE released_at IS NULL", 1)
	rewriteSchemaCount(t, s.db, "card_refunds WHERE payment_intent='payment'", 1)
}

func TestRefundWorkerBindsAndRetriesAfterPurge(t *testing.T) {
	s, _, now := providerPaymentFixture(t)
	providerCheckoutPaid(t, s)
	warning := providerPaymentEvent(now, capability.PaymentWarningEvent)
	warning.Actionable = true
	if _, err := s.settlePaymentFacts(t.Context(), warning); err != nil {
		t.Fatal(err)
	}
	purgePaymentTeam(t, s, "a")
	for attempt, status := range []string{"failed", "succeeded"} {
		due, err := s.AsOperator().DueCardRefunds(t.Context())
		if err != nil || len(due) != 1 {
			t.Fatalf("due attempt %d=%+v: %v", attempt, due, err)
		}
		refund := providerPaymentEvent(now, capability.PaymentRefund)
		refund.ObjectID, refund.EventID, refund.Status = "refund-"+status, "event-"+status, status
		if err := s.AsOperator().MarkCardRefundMade(t.Context(), "a", due[0].Key, refund.ObjectID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.settlePaymentFacts(t.Context(), refund); err != nil {
			t.Fatal(err)
		}
		s.SetTestClock(t, func() time.Time { return now.Add(2 * time.Hour) })
	}
	rewriteSchemaCount(t, s.db, "card_refunds WHERE status='succeeded' AND attempts=1", 1)
}
