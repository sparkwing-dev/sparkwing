package store_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/capability/paymenttest"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

// Stripe holds every card detail. A team's card setup, an Add funds payment, an automatic refill, an early fraud
// warning on it, its refund and the team's purge all pass through signed events whose provider objects carry the
// card's brand, last four, fingerprint, payment method and payer email, and none of those reach any column of any
// table. The owner's account email stays in the account tables alone. The warning holds the team its payment funded.
func TestNoCardDetailsAreStoredFromSetupThroughPurge(t *testing.T) {
	ctx := t.Context()
	s := storetest.Open(t)
	const deployment, owner = "dep-cards", "owner-sentinel@example.test"
	for _, query := range []string{
		`INSERT INTO teams(name,created_at,updated_at,billing_trust,billing_trust_at,billing_trust_by,billing_trust_reason) VALUES ('a',1,1,'granted',1,'operator','customer')`,
		`INSERT INTO team_counters(team) VALUES ('a')`,
		`INSERT INTO accounts(id,email,email_verified,created_at,updated_at) VALUES ('acct','` + owner + `',1,1,1)`,
		`INSERT INTO memberships(team,account_id,role,created_at) VALUES ('a','acct','owner',1)`,
		`INSERT INTO payment_correlations(payment_mode,provider_id,kind,team,customer_id) VALUES ('test','cs_setup','setup_session','a','cus_cards')`,
		fmt.Sprintf(`INSERT INTO credit_checkouts(id,team,session_id,payment_mode,currency,amount_minor,amount_micro,opened_at,expires_at) VALUES ('checkout','a','cs_paid','test','usd',2000,%d,1,%d)`,
			2000*store.MicroCreditsPerCent, time.Now().Add(time.Hour).UnixNano()),
	} {
		if _, err := s.DB().ExecContext(ctx, query); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	provider := paymenttest.Provider{}.SettledCheckout("pi_paid", false, "authenticated").SettledCheckout("pi_refill", false, "")
	dir := t.TempDir()
	key, secret := filepath.Join(dir, "key"), filepath.Join(dir, "secret")
	for path, value := range map[string]string{key: "sk_test_cards", secret: "whsec_cards"} {
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	verifier, err := capability.NewPaymentAuthenticator(capability.PaymentConfig{Mode: capability.PaymentTest, APIKeyFile: key, WebhookSecretFile: secret, HTTPTransport: provider})
	if err != nil {
		t.Fatal(err)
	}
	card := fmt.Sprintf(`"payment_method":%q,"receipt_email":%q,"customer_details":{"email":%q},"payment_method_details":{"card":{"brand":%q,"last4":%q,"fingerprint":%q}}`,
		paymenttest.CardMethod, paymenttest.PayerEmail, paymenttest.PayerEmail, paymenttest.CardBrand, paymenttest.CardLast4, paymenttest.CardFingerprint)
	stamp := `"metadata":{"sparkwing_deployment":"` + deployment + `"`
	deliver := func(id, kind, object string) {
		t.Helper()
		now := time.Now().Unix()
		body := fmt.Sprintf(`{"id":%q,"object":"event","type":%q,"created":%d,"livemode":false,"data":{"object":%s}}`, id, kind, now, object)
		mac := hmac.New(sha256.New, []byte("whsec_cards"))
		fmt.Fprintf(mac, "%d.%s", now, body)
		proof, err := verifier.VerifyPaymentEvent(ctx, capability.PaymentPresentation{Body: []byte(body), Signature: fmt.Sprintf("t=%d,v1=%s", now, hex.EncodeToString(mac.Sum(nil)))},
			capability.PaymentScope{Deployment: deployment, Records: s.AsOperator()})
		if err != nil || proof == nil {
			t.Fatalf("%s: proof=%v err=%v", id, proof, err)
		}
		if _, err := s.AsOperator().SettlePayment(ctx, *proof); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
	}

	deliver("evt_page", "checkout.session.completed", `{"id":"cs_setup","object":"checkout.session","mode":"setup","status":"complete","livemode":false,"customer":"cus_cards","setup_intent":"seti_cards",`+card+`,`+stamp+`,"purpose":"card_setup"}}`)
	deliver("evt_setup", "setup_intent.succeeded", `{"id":"seti_cards","object":"setup_intent","status":"succeeded","livemode":false,"customer":"cus_cards",`+card+`,`+stamp+`,"purpose":"card_setup"}}`)
	tenant, err := s.ForTeam(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	if spend, err := tenant.SpendStanding(ctx); err != nil || spend.Card.Customer != "cus_cards" || spend.Card.AddedAt.IsZero() {
		t.Fatalf("card after setup = %+v, %v; want the customer recorded as having a card", spend.Card, err)
	}
	deliver("evt_paid", "checkout.session.completed", `{"id":"cs_paid","object":"checkout.session","mode":"payment","payment_status":"paid","livemode":false,"payment_intent":"pi_paid","amount_total":2000,"currency":"usd",`+card+`,`+stamp+`}}`)

	if _, err := s.DB().ExecContext(ctx, `UPDATE teams SET refill_enabled=1, refill_threshold_cents=1000000, refill_amount_cents=1500 WHERE name='a'`); err != nil {
		t.Fatal(err)
	}
	due, err := collectDueRefills(ctx, s.AsOperator())
	if err != nil || len(due) != 1 || due[0].Customer != "cus_cards" {
		t.Fatalf("due refills = %+v, %v", due, err)
	}
	deliver("evt_refill", "payment_intent.succeeded", `{"id":"pi_refill","object":"payment_intent","status":"succeeded","livemode":false,"amount":1500,"amount_received":1500,"currency":"usd","customer":"cus_cards","latest_charge":"ch_refill",`+card+`,`+stamp+`,"purpose":"card_refill","refill_id":"`+due[0].ID+`"}}`)
	deliver("evt_warning", "radar.early_fraud_warning.created", `{"id":"issfr_refill","object":"radar.early_fraud_warning","livemode":false,"actionable":true,"charge":"ch_refill","payment_intent":"pi_refill",`+card+`}`)
	if standing, err := tenant.BillingStanding(ctx); err != nil || standing.Tier != "Held" {
		t.Fatalf("standing after warning = %+v, %v; want the warned payment's team held", standing, err)
	}
	refunds, err := s.AsOperator().DueCardRefunds(ctx)
	if err != nil || len(refunds) != 1 || refunds[0].PaymentIntent != "pi_refill" || refunds[0].Team != "a" {
		t.Fatalf("refunds after warning = %+v, %v; want the warned refill queued", refunds, err)
	}
	deliver("evt_refund", "refund.updated", `{"id":"re_refill","object":"refund","payment_intent":"pi_refill","charge":"ch_refill","status":"succeeded","amount":1500,"currency":"usd","livemode":false}`)

	assertNoCardDetails(t, s, owner)
	if _, _, err := s.AsOperator().RequestTeamDeletion(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.AsOperator().ClaimTeamDeletion(ctx, "a", "purger", time.Minute); err != nil || !ok {
		t.Fatalf("claim=%v: %v", ok, err)
	}
	if err := s.AsOperator().PurgeTeam(ctx, "a", "purger", 0); err != nil {
		t.Fatal(err)
	}
	assertNoCardDetails(t, s, owner)
}

func assertNoCardDetails(t *testing.T, s *store.Store, owner string) {
	t.Helper()
	ctx := t.Context()
	query := `SELECT name FROM sqlite_master WHERE type='table' ORDER BY name`
	if s.Dialect() == store.DialectPostgres {
		query = `SELECT table_name FROM information_schema.tables WHERE table_schema=current_schema() ORDER BY table_name`
	}
	rows, err := s.DB().QueryContext(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, table)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	_ = rows.Close()
	if len(tables) < 50 {
		t.Fatalf("found only %d tables", len(tables))
	}
	// safety: four digits occur by chance inside hex digests, so the last four counts only as a
	// whole column value or a whole JSON string; the long, distinctive values count anywhere.
	card := []string{paymenttest.CardFingerprint, paymenttest.CardBrand, paymenttest.CardMethod, paymenttest.PayerEmail, `"` + paymenttest.CardLast4 + `"`}
	for _, table := range tables {
		rows, err := s.DB().QueryContext(ctx, `SELECT * FROM `+table)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			values, pointers := make([]any, len(columns)), make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				t.Fatal(err)
			}
			for i, column := range columns {
				raw := fmt.Sprint(values[i])
				if b, ok := values[i].([]byte); ok {
					raw = string(b)
				}
				for _, detail := range card {
					if strings.Contains(raw, detail) {
						t.Errorf("%s.%s stores card detail %q: %s", table, column, detail, raw)
					}
				}
				if raw == paymenttest.CardLast4 {
					t.Errorf("%s.%s stores the card's last four", table, column)
				}
				if strings.Contains(raw, owner) && table != "accounts" {
					t.Errorf("%s.%s stores the owner's email: %s", table, column, raw)
				}
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		_ = rows.Close()
	}
}
