package capability

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
)

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
	HTTPTransport     http.RoundTripper
}

// PaymentEventKind identifies a verified provider outcome.
type PaymentEventKind string

const (
	PaymentCheckoutPaid    PaymentEventKind = "checkout_paid"
	PaymentCheckoutExpired PaymentEventKind = "checkout_expired"
	PaymentCheckoutFailed  PaymentEventKind = "checkout_failed"
	PaymentCardSetup       PaymentEventKind = "card_setup"
	PaymentCardPaid        PaymentEventKind = "card_paid"
	PaymentCardFailed      PaymentEventKind = "card_failed"
	PaymentRefund          PaymentEventKind = "refund"
	PaymentWarningEvent    PaymentEventKind = "warning"
	PaymentDisputeOpened   PaymentEventKind = "dispute_opened"
	PaymentDisputeClosed   PaymentEventKind = "dispute_closed"
	PaymentPageCompleted   PaymentEventKind = "page_completed"
)

// PaymentEventPolicy maps provider types to verified settlement outcomes.
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
	{"setup_intent.succeeded", PaymentCardSetup, false},
	{"payment_intent.succeeded", PaymentCardPaid, false},
	{"payment_intent.payment_failed", PaymentCardFailed, false},
	{"refund.created", PaymentRefund, false},
	{"refund.updated", PaymentRefund, false},
	{"refund.failed", PaymentRefund, false},
	{"radar.early_fraud_warning.created", PaymentWarningEvent, false},
	{"radar.early_fraud_warning.updated", PaymentWarningEvent, false},
	{"charge.dispute.created", PaymentDisputeOpened, false},
	{"charge.dispute.closed", PaymentDisputeClosed, false},
}

// PaymentEvent carries provider facts whose team and ledger effect come from committed records.
type PaymentEvent struct {
	EventID     string
	CheckoutID  string
	PaymentID   string
	Currency    string
	AmountMinor int64
	Mode        PaymentMode
	Kind        PaymentEventKind
	ObjectID    string
	CustomerID  string
	MethodID    string
	ChargeID    string
	Status      string
	Reason      string
	DeclineCode string
	Fingerprint string
	Brand       string
	Last4       string
	ThreeDS     string
	IntentID    string
	Created     int64
	Actionable  bool
}

// Identity is the event as Stripe signed it. Fingerprint, Brand, Last4 and ThreeDS come from provider reads made
// after verification, so a replay identifies the event without them.
func (e PaymentEvent) Identity() PaymentEvent {
	e.Fingerprint, e.Brand, e.Last4, e.ThreeDS = "", "", "", ""
	return e
}

// VerifiedPaymentEvent can originate only from payment signature verification.
// Its zero value carries no event and must be refused by settlement.
type VerifiedPaymentEvent struct {
	event PaymentEvent
}

// Event exposes authenticated facts for the store's checkout and replay checks.
func (v VerifiedPaymentEvent) Event() PaymentEvent {
	return v.event
}

// PaymentPresentation preserves the exact signed bytes until verification completes.
type PaymentPresentation struct {
	Body      []byte
	Signature string
}

// PaymentAuthenticator verifies the signature, timestamp, configured mode and supported event type,
// then classifies ownership before any provider read. Provider metadata only routes an event: it names
// the deployment and purpose of an object. Team, amount and payer always come from committed records.
// An authenticated event that is pending, unhandled, another deployment's or unowned returns no event.
type PaymentAuthenticator interface {
	VerifyPaymentEvent(context.Context, PaymentPresentation, PaymentScope) (*VerifiedPaymentEvent, error)
}

// NewPaymentAuthenticator binds one verifier to the configured private credential files.
func NewPaymentAuthenticator(config PaymentConfig) (PaymentAuthenticator, error) {
	if config.Mode != PaymentTest && config.Mode != PaymentLive {
		return nil, errors.New("payment mode must be test or live")
	}
	key, err := readPaymentSecret(config.APIKeyFile)
	if err != nil {
		return nil, fmt.Errorf("payment API key: %w", err)
	}
	if !strings.HasPrefix(key, "sk_"+string(config.Mode)+"_") && !strings.HasPrefix(key, "rk_"+string(config.Mode)+"_") {
		return nil, errors.New("payment API key does not match configured mode")
	}
	secret, err := readPaymentSecret(config.WebhookSecretFile)
	if err != nil {
		return nil, fmt.Errorf("payment webhook secret: %w", err)
	}
	if !strings.HasPrefix(secret, "whsec_") || len(secret) == len("whsec_") {
		return nil, errors.New("invalid payment webhook secret")
	}
	provider := newPaymentProvider(key)
	if config.HTTPTransport != nil {
		provider.client.Transport = config.HTTPTransport
	}
	return &paymentAuthenticator{mode: config.Mode, secret: []byte(secret), now: time.Now, provider: provider}, nil
}

func readPaymentSecret(path string) (string, error) {
	f, err := fssecure.OpenCredential(filepath.Dir(path), filepath.Base(path))
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(f)
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(data))
	if value == "" {
		return "", errors.New("credential file is empty")
	}
	return value, nil
}

type paymentAuthenticator struct {
	mode     PaymentMode
	secret   []byte
	now      func() time.Time
	provider *paymentProvider
}

