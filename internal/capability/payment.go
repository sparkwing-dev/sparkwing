package capability

import "context"

// PaymentMode selects provider credentials without changing verification or settlement code.
type PaymentMode string

const (
	PaymentTest PaymentMode = "test"
	PaymentLive PaymentMode = "live"
)

// PaymentConfig is supplied from operator configuration and private credential files.
// Both modes use the same endpoint, verifier, event table and store settlement transaction.
type PaymentConfig struct {
	Mode              PaymentMode
	APIKeyFile        string
	WebhookSecretFile string
}

// PaymentEventKind limits automated writes to outcomes of committed checkouts.
type PaymentEventKind string

const (
	PaymentCheckoutPaid    PaymentEventKind = "checkout_paid"
	PaymentCheckoutExpired PaymentEventKind = "checkout_expired"
	PaymentCheckoutFailed  PaymentEventKind = "checkout_failed"
)

// PaymentEventPolicy maps authenticated provider events to checkout settlement outcomes.
type PaymentEventPolicy struct {
	ProviderType string
	Kind         PaymentEventKind
	RequirePaid  bool
}

// PaymentEventPolicies is the complete accepted Stripe settlement-event inventory.
// Completed sessions without paid status remain pending until an asynchronous success event.
var PaymentEventPolicies = []PaymentEventPolicy{
	{"checkout.session.completed", PaymentCheckoutPaid, true},
	{"checkout.session.async_payment_succeeded", PaymentCheckoutPaid, true},
	{"checkout.session.async_payment_failed", PaymentCheckoutFailed, false},
	{"checkout.session.expired", PaymentCheckoutExpired, false},
}

// PaymentEvent carries verified provider facts, never a team or caller-selected credit amount.
// CheckoutID is the provider session ID recorded on the committed checkout.
type PaymentEvent struct {
	EventID     string
	CheckoutID  string
	PaymentID   string
	Currency    string
	AmountMinor int64
	Mode        PaymentMode
	Kind        PaymentEventKind
}

// VerifiedPaymentEvent can originate only from payment signature verification.
// Its zero value carries no event and must be refused by settlement.
type VerifiedPaymentEvent struct {
	event PaymentEvent
}

// Event exposes authenticated facts for the store's checkout and replay checks.
func (v VerifiedPaymentEvent) Event() PaymentEvent {
	panic("unimplemented: c2-credentials")
}

// PaymentPresentation preserves the exact signed bytes until verification completes.
type PaymentPresentation struct {
	Body      []byte
	Signature string
}

// PaymentAuthenticator verifies the signature, timestamp, configured mode and supported event type.
// It never trusts browser return parameters or provider metadata for team, credits or settlement.
// An authenticated but pending/unsupported event returns no settlement event.
type PaymentAuthenticator interface {
	VerifyPaymentEvent(context.Context, PaymentPresentation) (*VerifiedPaymentEvent, error)
}

// NewPaymentAuthenticator binds one verifier to the configured private credential files.
func NewPaymentAuthenticator(config PaymentConfig) (PaymentAuthenticator, error) {
	panic("unimplemented: c2-credentials")
}
