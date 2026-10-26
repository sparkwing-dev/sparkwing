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
	ID                   string           `json:"id"`
	Object               string           `json:"object"`
	LiveMode             *bool            `json:"livemode"`
	Status               string           `json:"status"`
	Currency             string           `json:"currency"`
	Amount               *int64           `json:"amount"`
	AmountReceived       *int64           `json:"amount_received"`
	Customer             paymentReference `json:"customer"`
	PaymentMethod        paymentReference `json:"payment_method"`
	PaymentIntent        paymentReference `json:"payment_intent"`
	Charge               paymentReference `json:"charge"`
	LatestCharge         paymentReference `json:"latest_charge"`
	Actionable           *bool            `json:"actionable"`
	Reason               string           `json:"reason"`
	Metadata             paymentMetadata  `json:"metadata"`
	PaymentMethodDetails struct {
		Card struct {
			ThreeDSecure *struct {
				Result string `json:"result"`
			} `json:"three_d_secure"`
		} `json:"card"`
	} `json:"payment_method_details"`
	LastPaymentError struct {
		DeclineCode string `json:"decline_code"`
	} `json:"last_payment_error"`
}

func (a *paymentAuthenticator) providerEvent(ctx context.Context, id string, created int64, body []byte, policy paymentEventPolicy) (*VerifiedPaymentEvent, error) {
	var object paymentObject
	if err := json.Unmarshal(body, &object); err != nil || object.ID == "" || object.LiveMode == nil || *object.LiveMode != (a.mode == PaymentLive) || created <= 0 {
		return nil, invalidPaymentEvent()
	}
	event := PaymentEvent{
		EventID: id, ObjectID: object.ID, Created: created, Mode: a.mode, Kind: policy.Kind, Status: object.Status,
		CustomerID: string(object.Customer), PaymentID: string(object.PaymentIntent), ChargeID: string(object.Charge), Reason: object.Reason,
	}
	var err error
	switch policy.Kind {
	case PaymentCardSetup:
		err = cardSetup(object, &event)
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

// safety: Stripe holds the card; a setup records only that its customer now has one, never which card.
func cardSetup(object paymentObject, event *PaymentEvent) error {
	if object.Object != "setup_intent" || object.Status != "succeeded" || event.CustomerID == "" || object.PaymentMethod == "" {
		return invalidPaymentEvent()
	}
	return nil
}

func (a *paymentAuthenticator) cardPayment(ctx context.Context, object paymentObject, event *PaymentEvent) error {
	if object.Object != "payment_intent" || object.Amount == nil || *object.Amount <= 0 || !validCurrency(object.Currency) {
		return invalidPaymentEvent()
	}
	event.PaymentID, event.AmountMinor, event.Currency = object.ID, *object.Amount, object.Currency
	event.ChargeID, event.DeclineCode = string(object.LatestCharge), object.LastPaymentError.DeclineCode
	if event.Kind == PaymentCardFailed {
		if object.Status == "requires_action" {
			event.DeclineCode = "authentication_required"
			return nil
		}
		if object.Status != "requires_payment_method" {
			return invalidPaymentEvent()
		}
		return nil
	}
	if object.Status != "succeeded" || object.AmountReceived == nil || *object.AmountReceived != *object.Amount || event.ChargeID == "" {
		return invalidPaymentEvent()
	}
	return a.chargeFacts(ctx, event)
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

func (a *paymentAuthenticator) chargeFacts(ctx context.Context, event *PaymentEvent) error {
	charge, err := a.provider.read(ctx, "charges", event.ChargeID, "charge", a.mode)
	if err != nil {
		return err
	}
	if string(charge.PaymentIntent) != event.PaymentID {
		return invalidPaymentEvent()
	}
	event.setPaidCardFacts(charge)
	return nil
}

func (event *PaymentEvent) setPaidCardFacts(charge paymentObject) {
	if secure := charge.PaymentMethodDetails.Card.ThreeDSecure; secure != nil {
		event.ThreeDS = secure.Result
		event.LiabilityShift = secure.Result == "authenticated"
	}
}

// paymentProviderAPIVersion is the Stripe API version the controller reads provider objects at.
const paymentProviderAPIVersion = "2026-08-26.dahlia"

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
	request.Header.Set("Stripe-Version", paymentProviderAPIVersion)
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
