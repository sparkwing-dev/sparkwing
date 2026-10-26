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
	}
	return s
}

var errCheckoutRefused = errors.New("the checkout service refused the session")

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
	if !validBillingPageURL(out.URL) {
		return fmt.Errorf("%w: non-https payment page", errCheckoutRefused)
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
		return fmt.Errorf("checkout service: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return fmt.Errorf("checkout service: read: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: %d %s", errCheckoutRefused, resp.StatusCode, strings.TrimSpace(string(payload)))
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("checkout service: decode: %w", err)
	}
	return nil
}

type refillResult struct {
	PaymentIntent string `json:"payment_intent"`
	Status        string `json:"status"`
}

func (c *billingCheckout) refill(ctx context.Context, deployment string, r store.Refill, action string) (refillResult, error) {
	var out refillResult
	path := "/internal/refill"
	if action != "" {
		path += "/" + action
	}
	err := c.post(ctx, path, map[string]any{
		"team": string(r.Team), "deployment": deployment, "refill_id": r.ID, "amount_cents": r.AmountCents,
		"customer": r.Customer, "payment_intent": r.PaymentIntent,
	}, &out)
	if err == nil && (out.PaymentIntent == "" || out.Status == "" || r.PaymentIntent != "" && out.PaymentIntent != r.PaymentIntent) {
		err = fmt.Errorf("%w: missing or mismatched payment or status", errCheckoutRefused)
	}
	return out, err
}

// portal opens a Stripe customer portal session, where the team views and changes its card; Sparkwing never sees it.
func (c *billingCheckout) portal(ctx context.Context, deployment, team, customer string) (string, error) {
	var out hostedPageJSON
	if err := c.post(ctx, "/internal/portal", map[string]any{"team": team, "deployment": deployment, "customer": customer}, &out); err != nil {
		return "", err
	}
	if !validBillingPageURL(out.URL) {
		return "", fmt.Errorf("%w: non-https portal page", errCheckoutRefused)
	}
	return out.URL, nil
}

func (c *billingCheckout) refund(ctx context.Context, paymentIntent, key, reason string) (string, string, error) {
	var out struct {
		Refund string `json:"refund"`
		Status string `json:"status"`
	}
	err := c.post(ctx, "/internal/refund", map[string]any{
		"payment_intent": paymentIntent, "key": key, "reason": reason,
	}, &out)
	if err == nil && (out.Refund == "" || out.Status == "") {
		err = fmt.Errorf("%w: it named no refund and status", errCheckoutRefused)
	}
	return out.Refund, out.Status, err
}

func validBillingPageURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil
}
