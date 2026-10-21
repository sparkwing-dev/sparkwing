package controller

import (
	"context"
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
	livePath := filepath.Join(dir, "live.db")
	if _, err := teststore.SQL(t, s.store).ExecContext(t.Context(), `VACUUM INTO ?`, livePath); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	options := []store.Option{
		teststore.WithClock(func() time.Time { return now }),
		store.WithPolicies(func(*store.Store) (store.Policies, error) {
			return store.Policies{Intake: scheduleIntakePolicy{}}, nil
		}),
	}
	live, err := teststore.OpenExisting(livePath, append(options, store.WithCredentialEpoch(epoch))...)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := live.Close(); err != nil {
			t.Error(err)
		}
	}()
	if err := live.EnsureCredentialEpoch(t.Context()); err != nil {
		t.Fatal(err)
	}
	_, token, err := teststore.ProvisionCredential(t.Context(), live, capability.ProvisionSpec{Kind: capability.ProvisionBootstrap})
	if err != nil {
		t.Fatal(err)
	}
	liveTenant, err := live.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	schedule, _, err := liveTenant.ArmCronSchedule(t.Context(), store.CronSchedule{
		ID: "restore-nightly", RepoPath: "https://github.com/acme/repo.git", Pipeline: "nightly", Cron: "* * * * *", CatchUp: 24 * time.Hour,
		Where: store.CronWhereController, GitBranch: "main", LockedRef: "0123456789abcdef0123456789abcdef01234567",
		GitHubInstallationID: 7, GitHubRepositoryID: 701, ArmedBy: "owner",
	})
	if err != nil {
		t.Fatal(err)
	}
	engineSQL(t, live, `INSERT INTO github_app_installations(installation_id,team,account_id,account_login,account_type,created_at,updated_at) VALUES (7,'a',1,'acme','Organization',1,1)`)
	engineSQL(t, live, `INSERT INTO github_app_automation(team,repo_key,repository_id,repository,installation_id,enabled_by,enabled_at) VALUES ('a',?,701,'acme/repo',7,'owner',1)`, schedule.RepositoryKey)
	if status, err := live.AsOperator().RestoreStatus(t.Context()); err != nil || status.Pending {
		t.Fatalf("backup must be normally operating: %+v %v", status, err)
	}
	ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: token.Prefix}
	if _, err := live.AsOperator().ResolveAuthority(t.Context(), ref); err != nil {
		t.Fatal(err)
	}
	backupPath := filepath.Join(dir, "backup.db")
	if _, err := teststore.SQL(t, live).ExecContext(t.Context(), `VACUUM INTO ?`, backupPath); err != nil {
		t.Fatal(err)
	}
	firedAfterBackup := now.Add(time.Minute).Truncate(time.Minute)
	now = firedAfterBackup.Add(time.Second)
	if result, err := liveTenant.AdmitScheduledRun(t.Context(), schedule, firedAfterBackup); err != nil || result.RunID == "" {
		t.Fatalf("live cron admission: %+v %v", result, err)
	}
	if err := live.AsOperator().RevokeAuthority(t.Context(), store.Actor{Authority: ref}, ref); err != nil {
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

	if err := New(live, nil).WithPayments(verifier, live.AsOperator()).WithPaymentDeployment("restore.test").ReconcilePayments(t.Context()); err != nil {
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
	restored, err := teststore.OpenExisting(restoredPath, append(options, store.WithCredentialEpoch(restoredEpoch))...)
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
	if result, err := tenant.AdmitScheduledRun(t.Context(), schedule, firedAfterBackup); !errors.Is(err, store.ErrRestorePending) || result.RunID != "" {
		t.Fatalf("restored cron admission before acknowledgement: %+v %v", result, err)
	}
	state, err := tenant.CreditState(t.Context(), 0)
	if err != nil || state.BalanceMicro != 0 {
		t.Fatalf("restored backup already has post-backup payment: %+v %v", state, err)
	}
	for sweep := 0; sweep < 2; sweep++ {
		if err := restored.AsOperator().ReconcileRestore(t.Context(), func(ctx context.Context) error {
			return New(restored, nil).WithPayments(verifier, restored.AsOperator()).WithPaymentDeployment("restore.test").ReconcilePayments(ctx)
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
	schedule, err = tenant.GetCronSchedule(t.Context(), "restore-nightly")
	if err != nil || schedule.CursorAt.Before(firedAfterBackup) || schedule.NextDueAt != nil {
		t.Fatalf("acknowledgement can replay an already fired instant: %+v %v", schedule, err)
	}
	if result, err := tenant.AdmitScheduledRun(t.Context(), schedule, firedAfterBackup); !errors.Is(err, store.ErrRestorePending) || result.RunID != "" {
		t.Fatalf("acknowledgement admitted an already fired instant: %+v %v", result, err)
	}
	next := firedAfterBackup.Add(time.Minute)
	now = next.Add(time.Second)
	if result, err := tenant.AdmitScheduledRun(t.Context(), schedule, next); err != nil || result.RunID == "" || result.Replayed {
		t.Fatalf("future cron admission after acknowledgement: %+v %v", result, err)
	}
	if fires, err := tenant.ListCronFires(t.Context(), schedule.ID, 0); err != nil || len(fires) != 1 || !fires[0].DueAt.Equal(next) {
		t.Fatalf("restored cron fired a duplicate instant: %+v %v", fires, err)
	}
	// A backup can predate the reservation too; an owned payment must stop restore
	// for operator repair rather than report a successful sweep with missing money.
	checkoutID = "cs_lost_after_backup"
	if err := New(restored, nil).WithPayments(verifier, restored.AsOperator()).WithPaymentDeployment("restore.test").ReconcilePayments(t.Context()); err == nil {
		t.Fatal("owned payment without restored reservation was silently skipped")
	}
	checkoutID, unknownRefill = "", true
	for sweep := 0; sweep < 2; sweep++ {
		err := New(restored, nil).WithPayments(verifier, restored.AsOperator()).WithPaymentDeployment("restore.test").ReconcilePayments(t.Context())
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
