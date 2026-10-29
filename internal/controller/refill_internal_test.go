package controller

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability/paymenttest"
	"github.com/sparkwing-dev/sparkwing/internal/deployment/deploymenttest"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func strictJSON(t *testing.T, data []byte, into any) {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		t.Fatalf("%s: %v", data, err)
	}
}

func sameJSON(t *testing.T, a, b []byte) bool {
	t.Helper()
	var x, y any
	if err := json.Unmarshal(a, &x); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &y); err != nil {
		t.Fatal(err)
	}
	return reflect.DeepEqual(x, y)
}

func refillReply(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}
}

func refillOwner(t *testing.T, e *engineFixture) {
	t.Helper()
	engineSQL(t, e.Store, `INSERT INTO accounts(id,email,email_verified,created_at,updated_at) VALUES ('owner','owner@example.test',1,1,1)`)
	engineSQL(t, e.Store, `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('a','owner','owner',1)`)
}

// One checkout call charges a refill; the reply moves no money, and the signed webhook, matched by the refill id in
// the payment's metadata, grants the credit once.
func TestRefillPassChargesInOneCallAndOnlyTheWebhookGrants(t *testing.T) {
	e, tenant := refillFixture(t)
	worker := NewDeployment(e.Store, deploymenttest.Team(t), nil).WithBillingCheckout("https://checkout.test", "credential")
	var bodies []map[string]any
	worker.checkout.http.Transport = billingPageTransport(func(r *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if r.URL.Path != "/internal/refill" || len(body) != 5 || body["team"] != "a" || body["amount_cents"] != float64(2000) ||
			body["customer"] != "cus_refill" || body["refill_id"] == "" {
			t.Fatalf("refill call %s %v", r.URL.Path, body)
		}
		bodies = append(bodies, body)
		return refillReply(http.StatusOK, `{"payment_intent":"pi_refill","status":"succeeded"}`), nil
	})
	worker.refillPass(t.Context())
	if len(bodies) != 1 {
		t.Fatalf("checkout calls %v; want one", bodies)
	}
	if micro, err := tenant.CreditBalanceMicro(t.Context()); err != nil || micro != 0 {
		t.Fatalf("the checkout reply moved money: %d, %v", micro, err)
	}
	standing, err := tenant.SpendStanding(t.Context())
	if err != nil || !standing.Refill.Open || standing.Refill.DayCount != 1 {
		t.Fatalf("refill state %+v: %v; want one open refill", standing.Refill, err)
	}
	provider := paymenttest.Provider{}.SettledCheckout("pi_refill", false, "")
	_, deliver := billingModelProviderDelivery(t, e, provider, func(s *Server) *Server { return s })
	for range 2 {
		if w := deliver("evt_refill", "payment_intent.succeeded", refillIntent("pi_refill", bodies[0]["refill_id"].(string), "succeeded")); w.Code != http.StatusNoContent {
			t.Fatalf("refill webhook = %d %s", w.Code, w.Body)
		}
	}
	if micro, err := tenant.CreditBalanceMicro(t.Context()); err != nil || micro != 2000*store.MicroCreditsPerCent {
		t.Fatalf("balance after the refill = %d, %v; want $20 granted once", micro, err)
	}
	standing, err = tenant.SpendStanding(t.Context())
	if err != nil || standing.Refill.Open || standing.Refill.DayCount != 1 || standing.Refill.WeekCents != 2000 {
		t.Fatalf("refill state %+v: %v; want it settled and counted", standing.Refill, err)
	}
	worker.refillPass(t.Context())
	if len(bodies) != 1 {
		t.Fatalf("a settled refill was charged again: %v", bodies)
	}
}