func (a *paymentAuthenticator) VerifyPaymentEvent(ctx context.Context, presentation PaymentPresentation, scope PaymentScope) (*VerifiedPaymentEvent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !a.validSignature(presentation) {
		return nil, unauthenticated()
	}
	var event struct {
		ID       string `json:"id"`
		Object   string `json:"object"`
		Type     string `json:"type"`
		Created  int64  `json:"created"`
		LiveMode *bool  `json:"livemode"`
		Data     struct {
			Object json.RawMessage `json:"object"`
		} `json:"data"`
	}
	if err := json.Unmarshal(presentation.Body, &event); err != nil || event.ID == "" || event.Object != "event" || event.Type == "" {
		return nil, invalidPaymentEvent()
	}
	if event.LiveMode == nil || *event.LiveMode != (a.mode == PaymentLive) {
		return nil, forbidden(apierr.ReasonBinding)
	}
	for _, policy := range PaymentEventPolicies {
		if policy.ProviderType != event.Type {
			continue
		}
		route, owned, err := a.classify(ctx, policy, event.Data.Object, scope)
		if err != nil || !owned {
			return nil, err
		}
		switch route.Kind {
		case PaymentPageCompleted:
			return a.pageEvent(event.ID, event.Created, event.Data.Object)
		case PaymentCheckoutPaid, PaymentCheckoutExpired, PaymentCheckoutFailed:
			return a.checkoutEvent(ctx, event.ID, event.Data.Object, policy)
		}
		return a.providerEvent(ctx, event.ID, event.Created, event.Data.Object, policy)
	}
	return nil, nil
}

// safety: Stripe retries receive fresh signatures; the authenticated event ID handles durable replay.
const paymentSignatureTolerance = 5 * time.Minute

func (a *paymentAuthenticator) validSignature(p PaymentPresentation) bool {
	var timestamp string
	var signatures [][]byte
	for _, part := range strings.Split(p.Signature, ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			return false
		}
		switch key {
		case "t":
			if timestamp != "" || value == "" {
				return false
			}
			timestamp = value
		case "v1":
			decoded, err := hex.DecodeString(value)
			if err == nil && len(decoded) == sha256.Size {
				signatures = append(signatures, decoded)
			}
		}
	}
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || seconds <= 0 || strconv.FormatInt(seconds, 10) != timestamp {
		return false
	}
	now, issued := a.now(), time.Unix(seconds, 0)
	if issued.Before(now.Add(-paymentSignatureTolerance)) || issued.After(now.Add(paymentSignatureTolerance)) {
		return false
	}
	mac := hmac.New(sha256.New, a.secret)
	if _, err := mac.Write([]byte(timestamp + ".")); err != nil {
		return false
	}
	if _, err := mac.Write(p.Body); err != nil {
		return false
	}
	expected := mac.Sum(nil)
	for _, signature := range signatures {
		if hmac.Equal(expected, signature) {
			return true
		}
	}
	return false
}

func (a *paymentAuthenticator) checkoutEvent(ctx context.Context, id string, body []byte, policy PaymentEventPolicy) (*VerifiedPaymentEvent, error) {
	var session struct {
		ID            string           `json:"id"`
		Object        string           `json:"object"`
		Mode          string           `json:"mode"`
		LiveMode      *bool            `json:"livemode"`
		PaymentStatus string           `json:"payment_status"`
		PaymentIntent paymentReference `json:"payment_intent"`
		Currency      string           `json:"currency"`
		Amount        *int64           `json:"amount_total"`
	}
	if err := json.Unmarshal(body, &session); err != nil || session.ID == "" || session.Object != "checkout.session" || session.Mode != "payment" ||
		session.LiveMode == nil || *session.LiveMode != (a.mode == PaymentLive) || session.Amount == nil || *session.Amount < 0 || !validCurrency(session.Currency) {
		return nil, invalidPaymentEvent()
	}
	if policy.RequirePaid && session.PaymentStatus != "paid" {
		return nil, nil
	}
	if policy.Kind != PaymentCheckoutPaid && session.PaymentStatus == "paid" {
		return nil, invalidPaymentEvent()
	}
	paymentID := string(session.PaymentIntent)
	if policy.Kind == PaymentCheckoutPaid && (paymentID == "" || *session.Amount == 0) {
		return nil, invalidPaymentEvent()
	}
	event := PaymentEvent{
		EventID: id, CheckoutID: session.ID, PaymentID: paymentID,
		Currency: session.Currency, AmountMinor: *session.Amount, Mode: a.mode, Kind: policy.Kind,
	}
	if policy.Kind == PaymentCheckoutPaid {
		if err := a.paidCard(ctx, &event); err != nil {
			return nil, err
		}
	}
	return &VerifiedPaymentEvent{event: event}, nil
}

// safety: the fingerprint of the card that paid pins it to the team or refuses the payment.
func (a *paymentAuthenticator) paidCard(ctx context.Context, event *PaymentEvent) error {
	intent, err := a.provider.read(ctx, "payment_intents", event.PaymentID, "payment_intent", a.mode)
	if err != nil {
		return err
	}
	if intent.LatestCharge == "" {
		return invalidPaymentEvent()
	}
	charge, err := a.provider.read(ctx, "charges", string(intent.LatestCharge), "charge", a.mode)
	if err != nil {
		return err
	}
	if string(charge.PaymentIntent) != event.PaymentID {
		return invalidPaymentEvent()
	}
	event.Fingerprint = charge.PaymentMethodDetails.Card.Fingerprint
	if secure := charge.PaymentMethodDetails.Card.ThreeDSecure; secure != nil {
		event.ThreeDS = secure.Result
	}
	return nil
}

func invalidPaymentEvent() *apierr.Error {
	return &apierr.Error{Code: apierr.BadRequest, Message: "invalid payment event"}
}

func validCurrency(currency string) bool {
	if len(currency) != 3 {
		return false
	}
	for _, c := range currency {
		if c < 'a' || c > 'z' {
			return false
		}
	}
	return true
}
