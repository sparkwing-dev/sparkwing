package capability

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

type paymentReference string

func (r *paymentReference) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*r = ""
		return nil
	}
	var id string
	if err := json.Unmarshal(data, &id); err != nil {
		var object struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(data, &object); err != nil || object.ID == "" {
			return invalidPaymentEvent()
		}
		id = object.ID
	}
	*r = paymentReference(id)
	return nil
}

type paymentObject struct {
	ID             string           `json:"id"`
	Object         string           `json:"object"`
	LiveMode       *bool            `json:"livemode"`
	Status         string           `json:"status"`
	Currency       string           `json:"currency"`
	Amount         *int64           `json:"amount"`
	AmountReceived *int64           `json:"amount_received"`
	Customer       paymentReference `json:"customer"`
	PaymentMethod  paymentReference `json:"payment_method"`
	PaymentIntent  paymentReference `json:"payment_intent"`
	Charge         paymentReference `json:"charge"`
	LatestCharge   paymentReference `json:"latest_charge"`
	Actionable     *bool            `json:"actionable"`
	Reason         string           `json:"reason"`
	Type           string           `json:"type"`
	Metadata       paymentMetadata  `json:"metadata"`
	Card           struct {
		Fingerprint string `json:"fingerprint"`
		Brand       string `json:"brand"`
		Last4       string `json:"last4"`
	} `json:"card"`
	PaymentMethodDetails struct {
		Card struct {
			Fingerprint  string `json:"fingerprint"`
			ThreeDSecure *struct {
				Result string `json:"result"`
			} `json:"three_d_secure"`
		} `json:"card"`
	} `json:"payment_method_details"`
	LastPaymentError struct {
		DeclineCode string `json:"decline_code"`
	} `json:"last_payment_error"`
}

func (a *paymentAuthenticator) providerEvent(ctx context.Context, id string, created int64, body []byte, policy PaymentEventPolicy) (*VerifiedPaymentEvent, error) {
	var object paymentObject
	if err := json.Unmarshal(body, &object); err != nil || object.ID == "" || object.LiveMode == nil || *object.LiveMode != (a.mode == PaymentLive) || created <= 0 {
		return nil, invalidPaymentEvent()
	}
	event := PaymentEvent{
		EventID: id, ObjectID: object.ID, Created: created, Mode: a.mode, Kind: policy.Kind, Status: object.Status,
		CustomerID: string(object.Customer), MethodID: string(object.PaymentMethod), PaymentID: string(object.PaymentIntent), ChargeID: string(object.Charge), Reason: object.Reason,
	}
	event.Deployment = object.Metadata.get(PaymentDeploymentMetadata)
	switch purpose := object.Metadata.get(paymentPurposeMetadata); {
	case policy.Kind == PaymentCardSetup && purpose != paymentPurposeSetup,
		(policy.Kind == PaymentCardPaid || policy.Kind == PaymentCardFailed) && purpose != paymentPurposeCharge:
		return nil, nil
	}
	var err error
	switch policy.Kind {
	case PaymentCardSetup:
		err = a.cardSetup(ctx, object, &event)
	case PaymentCardPaid, PaymentCardFailed:
		err = a.cardPayment(ctx, object, &event)
	case PaymentRefund:
		if policy.ProviderType == "refund.failed" && object.Status != "failed" {
			return nil, invalidPaymentEvent()
		}
		err = refundEvent(object, &event)
	case PaymentWarningEvent:
		if object.Object != "radar.early_fraud_warning" || object.Actionable == nil || event.PaymentID == "" || event.ChargeID == "" {
			return nil, invalidPaymentEvent()
		}
		event.Actionable = *object.Actionable
		err = a.chargeFingerprint(ctx, &event)
	case PaymentDisputeOpened, PaymentDisputeClosed:
		err = disputeEvent(object, &event)
	default:
		return nil, invalidPaymentEvent()
	}
	if err != nil {
		return nil, err
	}
	return &VerifiedPaymentEvent{event: event}, nil
}

func (a *paymentAuthenticator) cardSetup(ctx context.Context, object paymentObject, event *PaymentEvent) error {
	if object.Object != "setup_intent" || object.Status != "succeeded" || event.CustomerID == "" || event.MethodID == "" {
		return invalidPaymentEvent()
	}
	method, err := a.provider.read(ctx, "payment_methods", event.MethodID, "payment_method", a.mode)
	if err != nil {
		return err
	}
	if string(method.Customer) != event.CustomerID || method.Type != "card" || method.Card.Fingerprint == "" || method.Card.Brand == "" || len(method.Card.Last4) != 4 || strings.IndexFunc(method.Card.Last4, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
		return invalidPaymentEvent()
	}
	event.Fingerprint, event.Brand, event.Last4 = method.Card.Fingerprint, method.Card.Brand, method.Card.Last4
	return nil
}

