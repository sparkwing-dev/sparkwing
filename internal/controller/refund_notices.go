package controller

import (
	"context"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// noticeFailedRefunds tells each team's owners once that a refund Stripe failed or canceled will be handled by hand.
func (s *Server) noticeFailedRefunds(ctx context.Context) {
	notices, err := s.operator.ClaimFailedRefundNotices(ctx)
	if err != nil {
		s.logger.Error("listing failed refunds to announce", "err", err)
		return
	}
	for _, n := range notices {
		cents := n.AmountMicro / store.MicroCreditsPerCent
		s.mailOwners(ctx, n.Team, "Your Sparkwing refund could not be completed",
			fmt.Sprintf("The refund of $%d.%02d for your payment on %s could not be completed. Sparkwing will follow up with you about it.",
				cents/100, cents%100, n.PaidAt.Format("January 2, 2006")), s.billingPageURL())
	}
}
