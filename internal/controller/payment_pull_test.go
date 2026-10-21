package controller

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/capability/paymenttest"
	"github.com/sparkwing-dev/sparkwing/internal/credentials"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestPaymentPullRestoresUnsettledPaymentOnce(t *testing.T) {
	s, _, _ := billingNativeServer(t)
	tenant, err := s.store.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	billingModelCheckout(t, s.store, tenant, "cs_restore", 1000)
	dir := t.TempDir()
	key, secret := filepath.Join(dir, "key"), filepath.Join(dir, "secret")
	if err := os.WriteFile(key, []byte("sk_test_restore"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secret, []byte("whsec_restore"), 0o600); err != nil {
		t.Fatal(err)
	}

	epoch, err := credentials.LoadOrCreateEpoch(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.store.AsOperator().PrepareRestore(t.Context(), epoch); err != nil {
		t.Fatal(err)
	}
	_, token, err := teststore.ProvisionCredential(t.Context(), s.store, capability.ProvisionSpec{Kind: capability.ProvisionBootstrap, Subject: capability.Subject{Kind: capability.SubjectOperator, ID: "restore-drill"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.OperatorTokens}, ExpiresAt: teststore.CredentialExpiry(time.Now(), time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	var encoded []byte
	if err := teststore.SQL(t, s.store).QueryRowContext(t.Context(), `SELECT authority_json FROM capabilities WHERE prefix=?`, token.Prefix).Scan(&encoded); err != nil {
		t.Fatal(err)
	}
	var record store.CapabilityRecord
	if err := json.Unmarshal(encoded, &record); err != nil {
		t.Fatal(err)
	}
	record.Value.CredentialEpoch = epoch
	encoded, err = json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	engineSQL(t, s.store, `UPDATE capabilities SET authority_json=? WHERE prefix=?`, encoded, token.Prefix)
	livePath := filepath.Join(dir, "live.db")
	if _, err := teststore.SQL(t, s.store).ExecContext(t.Context(), `VACUUM INTO ?`, livePath); err != nil {
		t.Fatal(err)
	}
	live, err := teststore.OpenExisting(livePath, store.WithCredentialEpoch(epoch))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := live.Close(); err != nil {
			t.Error(err)
		}
	}()
	liveTenant, err := live.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := liveTenant.ArmCronSchedule(t.Context(), store.CronSchedule{ID: "restore-nightly", RepoPath: "https://example.com/acme/repo", Pipeline: "nightly", Cron: "* * * * *", CatchUp: 24 * time.Hour}); err != nil {
		t.Fatal(err)
	}
	backupPath := filepath.Join(dir, "backup.db")
	if _, err := teststore.SQL(t, live).ExecContext(t.Context(), `VACUUM INTO ?`, backupPath); err != nil {
		t.Fatal(err)
	}
	firedAfterBackup := time.Now().UTC()
	if _, err := teststore.SQL(t, live).ExecContext(t.Context(), `UPDATE cron_schedules SET cursor_at=?,last_fired_at=? WHERE id='restore-nightly'`, firedAfterBackup.UnixNano(), firedAfterBackup.UnixNano()); err != nil {
		t.Fatal(err)
	}
	ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: token.Prefix}
	if _, err := live.AsOperator().ResolveAuthority(t.Context(), ref); err != nil {
		t.Fatal(err)
	}
	if _, err := teststore.SQL(t, live).ExecContext(t.Context(), `UPDATE capabilities SET revoked_at=1 WHERE prefix=?`, token.Prefix); err != nil {
		t.Fatal(err)
	}
	if _, err := live.AsOperator().ResolveAuthority(t.Context(), ref); err == nil {
		t.Fatal("revoked live token accepted")
	}
	checkoutID := "cs_restore"
	unknownRefill := false
	provider := paymenttest.Provider{}.SettledCheckout("pi_restore", false, "authenticated").SettledCheckout("pi_unbound", false, "authenticated")
	verifier, err := capability.NewPaymentAuthenticator(capability.PaymentConfig{Mode: capability.PaymentTest, APIKeyFile: key, WebhookSecretFile: secret, HTTPTransport: billingPageTransport(func(r *http.Request) (*http.Response, error) {
		if _, ok := provider[r.URL.Path]; ok {
			return provider.RoundTrip(r)
		}
		data := `[]`
		if r.URL.Path == "/v1/checkout/sessions" && checkoutID != "" {
			data = `[{"id":"` + checkoutID + `","object":"checkout.session","created":1,"livemode":false,"mode":"payment","payment_status":"paid","payment_intent":"pi_restore","amount_total":1000,"currency":"usd","metadata":{"sparkwing_deployment":"restore.test"}}]`
		}

		if r.URL.Path == "/v1/payment_intents" && unknownRefill {
			data = `[{"id":"pi_unbound","object":"payment_intent","created":1,"livemode":false,"status":"succeeded","amount":1000,"amount_received":1000,"currency":"usd","latest_charge":"ch_unbound","metadata":{"sparkwing_deployment":"restore.test","purpose":"card_refill"}}]`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"object":"list","has_more":false,"data":` + data + `}`))}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}

	if err := ReconcilePayments(t.Context(), verifier, "restore.test", live.AsOperator()); err != nil {
		t.Fatal(err)
	}
	backup, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	restoredPath := filepath.Join(dir, "restored.db")
	if err := os.WriteFile(restoredPath, backup, 0o600); err != nil {
		t.Fatal(err)
	}
	restoredEpoch, err := credentials.RotateEpoch(dir)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := teststore.OpenExisting(restoredPath, store.WithCredentialEpoch(restoredEpoch))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := restored.Close(); err != nil {
			t.Error(err)
		}
	}()
	if err := restored.EnsureCredentialEpoch(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := restored.AsOperator().ResolveAuthority(t.Context(), ref); err == nil {
		t.Fatal("backup resurrected revoked token")
	}
	tenant, err = restored.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	if ran, err := restored.AsOperator().RunCronTickLeased(t.Context(), "restore-drill", time.Minute, func(context.Context) error { t.Fatal("restored cron tick ran before acknowledgement"); return nil }); ran || !errors.Is(err, store.ErrRestorePending) {
		t.Fatalf("restored cron hold: ran=%t err=%v", ran, err)
	}
	state, err := tenant.CreditState(t.Context(), 0)
	if err != nil || state.BalanceMicro != 0 {
		t.Fatalf("restored backup already has post-backup payment: %+v %v", state, err)
	}
	for sweep := 0; sweep < 2; sweep++ {
		if err := restored.AsOperator().ReconcileRestore(t.Context(), func(ctx context.Context) error {
			return ReconcilePayments(ctx, verifier, "restore.test", restored.AsOperator())
		}); err != nil {
			t.Fatal(err)
		}
		state, err := tenant.CreditState(t.Context(), 0)
		if err != nil || state.BalanceMicro != 1000*store.MicroCreditsPerCent {
			t.Fatalf("credit after sweep=%+v err=%v", state, err)
		}
	}
	if err := restored.AsOperator().AcknowledgeRestore(t.Context()); err != nil {
		t.Fatal(err)
	}
	schedule, err := tenant.GetCronSchedule(t.Context(), "restore-nightly")
	if err != nil || schedule.CursorAt.Before(firedAfterBackup) || schedule.NextDueAt != nil {
		t.Fatalf("acknowledgement can replay an already fired instant: %+v %v", schedule, err)
	}
	// A backup can predate the reservation too; an owned payment must stop restore
	// for operator repair rather than report a successful sweep with missing money.
	checkoutID = "cs_lost_after_backup"
	if err := ReconcilePayments(t.Context(), verifier, "restore.test", restored.AsOperator()); err == nil {
		t.Fatal("owned payment without restored reservation was silently skipped")
	}
	checkoutID, unknownRefill = "", true
	for sweep := 0; sweep < 2; sweep++ {
		err := ReconcilePayments(t.Context(), verifier, "restore.test", restored.AsOperator())
		var refusal *apierr.Error
		if !errors.As(err, &refusal) || refusal.Code != apierr.Conflict || !strings.Contains(refusal.Message, "no restored team correlation") {
			t.Fatalf("owned succeeded refill needs explicit correlation refusal, got %v", err)
		}
	}

	for _, query := range []string{
		`SELECT COUNT(*) FROM payment_events WHERE object_id='pi_unbound'`,
		`SELECT COUNT(*) FROM payment_states WHERE payment_id='pi_unbound'`,
	} {
		var count int
		if err := teststore.SQL(t, restored).QueryRowContext(t.Context(), query).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("uncorrelated provider pull committed state: %s = %d", query, count)
		}
	}
}