func (a *paymentAuthenticator) cardPayment(ctx context.Context, object paymentObject, event *PaymentEvent) error {
	if object.Object != "payment_intent" || object.Amount == nil || *object.Amount <= 0 || !validCurrency(object.Currency) {
		return invalidPaymentEvent()
	}
	event.PaymentID, event.AmountMinor, event.Currency = object.ID, *object.Amount, object.Currency
	event.ChargeID, event.DeclineCode = string(object.LatestCharge), object.LastPaymentError.DeclineCode
	if event.Kind == PaymentCardFailed {
		if object.Status != "requires_payment_method" {
			return invalidPaymentEvent()
		}
		return nil
	}
	if object.Status != "succeeded" || object.AmountReceived == nil || *object.AmountReceived != *object.Amount || event.ChargeID == "" {
		return invalidPaymentEvent()
	}
	return a.chargeFingerprint(ctx, event)
}

func refundEvent(object paymentObject, event *PaymentEvent) error {
	if object.Object != "refund" || event.PaymentID == "" || event.ChargeID == "" || object.Amount == nil || *object.Amount <= 0 || !validCurrency(object.Currency) {
		return invalidPaymentEvent()
	}
	switch object.Status {
	case "pending", "requires_action", "succeeded", "failed", "canceled":
	default:
		return invalidPaymentEvent()
	}
	event.AmountMinor, event.Currency = *object.Amount, object.Currency
	return nil
}

func disputeEvent(object paymentObject, event *PaymentEvent) error {
	if object.Object != "dispute" || event.PaymentID == "" || event.ChargeID == "" || object.Amount == nil || *object.Amount <= 0 || !validCurrency(object.Currency) || object.Reason == "" {
		return invalidPaymentEvent()
	}
	if event.Kind == PaymentDisputeClosed {
		if object.Status != "won" && object.Status != "lost" && object.Status != "warning_closed" {
			return invalidPaymentEvent()
		}
	} else {
		switch object.Status {
		case "warning_needs_response", "warning_under_review", "needs_response", "under_review":
		default:
			return invalidPaymentEvent()
		}
	}
	event.AmountMinor, event.Currency = *object.Amount, object.Currency
	return nil
}

func (a *paymentAuthenticator) chargeFingerprint(ctx context.Context, event *PaymentEvent) error {
	charge, err := a.provider.read(ctx, "charges", event.ChargeID, "charge", a.mode)
	if err != nil {
		return err
	}
	if string(charge.PaymentIntent) != event.PaymentID || charge.PaymentMethodDetails.Card.Fingerprint == "" {
		return invalidPaymentEvent()
	}
	event.Fingerprint = charge.PaymentMethodDetails.Card.Fingerprint
	return nil
}

// PaymentProviderAPIVersion is the Stripe API version the controller reads provider objects at.
const PaymentProviderAPIVersion = "2026-08-26.dahlia"

type paymentProvider struct {
	key    string
	client *http.Client
}

func newPaymentProvider(key string) *paymentProvider {
	return &paymentProvider{key: key, client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (p *paymentProvider) read(ctx context.Context, collection, id, kind string, mode PaymentMode) (paymentObject, error) {
	var object paymentObject
	if id == "" || strings.IndexFunc(id, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_')
	}) >= 0 {
		return object, invalidPaymentEvent()
	}
	if p == nil {
		return object, paymentProviderUnavailable()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.stripe.com/v1/"+collection+"/"+id, nil)
	if err != nil {
		return object, paymentProviderUnavailable()
	}
	request.Header.Set("Authorization", "Bearer "+p.key)
	request.Header.Set("Stripe-Version", PaymentProviderAPIVersion)
	response, err := p.client.Do(request)
	if err != nil {
		return object, paymentProviderUnavailable()
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 || response.StatusCode != http.StatusOK {
		return object, paymentProviderUnavailable()
	}
	if json.Unmarshal(body, &object) != nil || object.ID != id || object.Object != kind || object.LiveMode == nil || *object.LiveMode != (mode == PaymentLive) {
		return paymentObject{}, invalidPaymentEvent()
	}
	return object, nil
}

func paymentProviderUnavailable() *apierr.Error {
	return &apierr.Error{Code: apierr.Unavailable, Message: "payment provider is unavailable"}
}
