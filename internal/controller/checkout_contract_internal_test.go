package controller

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// checkoutContract is testdata/checkout_contract.json. The checkout service keeps a byte-identical copy and tests its
// real handler against it, so a change to either side's request, answer, bounds or refusal classes fails a suite.
type checkoutContract struct {
	Token      string `json:"token"`
	Deployment string `json:"deployment"`
	MinCents   int64  `json:"min_cents"`
	MaxCents   int64  `json:"max_cents"`
	Routes     map[string]struct {
		Path     string          `json:"path"`
		Request  json.RawMessage `json:"request"`
		Response json.RawMessage `json:"response"`
	} `json:"routes"`
	RefillCases []struct {
		Name       string          `json:"name"`
		HTTPStatus int             `json:"http_status"`
		Body       json.RawMessage `json:"body"`
	} `json:"refill_cases"`
	Refusals []struct {
		Name       string `json:"name"`
		HTTPStatus int    `json:"http_status"`
		Final      bool   `json:"final"`
	} `json:"refusals"`
}

func loadCheckoutContract(t *testing.T) checkoutContract {
	t.Helper()
	data, err := os.ReadFile("testdata/checkout_contract.json")
	if err != nil {
		t.Fatal(err)
	}
	var c checkoutContract
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

// client is the real checkout client holding token, talking to a fake service that, like the real one, answers 401
// to any other bearer, and otherwise requires the contract's request for route and answers status with body.
func (c checkoutContract) client(t *testing.T, token, route string, status int, body []byte) *billingCheckout {
	want := c.Routes[route]
	checkout := (&Server{}).WithBillingCheckout("https://checkout.test", token).checkout
	checkout.http.Transport = billingPageTransport(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer "+c.Token {
			return refillReply(http.StatusUnauthorized, `{"error":"checkout token rejected"}`), nil
		}
		sent, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if r.URL.Path != want.Path || !sameJSON(t, sent, want.Request) {
			t.Fatalf("%s call %s %s; want the contract request %s %s", route, r.URL.Path, sent, want.Path, want.Request)
		}
		return refillReply(status, string(body)), nil
	})
	return checkout
}

