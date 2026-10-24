package controller

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability/paymenttest"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestSignedFraudWarningUsesSettledLiabilityShift(t *testing.T) {
	for _, kind := range []string{"checkout", "refill"} {
		for _, result := range []string{"authenticated", "attempt_acknowledged", "", "failed", "unknown"} {
			for _, warningFirst := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/warning_first=%t", kind, result, warningFirst), func(t *testing.T) {
					e, tenant := refillFixture(t)
					s := e.Store
					payment, paidKind, paidObject := "pi_refill", "payment_intent.succeeded", refillIntent("pi_refill", "succeeded")
					if kind == "checkout" {
						billingModelCheckout(t, s, tenant, "cs_refill", 2000)
						paidKind, paidObject = "checkout.session.completed", stolenCardSession("cs_refill", payment)
					} else {
						due, err := s.AsOperator().DueRefills(t.Context())
						if err != nil || len(due) != 1 {
							t.Fatalf("reserve refill = %+v, %v", due, err)
						}
						if err := s.AsOperator().RecordRefillIntent(t.Context(), "a", due[0].ID, payment); err != nil {
							t.Fatal(err)
						}
					}
					engineSQL(t, s, `INSERT INTO teams(name,created_at,updated_at,card_customer,card_payment_method,card_fingerprint) VALUES ('b',1,1,'cus_b','pm_b','fp_refill')`)
					deployment, err := s.AsOperator().ConfigureDeployment(t.Context(), "", store.DefaultFreeTeamSlots, func() error { return nil })
					if err != nil {
						t.Fatal(err)
					}
					warning := strings.TrimSuffix(stolenCardWarning("issfr_refill", payment), "}") + fmt.Sprintf(`,"metadata":{"sparkwing_deployment":%q}}`, deployment)
					provider := paymenttest.Provider{}.Charge(payment, false, result, "fp_refill")
					_, deliver := billingModelProviderDelivery(t, e, provider, func(s *Server) *Server { return s.WithPaymentDeployment(deployment) })
					accept := func(id, kind, object string) {
						t.Helper()
						if response := deliver(id, kind, object); response.Code != http.StatusNoContent {
							t.Fatalf("%s: %d %s", id, response.Code, response.Body)
						}
					}
					protected := result == "authenticated"
					opposite := "authenticated"
					if protected {
						opposite = "failed"
					}
					if warningFirst {
						provider.Charge(payment, false, opposite, "fp_refill")
						accept("evt_warning", "radar.early_fraud_warning.created", warning)
						provider.Charge(payment, false, result, "fp_refill")
					}
					accept("evt_paid", paidKind, paidObject)
					var spent int64
					if !warningFirst {
						spent = 500 * store.MicroCreditsPerCent
						engineSQL(t, s, `INSERT INTO credit_charges(id,team,run_id,node_id,capability_prefix,kind,seconds,amount_micro,charged_at) VALUES ('spent','a','run','one','cloud','usage',60,?,1)`, spent)
					}
					provider.Charge(payment, false, opposite, "fp_refill")
					accept("evt_warning", "radar.early_fraud_warning.created", warning)
					accept("evt_warning_again", "radar.early_fraud_warning.created", warning)
					accept("evt_paid", paidKind, paidObject)
					accept("evt_paid_again", paidKind, paidObject)
					balance, err := tenant.CreditBalanceMicro(t.Context())
					wantBalance := -spent
					if protected {
						wantBalance += 2000 * store.MicroCreditsPerCent
					}
					if err != nil || balance != wantBalance {
						t.Fatalf("balance = %d, want %d: %v", balance, wantBalance, err)
					}
					refunds, err := s.AsOperator().DueCardRefunds(t.Context())
					wantRefunds := 1
					if protected {
						wantRefunds = 0
					}
					if err != nil || len(refunds) != wantRefunds {
						t.Fatalf("refunds = %+v, want %d: %v", refunds, wantRefunds, err)
					}
					freeze, err := tenant.CreditFreeze(t.Context())
					if err != nil || !freeze.Frozen || len(freeze.Disputes) != 1 || len(freeze.Causes) != 1 || freeze.Causes[0] != store.HoldCauseWarning {
						t.Fatalf("payment hold = %+v: %v", freeze, err)
					}
					var heldPayment, savedCard string
					if err := teststore.SQL(t, s).QueryRow(`SELECT payment_id FROM credit_freezes WHERE team='a' AND released_at IS NULL`).Scan(&heldPayment); err != nil || heldPayment != payment {
						t.Fatalf("held payment = %q: %v", heldPayment, err)
					}
					if err := teststore.SQL(t, s).QueryRow(`SELECT card_payment_method FROM teams WHERE name='b'`).Scan(&savedCard); err != nil || savedCard != "" {
						t.Fatalf("deployment card block = %q: %v", savedCard, err)
					}
					standing, err := tenant.BillingStanding(t.Context())
					if err != nil || standing.Tier != "Held" || standing.CloudVCPU != 0 {
						t.Fatalf("held spending = %+v: %v", standing, err)
					}
					var persistedResult string
					var persistedShift bool
					if err := teststore.SQL(t, s).QueryRow(`SELECT three_d_secure,liability_shift FROM credit_checkouts WHERE team='a' AND payment_id=?`, payment).Scan(&persistedResult, &persistedShift); err != nil || persistedResult != result || persistedShift != protected {
						t.Fatalf("settled authentication/protection = %q/%t, want %q/%t: %v", persistedResult, persistedShift, result, protected, err)
					}
					if protected {
						if count, err := tenant.ReleaseCreditFreezes(t.Context(), freeze.Disputes[0], "operator", "dispute window closed"); err != nil || count != 1 {
							t.Fatalf("release = %d: %v", count, err)
						}
						if restored, err := tenant.CreditBalanceMicro(t.Context()); err != nil || restored != wantBalance {
							t.Fatalf("released credit = %d: %v", restored, err)
						}
					}
				})
			}
		}
	}
}