// A call whose answer is lost is sent again with the same body, so the refill id keeps Stripe to one charge, but only
// while the team is still eligible; a refill withdrawn in between makes no call, and the payment the lost call made
// is still credited.
func TestALostRefillAnswerIsRetriedUnderTheSameKey(t *testing.T) {
	for _, withdrawn := range []bool{false, true} {
		t.Run(map[bool]string{false: "eligible", true: "turned off"}[withdrawn], func(t *testing.T) {
			e, tenant := refillFixture(t)
			worker := NewDeployment(e.Store, deploymenttest.Team(t), nil).WithBillingCheckout("https://checkout.test", "credential")
			var bodies []string
			worker.checkout.http.Transport = billingPageTransport(func(r *http.Request) (*http.Response, error) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatal(err)
				}
				bodies = append(bodies, string(body))
				if len(bodies) == 1 {
					return nil, errors.New("connection reset after Stripe charged")
				}
				return refillReply(http.StatusOK, `{"payment_intent":"pi_retried","status":"succeeded"}`), nil
			})
			worker.refillPass(t.Context())
			if withdrawn {
				engineSQL(t, e.Store, `UPDATE teams SET refill_enabled=0 WHERE name='a'`)
			}
			worker.refillPass(t.Context())
			if withdrawn && len(bodies) != 1 || !withdrawn && (len(bodies) != 2 || bodies[0] != bodies[1]) {
				t.Fatalf("calls %q; want the same request again only while eligible", bodies)
			}
			var sent refillRequest
			strictJSON(t, []byte(bodies[0]), &sent)
			provider := paymenttest.Provider{}.SettledCheckout("pi_retried", false, "")
			_, deliver := billingModelProviderDelivery(t, e, provider, func(s *Server) *Server { return s })
			if w := deliver("evt_retried", "payment_intent.succeeded", refillIntent("pi_retried", sent.RefillID, "succeeded")); w.Code != http.StatusNoContent {
				t.Fatalf("refill webhook = %d %s", w.Code, w.Body)
			}
			if micro, err := tenant.CreditBalanceMicro(t.Context()); err != nil || micro != 2000*store.MicroCreditsPerCent {
				t.Fatalf("balance after the retried refill = %d, %v", micro, err)
			}
			calls := len(bodies)
			worker.refillPass(t.Context())
			if len(bodies) != calls {
				t.Fatalf("a settled refill was charged again: %q", bodies)
			}
		})
	}
}