func contractFields(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func (c checkoutContract) refill(t *testing.T) store.Refill {
	var req refillRequest
	strictJSON(t, c.Routes["refill"].Request, &req)
	return store.Refill{Team: store.Team(req.Team), ID: req.RefillID, AmountCents: req.AmountCents, Customer: req.Customer}
}

// The controller sends every route exactly the contract's request with its bearer, reads the contract's answers,
// and holds the contract's payment bounds.
func TestEveryCheckoutCallMatchesTheSharedContract(t *testing.T) {
	c := loadCheckoutContract(t)
	if c.MinCents != store.MinCardPaymentCents || c.MaxCents != store.MaxCardPaymentCents {
		t.Fatalf("contract bounds %d..%d; controller %d..%d", c.MinCents, c.MaxCents, store.MinCardPaymentCents, store.MaxCardPaymentCents)
	}
	if len(c.Routes) != 5 {
		t.Fatalf("contract routes %d; want all 5", len(c.Routes))
	}
	ctx := t.Context()
	pageFor := func(route string, kind capability.BillingPageKind) (*capability.BillingPage, map[string]any) {
		in := contractFields(t, c.Routes[route].Request)
		page := &capability.BillingPage{Kind: kind, Team: in["team"].(string), ReservationID: in["key"].(string),
			ExpiresAt: time.Unix(int64(in["expires_at"].(float64)), 0)}
		if cents, ok := in["amount_cents"].(float64); ok {
			page.AmountCents = int64(cents)
		}
		if customer, ok := in["customer"].(string); ok {
			page.Customer = customer
		}
		return page, contractFields(t, c.Routes[route].Response)
	}

	page, out := pageFor("checkout", capability.BillingPageCredits)
	if err := c.client(t, c.Token, "checkout", http.StatusOK, c.Routes["checkout"].Response).openReservedPage(ctx, c.Deployment, page, ""); err != nil ||
		page.URL != out["url"] || page.ProviderID != out["id"] || page.ExpiresAt.Unix() != int64(out["expires_at"].(float64)) {
		t.Fatalf("checkout page %+v: %v", page, err)
	}
	page, out = pageFor("card_setup", capability.BillingPageCard)
	email := contractFields(t, c.Routes["card_setup"].Request)["email"].(string)
	if err := c.client(t, c.Token, "card_setup", http.StatusOK, c.Routes["card_setup"].Response).openReservedPage(ctx, c.Deployment, page, email); err != nil ||
		page.URL != out["url"] || page.ProviderID != out["id"] || page.Customer != out["customer"] {
		t.Fatalf("card setup page %+v: %v", page, err)
	}
	in, out := contractFields(t, c.Routes["portal"].Request), contractFields(t, c.Routes["portal"].Response)
	if url, err := c.client(t, c.Token, "portal", http.StatusOK, c.Routes["portal"].Response).portal(ctx, c.Deployment, in["team"].(string), in["customer"].(string)); err != nil || url != out["url"] {
		t.Fatalf("portal %q: %v", url, err)
	}
	in, out = contractFields(t, c.Routes["refund"].Request), contractFields(t, c.Routes["refund"].Response)
	if refund, status, err := c.client(t, c.Token, "refund", http.StatusOK, c.Routes["refund"].Response).refund(ctx, c.Deployment, in["payment_intent"].(string), in["reason"].(string)); err != nil ||
		refund != out["refund"] || status != out["status"] {
		t.Fatalf("refund %q %q: %v", refund, status, err)
	}

	outcomes := map[string]string{
		"charged": "", "processing": "", "declined": "insufficient_funds",
		"authentication_required": "authentication_required", "no_card": store.RefillNoCard,
	}
	if len(c.RefillCases) != len(outcomes) {
		t.Fatalf("contract refill cases %d; want %d", len(c.RefillCases), len(outcomes))
	}
	for _, tc := range c.RefillCases {
		t.Run("refill "+tc.Name, func(t *testing.T) {
			outcome, ok := outcomes[tc.Name]
			if !ok {
				t.Fatalf("contract case %q has no expected outcome", tc.Name)
			}
			if tc.Body != nil {
				strictJSON(t, tc.Body, &refillResponse{})
			}
			res, err := c.client(t, c.Token, "refill", tc.HTTPStatus, tc.Body).refill(t.Context(), c.Deployment, c.refill(t))
			got := res.Failure
			if errors.Is(err, errRefillNoCard) {
				got = store.RefillNoCard
			} else if err != nil {
				t.Fatal(err)
			}
			if got != outcome {
				t.Fatalf("outcome %q; want %q", got, outcome)
			}
		})
	}
}

// Every refusal the contract names is final or retried as it says, so a refusal no repeat can change stops instead
// of looping; a client holding another bearer meets the service's 401 and stops the same way.
func TestCheckoutRefusalsAreClassifiedAsTheSharedContractSays(t *testing.T) {
	c := loadCheckoutContract(t)
	if len(c.Refusals) == 0 {
		t.Fatal("contract names no refusals")
	}
	for _, refusal := range c.Refusals {
		t.Run(refusal.Name, func(t *testing.T) {
			token, status := c.Token, refusal.HTTPStatus
			if refusal.Name == "wrong_token" {
				token, status = "bct_another_token", http.StatusOK
			}
			_, err := c.client(t, token, "refill", status, []byte(`{"error":"refused"}`)).refill(t.Context(), c.Deployment, c.refill(t))
			if errors.Is(err, errCheckoutRefused) != refusal.Final || errors.Is(err, errCheckoutUnavailable) == refusal.Final {
				t.Fatalf("status %d classified as %v; want final %v", refusal.HTTPStatus, err, refusal.Final)
			}
		})
	}
}
