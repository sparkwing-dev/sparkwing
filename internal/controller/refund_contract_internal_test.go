package controller

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability/paymenttest"
	"github.com/sparkwing-dev/sparkwing/internal/deployment/deploymenttest"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestCapturedRefundSettlesExactlyOnce(t *testing.T) {
	contract := loadCheckoutContract(t)
	var event struct {
		ID   string `json:"id"`
		Data struct {
			Object struct {
				ID            string `json:"id"`
				PaymentIntent string `json:"payment_intent"`
			} `json:"object"`
		} `json:"data"`
	}
	if err := json.Unmarshal(contract.RefundEvent, &event); err != nil {
		t.Fatal(err)
	}
	for _, order := range []string{"paid_first", "refund_first", "purged"} {
		t.Run(order, func(t *testing.T) {
			e, tenant := engineServerFixture(t, deploymenttest.Cloud(t, "https://controller.test"), nil)
			engineSQL(t, e.Store, `INSERT INTO settings(key,value) VALUES ('payment_mode',?)`, []byte(`"test"`))
			billingModelCheckout(t, e.Store, tenant, "cs_refund_contract", 2000)
			deployment, err := e.Store.AsOperator().ConfigureDeployment(t.Context(), "", func() error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			provider := paymenttest.Provider{}.SettledCheckout(event.Data.Object.PaymentIntent, false, "")
			path := "/v1/payment_intents/" + event.Data.Object.PaymentIntent
			provider[path] = string(bytes.ReplaceAll([]byte(provider[path]), []byte(`"object":"payment_intent"`), []byte(fmt.Sprintf(`"object":"payment_intent","metadata":{"sparkwing_deployment":%q}`, deployment))))
			h, deliver := billingModelProviderDelivery(t, e, provider, func(s *Server) *Server { return s.WithPaymentDeployment(deployment) })
			accept := func(body []byte) {
				t.Helper()
				now := time.Now().Unix()
				mac := hmac.New(sha256.New, []byte("whsec_fixture"))
				fmt.Fprintf(mac, "%d.%s", now, body)
				req := httptest.NewRequest(http.MethodPost, "/webhooks/stripe", bytes.NewReader(body))
				req.Header.Set("Stripe-Signature", fmt.Sprintf("t=%d,v1=%x", now, mac.Sum(nil)))
				res := httptest.NewRecorder()
				h.ServeHTTP(res, req)
				if res.Code != http.StatusNoContent {
					t.Fatalf("refund: %d %s", res.Code, res.Body)
				}
			}
			if order == "refund_first" {
				accept(contract.RefundEvent)
			}
			paid := fmt.Sprintf(`{"id":"cs_refund_contract","object":"checkout.session","mode":"payment","livemode":false,"payment_status":"paid","payment_intent":%q,"amount_total":2000,"currency":"usd"}`, event.Data.Object.PaymentIntent)
			if res := deliver("evt_refund_contract_paid", "checkout.session.completed", paid); res.Code != http.StatusNoContent {
				t.Fatalf("paid: %d %s", res.Code, res.Body)
			}
			if order == "purged" {
				op := e.Store.AsOperator()
				e.Server.WithOperatorAccounts([]string{"refund-operator"})
				console, authority := teststore.Console(t, e.Store, tenant.Team(), "refund-operator")
				if _, err := console.ConsoleDeleteTeam(t.Context(), authority); err != nil {
					t.Fatal(err)
				}
				if ok, err := op.ClaimTeamDeletion(t.Context(), tenant.Team(), "purger", time.Minute); err != nil || !ok {
					t.Fatalf("claim: %t %v", ok, err)
				}
				if err := op.PurgeTeam(t.Context(), tenant.Team(), "purger", 0); err != nil {
					t.Fatal(err)
				}
			}
			for range 2 {
				accept(contract.RefundEvent)
			}
			updated := bytes.ReplaceAll(contract.RefundEvent, []byte(event.ID), []byte("evt_contract_refund_updated"))
			updated = bytes.ReplaceAll(updated, []byte("refund.created"), []byte("refund.updated"))
			accept(updated)
			balance, err := tenant.CreditBalanceMicro(t.Context())
			if err != nil || balance != 1500*store.MicroCreditsPerCent {
				t.Fatalf("balance: %d %v", balance, err)
			}
			second := bytes.ReplaceAll(contract.RefundEvent, []byte(event.ID), []byte("evt_contract_second_refund"))
			second = bytes.ReplaceAll(second, []byte(event.Data.Object.ID), []byte("re_contract_second_refund"))
			for range 2 {
				accept(second)
			}
			balance, err = tenant.CreditBalanceMicro(t.Context())
			if err != nil || balance != 1000*store.MicroCreditsPerCent {
				t.Fatalf("second refund balance: %d %v", balance, err)
			}
			var count int
			if err := teststore.SQL(t, e.Store).QueryRow(`SELECT COUNT(*) FROM credit_grants WHERE kind='reversal'`).Scan(&count); err != nil || count != 2 {
				t.Fatalf("reversals: %d %v", count, err)
			}
		})
	}
}
