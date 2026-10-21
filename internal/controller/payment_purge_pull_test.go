package controller

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/capability/paymenttest"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestPurgedPaidTeamDoesNotBlockPaymentRecovery(t *testing.T) {
	s, _, _ := billingNativeServer(t)
	tenant, err := s.store.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	billingModelCheckout(t, s.store, tenant, "cs_purged", 1000)
	dir := t.TempDir()
	key, secret := filepath.Join(dir, "key"), filepath.Join(dir, "secret")
	for path, value := range map[string]string{key: "sk_test_purge", secret: "whsec_purge"} {
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	provider := paymenttest.Provider{}.SettledCheckout("pi_purged", false, "authenticated")
	objects := `[{"id":"cs_purged","object":"checkout.session","created":1,"livemode":false,"mode":"payment","payment_status":"paid","payment_intent":"pi_purged","amount_total":1000,"currency":"usd","metadata":{"sparkwing_deployment":"restore.test"}}]`
	collections := map[string]int{}
	verifier, err := capability.NewPaymentAuthenticator(capability.PaymentConfig{Mode: capability.PaymentTest, APIKeyFile: key, WebhookSecretFile: secret, HTTPTransport: billingPageTransport(func(r *http.Request) (*http.Response, error) {
		if _, ok := provider[r.URL.Path]; ok {
			return provider.RoundTrip(r)
		}
		collections[r.URL.Path]++
		data := `[]`
		if r.URL.Path == "/v1/checkout/sessions" {
			data = objects
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"object":"list","has_more":false,"data":` + data + `}`))}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	s.WithPayments(verifier, s.store.AsOperator()).WithPaymentDeployment("restore.test")
	if err := s.ReconcilePayments(t.Context()); err != nil {
		t.Fatal(err)
	}
	if state, err := tenant.CreditState(t.Context(), 0); err != nil || state.BalanceMicro != 1000*store.MicroCreditsPerCent {
		t.Fatalf("paid team before purge: %+v %v", state, err)
	}
	op := s.store.AsOperator()
	if _, _, err := op.RequestTeamDeletion(t.Context(), "a"); err != nil {
		t.Fatal(err)
	}
	if ok, err := op.ClaimTeamDeletion(t.Context(), "a", "purge", time.Hour); err != nil || !ok {
		t.Fatalf("claim: %t %v", ok, err)
	}
	if err := op.PurgeTeam(t.Context(), "a", "purge", time.Hour); err != nil {
		t.Fatal(err)
	}
	for _, route := range []capability.PaymentRoute{
		{Mode: capability.PaymentTest, CheckoutID: "cs_purged"},
		{Mode: capability.PaymentTest, PaymentID: "pi_purged", ObjectID: "re_after_purge"},
	} {
		state, err := op.RecordsPaymentParent(t.Context(), route)
		if err != nil || state != capability.PaymentParentPurged {
			t.Fatalf("purged provider parent: %v %v", state, err)
		}
	}
	if state, err := op.RecordsPaymentParent(t.Context(), capability.PaymentRoute{Mode: capability.PaymentLive, PaymentID: "pi_purged"}); err != nil || state != capability.PaymentParentMissing {
		t.Fatalf("closure escaped payment mode: %v %v", state, err)
	}
	var closures, redacted int
	if err := teststore.SQL(t, s.store).QueryRowContext(t.Context(), `SELECT COUNT(*),COUNT(CASE WHEN length(provider_digest)=64 THEN 1 END) FROM payment_closures`).Scan(&closures, &redacted); err != nil || closures != 2 || redacted != 2 {
		t.Fatalf("redacted closure evidence: count=%d redacted=%d err=%v", closures, redacted, err)
	}
	for range 2 {
		if err := s.ReconcilePayments(t.Context()); err != nil {
			t.Fatalf("periodic sweep after purge: %v", err)
		}
	}
	if collections["/v1/radar/early_fraud_warnings"] != 3 {
		t.Fatalf("sweeps stopped before later financial collections: %v", collections)
	}
	backup := filepath.Join(dir, "purged-backup.db")
	if _, err := teststore.SQL(t, s.store).ExecContext(t.Context(), `VACUUM INTO ?`, backup); err != nil {
		t.Fatal(err)
	}
	restored, err := teststore.OpenExisting(backup)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := restored.Close(); err != nil {
			t.Error(err)
		}
	}()
	if err := restored.AsOperator().PrepareRestore(t.Context(), "purged-restore"); err != nil {
		t.Fatal(err)
	}
	recovery := New(restored, nil).WithPayments(verifier, restored.AsOperator()).WithPaymentDeployment("restore.test")
	if err := restored.AsOperator().ReconcileRestore(t.Context(), func(ctx context.Context) error { return recovery.ReconcilePayments(ctx) }); err != nil {
		t.Fatalf("restore reconciliation after purge: %v", err)
	}
	if err := restored.AsOperator().AcknowledgeRestore(t.Context()); err != nil {
		t.Fatal(err)
	}
	objects = strings.ReplaceAll(objects, "cs_purged", "cs_lost")
	objects = strings.ReplaceAll(objects, "pi_purged", "pi_lost")
	provider = provider.SettledCheckout("pi_lost", false, "authenticated")
	if err := recovery.ReconcilePayments(t.Context()); err == nil {
		t.Fatal("missing restored reservation treated as legitimate purge")
	}
}
