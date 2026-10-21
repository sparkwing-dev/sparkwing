package capability

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

// PaymentPuller reconciles provider-owned current facts, including objects older than
// Stripe's event retention. Every accepted object passes the webhook ownership checks.
type PaymentPuller interface {
	PullPayments(context.Context, PaymentScope, func(VerifiedPaymentEvent) error) error
}

type paymentSetupRecords interface {
	PaymentSetupCompletion(context.Context, PaymentMode, string) (int64, error)
}

// PullPayments scans complete provider collections. Repeating a sweep is safe: proof
// identities are stable for a given provider outcome and settlement also deduplicates money.
func (a *paymentAuthenticator) PullPayments(ctx context.Context, scope PaymentScope, settle func(VerifiedPaymentEvent) error) error {
	completed := map[string]int64{}
	var incomplete error
	for _, collection := range []string{"checkout/sessions", "events", "setup_intents", "payment_intents", "refunds", "disputes", "radar/early_fraud_warnings"} {
		cursor := ""
		for {
			query := url.Values{"limit": {"100"}}
			if collection == "events" {
				query.Set("type", "setup_intent.succeeded")
			}
			if cursor != "" {
				query.Set("starting_after", cursor)
			}
			body, err := a.provider.fetch(ctx, collection+"?"+query.Encode(), 8<<20)
			if err != nil {
				return err
			}
			var page struct {
				Object  string            `json:"object"`
				HasMore *bool             `json:"has_more"`
				Data    []json.RawMessage `json:"data"`
			}
			if json.Unmarshal(body, &page) != nil || page.Object != "list" || page.HasMore == nil || page.Data == nil || *page.HasMore && len(page.Data) == 0 {
				return invalidPaymentEvent()
			}
			for _, raw := range page.Data {
				var head struct {
					ID            string          `json:"id"`
					Object        string          `json:"object"`
					Created       int64           `json:"created"`
					LiveMode      *bool           `json:"livemode"`
					Status        string          `json:"status"`
					PaymentStatus string          `json:"payment_status"`
					LastError     json.RawMessage `json:"last_payment_error"`
					Type          string          `json:"type"`
					Data          struct {
						Object struct {
							ID string `json:"id"`
						} `json:"object"`
					} `json:"data"`
				}
				if json.Unmarshal(raw, &head) != nil || head.ID == "" || head.LiveMode == nil || *head.LiveMode != (a.mode == PaymentLive) {
					return invalidPaymentEvent()
				}
				policy := PaymentEventPolicy{}
				switch collection {
				case "events":
					if head.Object != "event" || head.Type != "setup_intent.succeeded" || head.Created <= 0 || head.Data.Object.ID == "" {
						return invalidPaymentEvent()
					}
					completed[head.Data.Object.ID] = max(completed[head.Data.Object.ID], head.Created)
				case "checkout/sessions":
					if head.Object != "checkout.session" {
						return invalidPaymentEvent()
					}
					switch {
					case head.PaymentStatus == "paid" || head.Status == "complete":
						policy = paymentPullPolicy("checkout.session.completed")
					case head.Status == "expired":
						policy = paymentPullPolicy("checkout.session.expired")
					}
				case "setup_intents":
					if head.Object != "setup_intent" {
						return invalidPaymentEvent()
					}
					if head.Status == "succeeded" {
						policy = paymentPullPolicy("setup_intent.succeeded")
					}
				case "payment_intents":
					if head.Object != "payment_intent" {
						return invalidPaymentEvent()
					}
					if head.Status == "succeeded" {
						policy = paymentPullPolicy("payment_intent.succeeded")
					} else if head.Status == "requires_payment_method" && len(head.LastError) > 0 && string(head.LastError) != "null" {
						policy = paymentPullPolicy("payment_intent.payment_failed")
					}
				case "refunds":
					if head.Object != "refund" {
						return invalidPaymentEvent()
					}
					policy = paymentPullPolicy("refund.updated")
				case "disputes":
					if head.Object != "dispute" {
						return invalidPaymentEvent()
					}
					policy = paymentPullPolicy("charge.dispute.created")
					if head.Status == "won" || head.Status == "lost" || head.Status == "warning_closed" {
						policy = paymentPullPolicy("charge.dispute.closed")
					}
				case "radar/early_fraud_warnings":
					if head.Object != "radar.early_fraud_warning" {
						return invalidPaymentEvent()
					}
					policy = paymentPullPolicy("radar.early_fraud_warning.updated")
				}
				if policy.ProviderType != "" {
					route, owned, err := a.classify(ctx, policy, raw, scope)
					if err != nil {
						return err
					}
					if owned {
						var proof *VerifiedPaymentEvent
						switch route.Kind {
						case PaymentPageCompleted:
							proof, err = a.pageEvent("pull", head.Created, raw)
						case PaymentCheckoutPaid, PaymentCheckoutExpired, PaymentCheckoutFailed:
							proof, err = a.checkoutEvent(ctx, "pull", raw, policy)
						default:
							created := head.Created
							if route.Kind == PaymentCardSetup {
								created = completed[head.ID]
								if created == 0 {
									if records, ok := scope.Records.(paymentSetupRecords); ok {
										created, err = records.PaymentSetupCompletion(ctx, a.mode, head.ID)
									}
									if err != nil {
										return err
									}
								}
								if created == 0 {
									incomplete = errors.Join(incomplete, &apierr.Error{Code: apierr.Conflict, Message: "card setup has no provider completion evidence; repair card selection before acknowledging restore"})
									break
								}
							}
							proof, err = a.providerEvent(ctx, "pull", created, raw, policy)
						}
						if err != nil {
							return err
						}
						if proof != nil {
							identity, err := json.Marshal(struct {
								Event    PaymentEvent
								Detached bool
							}{proof.event.Identity(), proof.event.MethodDetached})
							if err != nil {
								return err
							}
							proof.event.EventID = fmt.Sprintf("pull_%x", sha256.Sum256(identity))
							if err := settle(*proof); err != nil {
								return err
							}
						}
					}
				}
				if head.ID == cursor {
					return invalidPaymentEvent()
				}
				cursor = head.ID
			}
			if !*page.HasMore {
				break
			}
		}
	}
	return incomplete
}

func paymentPullPolicy(providerType string) PaymentEventPolicy {
	for _, policy := range paymentEventPolicies {
		if policy.ProviderType == providerType {
			return policy
		}
	}
	panic("missing payment policy: " + providerType)
}
