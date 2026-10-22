package capability

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

const paymentDeploymentMetadata = "sparkwing_deployment"

const (
	paymentPurposeMetadata = "purpose"
	paymentPurposeSetup    = "card_setup"
	paymentPurposeRefill   = "card_refill"
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

// PaymentOwnershipReason marks a recorded local object whose event names another deployment; it is retried and never
// settled.
const PaymentOwnershipReason = "payment_ownership_conflict"

type paymentMetadata map[string]any

func (m paymentMetadata) get(key string) string {
	v, _ := m[key].(string)
	return v
}

func moneyEvent(kind PaymentEventKind) bool {
	return kind != PaymentCheckoutExpired && kind != PaymentCheckoutFailed && kind != PaymentCardFailed
}

// safety: ownership is decided once, before any provider read or store write, and before purpose filtering: another
// deployment's stamp is foreign unless we recorded the object, which is an integrity fault whatever its outcome; our
// stamp or a committed parent is local, retried while a money event's parent is unrecorded; an unstamped child needs
// our stamp on its intent.
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
	checkout := strings.HasPrefix(policy.ProviderType, "checkout.session.")
	if checkout {
		route.CheckoutID = head.ID
	}
	if policy.Kind == PaymentCardPaid || policy.Kind == PaymentCardFailed {
		route.PaymentID = head.ID
	}
	if scope.Records == nil {
		return route, false, paymentProviderUnavailable()
	}
	stamp := head.Metadata.get(paymentDeploymentMetadata)
	recorded, err := scope.Records.RecordsPaymentParent(ctx, route)
	switch {
	case err != nil:
		return route, false, err
	case stamp != "" && stamp != scope.Deployment && recorded:
		return route, false, &apierr.Error{Code: apierr.IntegrityFailure, Message: "payment object " + head.ID + " is recorded here but stamped for deployment " + stamp,
			Details: map[string]any{"reason": PaymentOwnershipReason, "object": head.ID, "stamp": stamp}}
	case stamp != "" && stamp != scope.Deployment:
		return route, false, nil
	}
	purpose := head.Metadata.get(paymentPurposeMetadata)
	switch {
	case checkout && (purpose != "" || head.Mode != "payment"):
		if policy.Kind != PaymentCheckoutPaid || purpose != paymentPurposeSetup {
			return route, false, nil
		}
		route.Kind = PaymentPageCompleted
	case policy.Kind == PaymentCardSetup && purpose != paymentPurposeSetup:
		return route, false, nil
	case (policy.Kind == PaymentCardPaid || policy.Kind == PaymentCardFailed) && purpose != paymentPurposeRefill:
		return route, false, nil
	}
	switch {
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
	parent := intent.Metadata.get(paymentDeploymentMetadata)
	return route, parent != "" && parent == scope.Deployment, nil
}

func (a *paymentAuthenticator) pageEvent(id string, created int64, body []byte) (*VerifiedPaymentEvent, error) {
	var session struct {
		ID          string           `json:"id"`
		Object      string           `json:"object"`
		Mode        string           `json:"mode"`
		Status      string           `json:"status"`
		LiveMode    *bool            `json:"livemode"`
		Customer    paymentReference `json:"customer"`
		SetupIntent paymentReference `json:"setup_intent"`
	}
	if err := json.Unmarshal(body, &session); err != nil || session.Object != "checkout.session" || session.Status != "complete" ||
		session.LiveMode == nil || *session.LiveMode != (a.mode == PaymentLive) || session.Customer == "" || created <= 0 {
		return nil, invalidPaymentEvent()
	}
	intent := string(session.SetupIntent)
	if session.Mode != "setup" || intent == "" {
		return nil, invalidPaymentEvent()
	}
	return &VerifiedPaymentEvent{event: PaymentEvent{
		EventID: id, ObjectID: session.ID, Created: created, Mode: a.mode, Kind: PaymentPageCompleted,
		CustomerID: string(session.Customer), IntentID: intent,
	}}, nil
}
