package store

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// A queued refund Stripe fails is announced to the owners once: not while it is pending, and never again after.
func TestAFailedQueuedRefundIsAnnouncedOnce(t *testing.T) {
	s, _, now := queuedRefundFixture(t, false)
	ctx := t.Context()
	s.SetTestClock(t, func() time.Time { return now })
	if err := s.AsOperator().MarkCardRefundMade(ctx, "a", "payment", "re_1"); err != nil {
		t.Fatal(err)
	}
	if notices, err := s.AsOperator().ClaimFailedRefundNotices(ctx); err != nil || len(notices) != 0 {
		t.Fatalf("pending refund notices = %+v, %v; want none", notices, err)
	}
	event := providerPaymentEvent(now, capability.PaymentRefund)
	event.PaymentID, event.ObjectID, event.Status, event.AmountMinor = "payment", "re_1", "failed", 1_000
	if _, err := s.settlePaymentFacts(ctx, event); err != nil {
		t.Fatal(err)
	}
	notices, err := s.AsOperator().ClaimFailedRefundNotices(ctx)
	if err != nil || len(notices) != 1 || notices[0].Team != "a" || notices[0].AmountMicro != 1_000*MicroCreditsPerCent || notices[0].PaidAt.IsZero() {
		t.Fatalf("failed refund notices = %+v, %v; want one for the whole payment", notices, err)
	}
	if _, err := s.settlePaymentFacts(ctx, event); err != nil {
		t.Fatal(err)
	}
	if again, err := s.AsOperator().ClaimFailedRefundNotices(ctx); err != nil || len(again) != 0 {
		t.Fatalf("second claim = %+v, %v; want the refund announced only once", again, err)
	}
}
