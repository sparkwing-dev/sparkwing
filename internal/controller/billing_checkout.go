package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/routepolicy"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// perf: Bound the Stripe-facing checkout call so an owner click cannot hang.
const billingCheckoutTimeout = 15 * time.Second

// safety: The checkout service receives the signed-in team, never a browser-supplied team.
type billingCheckout struct {
	url   string
	token string
	http  *http.Client

	now func() time.Time
	// safety: set once the misconfiguration alert is logged, so it fires once, and cleared by the next answered call.
	misconfigured bool
	// safety: only the billing worker's goroutine touches retries, keyed by idempotency key; a restart forgets
	// them and starts the backoff again.
	retries map[string]checkoutRetry
}

// WithBillingCheckout points the controller at the checkout service that
// opens a Stripe Checkout Session for a team's credit purchase. url is the
// service's base URL and token the bearer it accepts. An empty url leaves
// purchases off, which is every self-hosted controller.
func (s *Server) WithBillingCheckout(url, token string) *Server {
	url = strings.TrimRight(strings.TrimSpace(url), "/")
	if url == "" {
		s.checkout = nil
		return s
	}
	s.checkout = &billingCheckout{
		url: url, token: token,
		http: &http.Client{Timeout: billingCheckoutTimeout},
		now:  time.Now, retries: map[string]checkoutRetry{},
	}
	return s
}

// safety: no answer, a 5xx, a 429 or a misconfigured pairing may pass on a later call; any other refusal is about the
// request itself and is final, so a refill or refund stops and reaches the owners instead of looping.
var errCheckoutUnavailable = errors.New("the checkout service is unavailable")

type checkoutMisconfiguredError struct{ cause string }

func (e checkoutMisconfiguredError) Error() string { return e.cause }

// safety: an operator slip in the pairing is retried like an outage, never final for every team's refill or refund.
var checkoutMisconfigurations = map[int]string{
	http.StatusUnauthorized:       "the checkout service refused the billing-token credential; it must equal the service's BILLING_CHECKOUT_TOKEN",
	http.StatusNotFound:           "the checkout service has no such route; --billing-url must name the checkout service",
	http.StatusMisdirectedRequest: "the checkout service serves another deployment; its BILLING_DEPLOYMENT must be this controller's deployment",
}

var errCheckoutRefused = errors.New("the checkout service refused the request")

// errRefillNoCard is the checkout service's refusal to charge a refill for a customer with no card in Stripe.
var errRefillNoCard = errors.New("the customer has no card")

// openReservedPage opens the provider page for a reservation; email, the team owner's address for Stripe's receipts,
// is sent with a card setup and never stored.
func (c *billingCheckout) openReservedPage(ctx context.Context, deployment string, page *capability.BillingPage, email string) error {
	path := "/internal/checkout"
	body := map[string]any{"team": page.Team, "key": page.ReservationID, "deployment": deployment, "expires_at": page.ExpiresAt.Unix()}
	switch page.Kind {
	case capability.BillingPageCredits:
		body["amount_cents"] = page.AmountCents
	case capability.BillingPageCard:
		path = "/internal/card-setup"
		body["setup_id"], body["customer"], body["email"] = page.ReservationID, page.Customer, email
	default:
		return routepolicy.MissingEndpointPolicy()
	}
	var out struct {
		URL       string `json:"url"`
		ID        string `json:"id"`
		Customer  string `json:"customer"`
		ExpiresAt int64  `json:"expires_at"`
	}
	if err := c.post(ctx, path, body, &out); err != nil {
		return err
	}
	if !validBillingPageURL(out.URL, stripeCheckoutHost) {
		return fmt.Errorf("%w: payment page not on %s", errCheckoutRefused, stripeCheckoutHost)
	}
	if out.ID == "" {
		return fmt.Errorf("%w: missing session id", errCheckoutRefused)
	}
	page.URL, page.ProviderID = out.URL, out.ID
	if out.Customer != "" {
		if page.Customer != "" && page.Customer != out.Customer {
			return fmt.Errorf("%w: customer changed", errCheckoutRefused)
		}
		page.Customer = out.Customer
	}
	if page.Kind != capability.BillingPageCredits && page.Customer == "" {
		return fmt.Errorf("%w: missing customer identity", errCheckoutRefused)
	}
	if out.ExpiresAt > 0 {
		expires := time.Unix(out.ExpiresAt, 0)
		if page.ExpiresAt.IsZero() || expires.Before(page.ExpiresAt) {
			page.ExpiresAt = expires
		}
	}
	return nil
}

func (c *billingCheckout) post(ctx context.Context, path string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %w", errCheckoutUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return fmt.Errorf("%w: read: %w", errCheckoutUnavailable, err)
	}
	if resp.StatusCode != http.StatusOK {
		detail := strings.TrimSpace(string(payload))
		if cause, ok := checkoutMisconfigurations[resp.StatusCode]; ok {
			return fmt.Errorf("%w: %w: %d %s", errCheckoutUnavailable, checkoutMisconfiguredError{cause}, resp.StatusCode, detail)
		}
		class := errCheckoutRefused
		if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusRequestTimeout {
			class = errCheckoutUnavailable
		}
		err := fmt.Errorf("%w: %d %s", class, resp.StatusCode, detail)
		if path == "/internal/refill" && resp.StatusCode == http.StatusConflict {
			err = errors.Join(errRefillNoCard, err)
		}
		return err
	}
	c.misconfigured = false
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("%w: decode: %w", errCheckoutRefused, err)
	}
	return nil
}