// A refill the checkout service or a signed webhook reports as refused closes with its failure code, stops refills
// until Add funds, alerts the operator and mails the owners once.
func TestARefusedRefillStopsRefillsAndNotifiesTheOwners(t *testing.T) {
	for _, tc := range []struct {
		name, reply, webhook, failure string
		status                        int
	}{
		{"no card", `{"error":"the customer has no card"}`, "", store.RefillNoCard, http.StatusConflict},
		{"declined", `{"payment_intent":"pi_refused","status":"requires_payment_method","failure":"insufficient_funds"}`, "", "insufficient_funds", http.StatusOK},
		{"authentication required", `{"payment_intent":"pi_refused","status":"requires_payment_method","failure":"authentication_required"}`, "", "authentication_required", http.StatusOK},
		{"signed decline", `{"payment_intent":"pi_refused","status":"processing"}`, "payment_intent.payment_failed", "insufficient_funds", http.StatusOK},
		{"signed authentication", `{"payment_intent":"pi_refused","status":"processing"}`, "payment_intent.requires_action", "authentication_required", http.StatusOK},
		{"wrong deployment", `{"error":"deployment must name this service's deployment"}`, "", refillCheckoutRefused, http.StatusBadRequest},
		{"wrong token", `{"error":"checkout token rejected"}`, "", refillCheckoutRefused, http.StatusUnauthorized},
		{"foreign customer", `{"error":"the customer belongs to another deployment"}`, "", refillCheckoutRefused, http.StatusForbidden},
		{"stripe refused the request", `{"error":"stripe refused the payment"}`, "", refillCheckoutRefused, http.StatusUnprocessableEntity},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, tenant := refillFixture(t)
			refillOwner(t, e)
			var logs bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logs, nil))
			mail := &refillMail{}
			worker := NewDeployment(e.Store, deploymenttest.Team(t), nil).WithBillingCheckout("https://checkout.test", "credential").WithMailer(mail)
			worker.logger = logger
			var sent []refillRequest
			worker.checkout.http.Transport = billingPageTransport(func(r *http.Request) (*http.Response, error) {
				var body refillRequest
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				sent = append(sent, body)
				return refillReply(tc.status, tc.reply), nil
			})
			worker.refillPass(t.Context())
			if tc.webhook != "" {
				_, deliver := billingModelProviderDelivery(t, e, paymenttest.Provider{}, func(s *Server) *Server {
					s.logger = logger
					return s.WithMailer(mail)
				})
				status := "requires_action"
				if tc.webhook == "payment_intent.payment_failed" {
					status = "failed"
				}
				if w := deliver("evt_refused", tc.webhook, refillIntent("pi_refused", sent[0].RefillID, status)); w.Code != http.StatusNoContent {
					t.Fatalf("webhook = %d %s", w.Code, w.Body)
				}
			}
			for range 2 {
				worker.refillPass(t.Context())
			}
			if len(sent) != 1 {
				t.Fatalf("checkout calls %d; want the refill stopped after its outcome", len(sent))
			}
			standing, err := tenant.SpendStanding(t.Context())
			if err != nil || standing.Refill.Open || standing.Refill.FailedAt.IsZero() || standing.Refill.Failure != tc.failure {
				t.Fatalf("refill state %+v: %v; want it closed as %s", standing.Refill, err, tc.failure)
			}
			if micro, err := tenant.CreditBalanceMicro(t.Context()); err != nil || micro != 0 {
				t.Fatalf("a refused refill granted %d: %v", micro, err)
			}
			if !strings.Contains(logs.String(), `"alert":"refill_failed"`) || !strings.Contains(logs.String(), tc.failure) {
				t.Fatalf("a refused refill raised no alert: %s", logs.String())
			}
			if len(mail.sent) != 1 || mail.sent[0].To != "owner@example.test" || !strings.Contains(mail.sent[0].Text, tc.failure) {
				t.Fatalf("owner mail %+v; want one refill failure notice", mail.sent)
			}
		})
	}
}

// A refill the checkout service cannot answer is retried under its key with a growing wait, and one it still cannot
// answer checkoutRetryLimit after the first failure stops refills and reaches the owners like a refusal.
func TestAnUnansweredRefillBacksOffThenFailsForGood(t *testing.T) {
	e, tenant := refillFixture(t)
	refillOwner(t, e)
	var logs bytes.Buffer
	mail := &refillMail{}
	worker := NewDeployment(e.Store, deploymenttest.Team(t), nil).WithBillingCheckout("https://checkout.test", "credential").WithMailer(mail)
	worker.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	now := time.Unix(1_800_000_000, 0)
	worker.checkout.now = func() time.Time { return now }
	calls := 0
	worker.checkout.http.Transport = billingPageTransport(func(*http.Request) (*http.Response, error) {
		calls++
		if calls%2 == 0 {
			return nil, errors.New("connection refused")
		}
		return refillReply(http.StatusBadGateway, `{"error":"stripe did not answer the payment"}`), nil
	})
	pass := func(after time.Duration, want int) {
		t.Helper()
		now = now.Add(after)
		worker.refillPass(t.Context())
		if calls != want {
			t.Fatalf("after %s: %d calls; want %d", after, calls, want)
		}
	}
	pass(0, 1)
	pass(0, 2)
	pass(0, 2)
	pass(billingInterval, 3)
	pass(billingInterval, 3)
	pass(billingInterval, 4)
	if standing, err := tenant.SpendStanding(t.Context()); err != nil || !standing.Refill.Open || len(mail.sent) != 0 {
		t.Fatalf("a refill the service has not answered yet stopped: %+v, %v, mail %d", standing.Refill, err, len(mail.sent))
	}
	pass(checkoutRetryLimit, 5)
	pass(checkoutRetryMaxWait, 5)
	standing, err := tenant.SpendStanding(t.Context())
	if err != nil || standing.Refill.Open || standing.Refill.Failure != refillCheckoutUnavailable {
		t.Fatalf("refill state %+v: %v; want it closed as %s", standing.Refill, err, refillCheckoutUnavailable)
	}
	if !strings.Contains(logs.String(), `"alert":"`+checkoutFailedAlert+`"`) || len(mail.sent) != 1 || !strings.Contains(mail.sent[0].Text, refillCheckoutUnavailable) {
		t.Fatalf("no alert or owner mail for a refill the service never answered: mail %+v", mail.sent)
	}
}

