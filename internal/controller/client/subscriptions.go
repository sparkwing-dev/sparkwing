package client

import (
	"context"
	"net/url"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

var _ controllerwire.SubscriptionManager = (*engineClient)(nil)

// NewSubscriptionManager shares authenticated transport and retry policy with engines.
func NewSubscriptionManager(options EngineOptions) (controllerwire.SubscriptionManager, error) {
	return newEngineClient(options)
}

func (c *engineClient) ListSubscriptions(ctx context.Context) ([]controllerwire.Subscription, error) {
	var result controllerwire.Subscriptions
	if err := c.requestList(ctx, "/api/v1/subscriptions", "/api/v1/subscriptions", &result, "subscriptions"); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, sub := range result.Subscriptions {
		if !validSubscriptionID(sub.ID) || seen[sub.ID] {
			return nil, subscriptionProtocolError("controller returned missing or repeated subscriptions")
		}
		seen[sub.ID] = true
	}
	return result.Subscriptions, nil
}

func (c *engineClient) GetSubscription(ctx context.Context, id string) (controllerwire.Subscription, error) {
	var result controllerwire.Subscription
	if !validSubscriptionID(id) {
		return result, subscriptionSelectorError()
	}
	err := c.request(ctx, "GET", "/api/v1/subscriptions/{subscription}", "/api/v1/subscriptions/"+url.PathEscape(id), "", nil, &result)
	if err == nil && result.ID != id {
		err = subscriptionProtocolError("controller returned another subscription")
	}
	return result, err
}

func (c *engineClient) CreateSubscription(ctx context.Context, input controllerwire.SubscriptionWrite) (controllerwire.SubscriptionSecret, error) {
	var result controllerwire.SubscriptionSecret
	if strings.TrimSpace(input.URL) == "" || !validSelector(input.IdempotencyKey) {
		return result, &apierr.Error{Code: apierr.BadRequest, Message: "a subscription requires a URL and an idempotency key"}
	}
	err := c.request(ctx, "POST", "/api/v1/subscriptions", "/api/v1/subscriptions", input.IdempotencyKey, input, &result)
	if err == nil && (!validSubscriptionID(result.Subscription.ID) || result.Subscription.URL != input.URL || !strings.HasPrefix(result.Secret, "whsec_")) {
		err = subscriptionProtocolError("controller returned a conflicting subscription")
	}
	return result, err
}

func (c *engineClient) DeleteSubscription(ctx context.Context, id string) (controllerwire.SubscriptionDeletion, error) {
	var result controllerwire.SubscriptionDeletion
	if !validSubscriptionID(id) {
		return result, subscriptionSelectorError()
	}
	err := c.request(ctx, "DELETE", "/api/v1/subscriptions/{subscription}", "/api/v1/subscriptions/"+url.PathEscape(id), "", nil, &result)
	if err == nil && result.ID != id {
		err = subscriptionProtocolError("controller returned a conflicting subscription deletion")
	}
	return result, err
}

func (c *engineClient) RotateSubscriptionSecret(ctx context.Context, id string) (controllerwire.SubscriptionSecret, error) {
	var result controllerwire.SubscriptionSecret
	if !validSubscriptionID(id) {
		return result, subscriptionSelectorError()
	}
	err := c.request(ctx, "POST", "/api/v1/subscriptions/{subscription}/rotate-secret", "/api/v1/subscriptions/"+url.PathEscape(id)+"/rotate-secret", "", nil, &result)
	if err == nil && (result.Subscription.ID != id || !strings.HasPrefix(result.Secret, "whsec_")) {
		err = subscriptionProtocolError("controller returned a conflicting secret rotation")
	}
	return result, err
}

func validSubscriptionID(id string) bool {
	return strings.HasPrefix(id, "sub_") && validSelector(id)
}

func subscriptionSelectorError() error {
	return &apierr.Error{Code: apierr.BadRequest, Message: "a subscription ID starts with sub_, as subscriptions list shows it"}
}

func subscriptionProtocolError(message string) error {
	return &apierr.Error{Code: apierr.ProtocolError, Message: message}
}