// safety: an unanswered call is retried on the next pass, then after waits doubling from billingInterval up to
// checkoutRetryMaxWait, and fails for good checkoutRetryLimit after its first failure, reaching the owners.
const (
	checkoutRetryMaxWait = 30 * time.Minute
	checkoutRetryLimit   = 6 * time.Hour
)

type checkoutRetry struct {
	first, next time.Time
	wait        time.Duration
}

func (c *billingCheckout) due(key string) bool {
	r, ok := c.retries[key]
	return !ok || !c.now().Before(r.next)
}

func (c *billingCheckout) unavailable(key string) bool {
	now := c.now()
	r, ok := c.retries[key]
	if !ok {
		r = checkoutRetry{first: now}
	}
	r.next = now.Add(r.wait)
	r.wait = min(max(r.wait*2, billingInterval), checkoutRetryMaxWait)
	c.retries[key] = r
	if now.Sub(r.first) < checkoutRetryLimit {
		return false
	}
	delete(c.retries, key)
	return true
}

func (c *billingCheckout) settled(key string) { delete(c.retries, key) }

// safety: operator alerting keys on this value; it fires once per misconfiguration, naming the likely cause.
const checkoutMisconfiguredAlert = "checkout_misconfigured"

func (s *Server) alertCheckoutMisconfigured(err error) {
	var m checkoutMisconfiguredError
	if !errors.As(err, &m) || s.checkout.misconfigured {
		return
	}
	s.checkout.misconfigured = true
	s.logger.Error("billing alert: the checkout service and this controller are not paired; refills and refunds are retried until it is fixed",
		"alert", checkoutMisconfiguredAlert, "cause", m.cause, "deployment", s.paymentDeployment, "err", err)
}

// refillRequest and refillResponse are the checkout service's POST /internal/refill contract, which creates and
// confirms one off-session payment under the refill id. testdata/checkout_contract.json pins it with the other routes;
// the checkout service tests its handler against a byte-identical copy, so the two copies change together.
type refillRequest struct {
	Team        string `json:"team"`
	Deployment  string `json:"deployment"`
	RefillID    string `json:"refill_id"`
	AmountCents int64  `json:"amount_cents"`
	Customer    string `json:"customer"`
}

// refillResponse names the payment and its Stripe status. Failure is set when Stripe refused the charge:
// authentication_required, or the decline code.
type refillResponse struct {
	PaymentIntent string `json:"payment_intent"`
	Status        string `json:"status"`
	Failure       string `json:"failure,omitempty"`
}

func (c *billingCheckout) refill(ctx context.Context, deployment string, r store.Refill) (refillResponse, error) {
	var out refillResponse
	err := c.post(ctx, "/internal/refill", refillRequest{
		Team: string(r.Team), Deployment: deployment, RefillID: r.ID, AmountCents: r.AmountCents, Customer: r.Customer,
	}, &out)
	// A payment Stripe accepted is succeeded or processing, and its signed webhook settles it.
	if err == nil && (out.PaymentIntent == "" || out.Failure == "" && out.Status != "succeeded" && out.Status != "processing") {
		err = fmt.Errorf("%w: refill answered no payment, status %q", errCheckoutRefused, out.Status)
	}
	return out, err
}

// portal opens a Stripe customer portal session, where the team views and changes its card; Sparkwing never sees it.
func (c *billingCheckout) portal(ctx context.Context, deployment, team, customer string) (string, error) {
	var out hostedPageJSON
	if err := c.post(ctx, "/internal/portal", map[string]any{"team": team, "deployment": deployment, "customer": customer}, &out); err != nil {
		return "", err
	}
	if !validBillingPageURL(out.URL, stripePortalHost) {
		return "", fmt.Errorf("%w: portal page not on %s", errCheckoutRefused, stripePortalHost)
	}
	return out.URL, nil
}

func (c *billingCheckout) refund(ctx context.Context, deployment string, team store.Team, paymentIntent, reason string) (string, string, error) {
	var out struct {
		Refund string `json:"refund"`
		Status string `json:"status"`
	}
	err := c.post(ctx, "/internal/refund", map[string]any{
		"team": string(team), "deployment": deployment, "payment_intent": paymentIntent, "key": paymentIntent, "reason": reason,
	}, &out)
	if err == nil && (out.Refund == "" || out.Status == "") {
		err = fmt.Errorf("%w: it named no refund and status", errCheckoutRefused)
	}
	return out.Refund, out.Status, err
}

// Stripe hosts its payment pages on checkout.stripe.com and its customer portal on billing.stripe.com.
const (
	stripeCheckoutHost = "checkout.stripe.com"
	stripePortalHost   = "billing.stripe.com"
)

// safety: a hosted page leaves only for Stripe's own host, so a compromised checkout service cannot send an owner to
// a look-alike payment page.
func validBillingPageURL(raw, host string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host == host && u.User == nil
}
