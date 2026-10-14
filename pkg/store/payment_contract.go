package store

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// PaymentSettlement reports the committed checkout result without accepting caller-selected credits.
type PaymentSettlement struct {
	Team        Team
	CheckoutID  string
	GrantID     string
	AmountMicro int64
	Replayed    bool
}

// SettlePayment accepts only C2-verified provider events. It refuses a zero proof,
// mismatched configured mode, amount or currency, and any conflicting payment ID.
// The provider checkout ID selects a previously committed credit_checkouts row;
// that row alone supplies team and credit value. A previously unset payment ID is
// bound once from the verified event for that checkout, never from browser data.
// The transaction locks the checkout and deduplicates (mode,event ID), checks the
// canonical event digest, and records the event, checkout outcome and paid credit
// grant together. A paid checkout grants once even under distinct paid event IDs;
// failed/expired events cannot undo payment. Financial rows have no run cascade.
func (s *Store) SettlePayment(ctx context.Context, event capability.VerifiedPaymentEvent) (PaymentSettlement, error) {
	panic("unimplemented: c1-payment")
}
