package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// billingCheckoutTimeout bounds the call to the checkout service, which waits
// on Stripe, so an owner's click fails in seconds rather than hanging.
const billingCheckoutTimeout = 15 * time.Second

// billingCheckout is the hosted checkout service that opens Stripe Checkout
// Sessions. The controller names the team from the signed-in principal and
// the service never sees a browser, so no caller-typed team reaches a payment.
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

// errCheckoutRefused is a checkout service answer outside the success range.
var errCheckoutRefused = errors.New("the checkout service refused the session")

func (c *billingCheckout) open(ctx context.Context, team string, cents int64) (string, error) {
	body, err := json.Marshal(map[string]any{"team": team, "amount_cents": cents})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url+"/internal/checkout", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("checkout service: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return "", fmt.Errorf("checkout service: read: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: %d %s", errCheckoutRefused, resp.StatusCode, strings.TrimSpace(string(payload)))
	}
	var out struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(payload, &out); err != nil {
		return "", fmt.Errorf("checkout service: decode: %w", err)
	}
	// safety: the browser is sent wherever this says, so only an https page
	// is followed; a service answering anything else is misconfigured.
	if !strings.HasPrefix(out.URL, "https://") {
		return "", fmt.Errorf("%w: it answered a non-https checkout url", errCheckoutRefused)
	}
	return out.URL, nil
}
