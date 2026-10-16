package capability

import "time"

// BillingPageKind identifies the committed operation behind a hosted payment page.
type BillingPageKind string

const (
	BillingPageCredits  BillingPageKind = "credits"
	BillingPageCard     BillingPageKind = "card"
	BillingPageRecovery BillingPageKind = "recovery"
)

// BillingPage carries store-derived reservation facts and the provider's completed page.
type BillingPage struct {
	Kind          BillingPageKind
	Team          string
	ReservationID string
	AmountCents   int64
	Customer      string
	Email         string
	ChargeID      string
	AttemptID     string
	ProviderID    string
	IntentID      string
	URL           string
	ExpiresAt     time.Time
}
