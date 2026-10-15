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
	return v.event
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
	return &paymentAuthenticator{mode: config.Mode, secret: []byte(secret), now: time.Now}, nil
}

func readPaymentSecret(path string) (string, error) {
	f, err := fssecure.OpenPrivateConfig(path)
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
	mode   PaymentMode
	secret []byte
	now    func() time.Time
}

func (a *paymentAuthenticator) VerifyPaymentEvent(ctx context.Context, presentation PaymentPresentation) (*VerifiedPaymentEvent, error) {
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
		if policy.ProviderType == event.Type {
			return a.checkoutEvent(event.ID, event.Data.Object, policy)
		}
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

func (a *paymentAuthenticator) checkoutEvent(id string, body []byte, policy PaymentEventPolicy) (*VerifiedPaymentEvent, error) {
	var session struct {
		ID            string          `json:"id"`
		Object        string          `json:"object"`
		Mode          string          `json:"mode"`
		LiveMode      *bool           `json:"livemode"`
		PaymentStatus string          `json:"payment_status"`
		PaymentIntent json.RawMessage `json:"payment_intent"`
		Currency      string          `json:"currency"`
		Amount        *int64          `json:"amount_total"`
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
	var paymentID string
	if string(session.PaymentIntent) != "null" && len(session.PaymentIntent) > 0 {
		if err := json.Unmarshal(session.PaymentIntent, &paymentID); err != nil {
			var expanded struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(session.PaymentIntent, &expanded); err != nil {
				return nil, invalidPaymentEvent()
			}
			paymentID = expanded.ID
		}
	}
	if policy.Kind == PaymentCheckoutPaid && (paymentID == "" || *session.Amount == 0) {
		return nil, invalidPaymentEvent()
	}
	return &VerifiedPaymentEvent{event: PaymentEvent{
		EventID: id, CheckoutID: session.ID, PaymentID: paymentID,
		Currency: session.Currency, AmountMinor: *session.Amount, Mode: a.mode, Kind: policy.Kind,
	}}, nil
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
