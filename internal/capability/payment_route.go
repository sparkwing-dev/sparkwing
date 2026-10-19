package capability

import (
	"context"
	"encoding/json"
	"strings"
)

// PaymentDeploymentMetadata is the provider metadata key naming the deployment that created a page or payment.
const PaymentDeploymentMetadata = "sparkwing_deployment"

const (
	paymentPurposeMetadata = "purpose"
	paymentPurposeSetup    = "card_setup"
	paymentPurposeCharge   = "card_charge"
)

// PaymentRoute names the provider objects a signed event concerns, read from the event before any provider call.
type PaymentRoute struct {
	Mode       PaymentMode
	Kind       PaymentEventKind
	ObjectID   string
	CheckoutID string
	PaymentID  string
}

// PaymentRecords answers whether this deployment committed the parent object an event concerns.
type PaymentRecords interface {
	RecordsPaymentParent(context.Context, PaymentRoute) (bool, error)
}

// PaymentScope is the deployment a verifier settles for and the records that resolve unstamped events.
type PaymentScope struct {
	Deployment string
	Records    PaymentRecords
}

// PaymentOwnershipConflict is a recorded local object whose event names another deployment; it is retried and never settled.
type PaymentOwnershipConflict struct {
	EventObject string
	Stamp       string
}

func (e *PaymentOwnershipConflict) Error() string {
	return "payment object " + e.EventObject + " is recorded here but stamped for deployment " + e.Stamp
}

type paymentMetadata map[string]any

func (m paymentMetadata) get(key string) string {
	v, _ := m[key].(string)
	return v
}

func moneyEvent(kind PaymentEventKind) bool {
	return kind != PaymentCheckoutExpired && kind != PaymentCheckoutFailed && kind != PaymentCardFailed
}

// safety: ownership is decided once, before any provider read or store write. Another deployment's stamp
// is foreign unless we recorded the object, which is an integrity fault; our stamp or a committed parent is
// local, retried while a money event's parent is unrecorded; an unstamped child needs our stamp on its intent.
func (a *paymentAuthenticator) classify(ctx context.Context, policy PaymentEventPolicy, body []byte, scope PaymentScope) (PaymentRoute, bool, error) {
	var head struct {
		ID            string           `json:"id"`
		Object        string           `json:"object"`
		Mode          string           `json:"mode"`
		PaymentIntent paymentReference `json:"payment_intent"`
		Metadata      paymentMetadata  `json:"metadata"`
	}
	if err := json.Unmarshal(body, &head); err != nil || head.ID == "" {
		return PaymentRoute{}, false, invalidPaymentEvent()
	}
	route := PaymentRoute{Mode: a.mode, Kind: policy.Kind, ObjectID: head.ID, PaymentID: string(head.PaymentIntent)}
	purpose := head.Metadata.get(paymentPurposeMetadata)
	switch {
	case strings.HasPrefix(policy.ProviderType, "checkout.session."):
		route.CheckoutID = head.ID
		if purpose != "" || head.Mode != "payment" {
			if policy.Kind != PaymentCheckoutPaid || purpose != paymentPurposeSetup && purpose != paymentPurposeCharge {
				return route, false, nil
			}
			route.Kind = PaymentPageCompleted
		}
	case policy.Kind == PaymentCardSetup:
		if purpose != paymentPurposeSetup {
			return route, false, nil
		}
	case policy.Kind == PaymentCardPaid || policy.Kind == PaymentCardFailed:
		if purpose != paymentPurposeCharge {
			return route, false, nil
		}
		route.PaymentID = head.ID
	}
	if scope.Records == nil {
		return route, false, paymentProviderUnavailable()
	}
	stamp := head.Metadata.get(PaymentDeploymentMetadata)
	recorded, err := scope.Records.RecordsPaymentParent(ctx, route)
	switch {
	case err != nil:
		return route, false, err
	case stamp != "" && stamp != scope.Deployment && recorded:
		return route, false, &PaymentOwnershipConflict{EventObject: head.ID, Stamp: stamp}
	case stamp != "" && stamp != scope.Deployment:
		return route, false, nil
	case recorded:
		return route, true, nil
	case !moneyEvent(route.Kind):
		return route, false, nil
	case stamp != "":
		return route, true, nil
	case route.PaymentID == "" || route.PaymentID == head.ID:
		return route, false, nil
	}
	intent, err := a.provider.read(ctx, "payment_intents", route.PaymentID, "payment_intent", a.mode)
	if err != nil {
		return route, false, err
	}
	parent := intent.Metadata.get(PaymentDeploymentMetadata)
	return route, parent != "" && parent == scope.Deployment, nil
}

func (a *paymentAuthenticator) pageEvent(id string, created int64, body []byte) (*VerifiedPaymentEvent, error) {
	var session struct {
		ID            string           `json:"id"`
		Object        string           `json:"object"`
		Mode          string           `json:"mode"`
		Status        string           `json:"status"`
		PaymentStatus string           `json:"payment_status"`
		LiveMode      *bool            `json:"livemode"`
		Customer      paymentReference `json:"customer"`
		PaymentIntent paymentReference `json:"payment_intent"`
		SetupIntent   paymentReference `json:"setup_intent"`
	}
	if err := json.Unmarshal(body, &session); err != nil || session.Object != "checkout.session" || session.Status != "complete" ||
		session.LiveMode == nil || *session.LiveMode != (a.mode == PaymentLive) || session.Customer == "" || created <= 0 {
		return nil, invalidPaymentEvent()
	}
	intent := string(session.SetupIntent)
	if session.Mode == "payment" {
		if session.PaymentStatus != "paid" {
			return nil, nil
		}
		intent = string(session.PaymentIntent)
	}
	if intent == "" {
		return nil, invalidPaymentEvent()
	}
	return &VerifiedPaymentEvent{event: PaymentEvent{
		EventID: id, ObjectID: session.ID, Created: created, Mode: a.mode, Kind: PaymentPageCompleted,
		CustomerID: string(session.Customer), IntentID: intent,
	}}, nil
}