// Each refill is authorized immediately before its own call, so a team that turns refills off, is held or tops up
// above its threshold while another team's call is in flight is not charged.
func TestARefillIsAuthorizedImmediatelyBeforeItsOwnCall(t *testing.T) {
	for change, query := range map[string]string{
		"disabled": `UPDATE teams SET refill_enabled=0 WHERE name='b'`,
		"held":     `INSERT INTO credit_freezes(dispute_id,team,reason,cause,placed_by,created_at) VALUES ('operator:b','b','review','operator','owner',1)`,
		"topped up": `INSERT INTO credit_grants(id,team,kind,amount_micro,created_at) VALUES ('topup','b','paid',` +
			strconv.FormatInt(5000*store.MicroCreditsPerCent, 10) + `,1)`,
	} {
		t.Run(change, func(t *testing.T) {
			e, _ := refillFixture(t)
			engineSQL(t, e.Store, `INSERT INTO teams(name,created_at,updated_at,card_customer,card_added_at,refill_enabled,refill_threshold_cents,refill_amount_cents) VALUES ('b',1,1,'cus_b',1,1,1000,2000)`)
			worker := NewDeployment(e.Store, deploymenttest.Team(t), nil).WithBillingCheckout("https://checkout.test", "credential")
			var teams []string
			worker.checkout.http.Transport = billingPageTransport(func(r *http.Request) (*http.Response, error) {
				var body refillRequest
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				teams = append(teams, body.Team)
				if body.Team == "a" {
					engineSQL(t, e.Store, query)
				}
				return refillReply(http.StatusOK, `{"payment_intent":"pi_`+body.Team+`","status":"processing"}`), nil
			})
			worker.refillPass(t.Context())
			if len(teams) != 1 || teams[0] != "a" {
				t.Fatalf("charged teams %v; want only a, whose authorization preceded the change to b", teams)
			}
		})
	}
}

// The pass records the payment intent a charged answer names, so a refill withdrawn before its webhook still counts
// against the team's limits.
func TestAChargedAnswerKeepsAWithdrawnRefillInTheLimits(t *testing.T) {
	e, tenant := refillFixture(t)
	worker := NewDeployment(e.Store, deploymenttest.Team(t), nil).WithBillingCheckout("https://checkout.test", "credential")
	worker.checkout.http.Transport = billingPageTransport(func(*http.Request) (*http.Response, error) {
		return refillReply(http.StatusOK, `{"payment_intent":"pi_answered","status":"succeeded"}`), nil
	})
	worker.refillPass(t.Context())
	engineSQL(t, e.Store, `UPDATE teams SET refill_enabled=0 WHERE name='a'`)
	worker.refillPass(t.Context())
	standing, err := tenant.SpendStanding(t.Context())
	if err != nil || standing.Refill.Open || standing.Refill.DayCount != 1 || standing.Refill.WeekCents != 2000 {
		t.Fatalf("refill state %+v: %v; want the withdrawn charged refill counted", standing.Refill, err)
	}
}
