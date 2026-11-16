package controllerwire

import "context"

// Subscription is a team's run event subscription as the API shows it: never
// its signing secret.
type Subscription struct {
	ID           string   `json:"id"`
	URL          string   `json:"url"`
	EventTypes   []string `json:"event_types"`
	Statuses     []string `json:"statuses"`
	Pipelines    []string `json:"pipelines"`
	Repositories []string `json:"repositories"`
	Description  string   `json:"description,omitempty"`
	CreatedBy    string   `json:"created_by"`
	CreatedAt    int64    `json:"created_at"`
	UpdatedAt    int64    `json:"updated_at"`
}

// Subscriptions is one page of a team's subscriptions.
type Subscriptions struct {
	Subscriptions []Subscription `json:"subscriptions"`
}

// SubscriptionWrite creates a subscription. Omitted event types mean
// run.finished when statuses are given and both events otherwise; other
// omitted filters match every value.
// IdempotencyKey travels only in the header.
type SubscriptionWrite struct {
	URL            string   `json:"url"`
	EventTypes     []string `json:"event_types,omitempty"`
	Statuses       []string `json:"statuses,omitempty"`
	Pipelines      []string `json:"pipelines,omitempty"`
	Repositories   []string `json:"repositories,omitempty"`
	Description    string   `json:"description,omitempty"`
	IdempotencyKey string   `json:"-"`
}

// SubscriptionSecret answers create and rotate-secret: the only responses that
// carry the signing secret.
type SubscriptionSecret struct {
	Subscription Subscription `json:"subscription"`
	// Secret is the Standard Webhooks signing secret, "whsec_" plus base64.
	Secret   string `json:"secret"`
	Replayed bool   `json:"replayed,omitempty"`
}

// SubscriptionDeletion reports whether a delete removed a subscription.
type SubscriptionDeletion struct {
	ID      string `json:"id"`
	Deleted bool   `json:"deleted"`
}

// SubscriptionManager uses the team-admin subscription routes.
type SubscriptionManager interface {
	ListSubscriptions(context.Context) ([]Subscription, error)
	GetSubscription(context.Context, string) (Subscription, error)
	CreateSubscription(context.Context, SubscriptionWrite) (SubscriptionSecret, error)
	DeleteSubscription(context.Context, string) (SubscriptionDeletion, error)
	RotateSubscriptionSecret(context.Context, string) (SubscriptionSecret, error)
}
