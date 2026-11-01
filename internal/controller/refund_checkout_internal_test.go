package controller

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/deployment/deploymenttest"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestQueuedRefundLogLevel(t *testing.T) {
	for _, tc := range []struct {
		status, level string
	}{
		{"succeeded", "INFO"},
		{"pending", "INFO"},
		{"failed", "WARN"},
		{"canceled", "WARN"},
	} {
		t.Run(tc.status, func(t *testing.T) {
			e, _ := engineRuntimeStore(t)
			engineSQL(t, e.Store, `INSERT INTO settings(key,value) VALUES ('payment_mode',?)`, []byte(`"test"`))
			engineSQL(t, e.Store, `INSERT INTO card_refunds(payment_mode,team,payment_intent,reason,amount_micro,created_at) VALUES ('test','a','pi_due','refused',?,1)`, 1_000*store.MicroCreditsPerCent)
			var logs bytes.Buffer
			worker := NewDeployment(e.Store, deploymenttest.Team(t), slog.New(slog.NewJSONHandler(&logs, nil))).WithBillingCheckout("https://checkout.test", "credential")
			worker.checkout.http.Transport = billingPageTransport(func(*http.Request) (*http.Response, error) {
				return refillReply(http.StatusOK, fmt.Sprintf(`{"refund":"re_due","status":%q}`, tc.status)), nil
			})
			worker.refundQueuedPayments(t.Context())
			records := auditLines(t, &logs, "billing alert: a queued card refund was submitted")
			if len(records) != 1 || records[0]["level"] != tc.level || records[0]["status"] != tc.status || records[0]["payment_intent"] != "pi_due" || records[0]["refund"] != "re_due" {
				t.Fatalf("refund diagnostic = %s, want %s with payment and refund", &logs, tc.level)
			}
		})
	}
}

// A queued refund the checkout service refuses, or cannot answer for checkoutRetryLimit, is not retried forever: it is
// listed as failed for the operator, alerts, and its team's owners are told once, as for a refund Stripe failed.
func TestARefundTheCheckoutServiceCannotMakeIsLeftToTheOperator(t *testing.T) {
	for _, tc := range []struct {
		name   string
		answer func() (*http.Response, error)
		after  time.Duration
	}{
		{"refused", func() (*http.Response, error) {
			return refillReply(http.StatusForbidden, `{"error":"the payment belongs to another deployment"}`), nil
		}, 0},
		{"unanswered", func() (*http.Response, error) { return nil, errors.New("connection refused") }, checkoutRetryLimit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, tenant := engineRuntimeStore(t)
			engineSQL(t, e.Store, `INSERT INTO settings(key,value) VALUES ('payment_mode',?)`, []byte(`"test"`))
			refillOwner(t, e)
			engineSQL(t, e.Store, `INSERT INTO card_refunds(payment_mode,team,payment_intent,reason,amount_micro,created_at) VALUES ('test','a','pi_due','refused',?,1)`, 1_000*store.MicroCreditsPerCent)
			var logs bytes.Buffer
			mail := &refundMail{}
			worker := NewDeployment(e.Store, deploymenttest.Team(t), nil).WithBillingCheckout("https://checkout.test", "credential").WithMailer(mail)
			worker.logger = slog.New(slog.NewJSONHandler(&logs, nil))
			now := time.Unix(1_800_000_000, 0)
			worker.checkout.now = func() time.Time { return now }
			calls := 0
			worker.checkout.http.Transport = billingPageTransport(func(*http.Request) (*http.Response, error) {
				calls++
				return tc.answer()
			})
			worker.refundQueuedPayments(t.Context())
			now = now.Add(tc.after)
			for range 3 {
				worker.refundQueuedPayments(t.Context())
				worker.noticeFailedRefunds(t.Context())
			}
			want := 1
			if tc.after > 0 {
				want = 2
			}
			if calls != want {
				t.Fatalf("checkout calls %d; want %d", calls, want)
			}
			failed, err := tenant.FailedCardRefunds(t.Context())
			if err != nil || len(failed) != 1 || failed[0].PaymentIntent != "pi_due" {
				t.Fatalf("failed refunds %+v, %v; want the queued refund listed", failed, err)
			}
			if !strings.Contains(logs.String(), `"alert":"`+checkoutFailedAlert+`"`) || len(mail.sent) != 1 || !strings.Contains(mail.sent[0].Text, "$10.00") {
				t.Fatalf("no alert or owner mail for a refund the service did not make: mail %+v", mail.sent)
			}
			records := auditLines(t, &logs, "billing alert: the checkout service did not make a queued refund; it is left to the operator")
			if len(records) != 1 || records[0]["level"] != "ERROR" {
				t.Fatalf("failed refund must log at error: %s", &logs)
			}
		})
	}
}
