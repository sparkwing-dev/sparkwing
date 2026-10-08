package store_test

import (
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestGitHubAppAutomationConsentIsolationAndRevocation(t *testing.T) {
	st := storetest.Open(t)
	acme, other := teamHandle(t, st, "acme"), teamHandle(t, st, "other")
	ctx := t.Context()
	now := time.Now()
	if _, err := acme.BindGitHubAppInstallation(ctx, acmeInstallation(), now); err != nil {
		t.Fatal(err)
	}
	a := store.GitHubAppAutomation{RepositoryID: 701, Repository: "acme/widgets", InstallationID: 7, EnabledBy: "acct-1"}
	if err := other.PutGitHubAppAutomation(ctx, a, now); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("other team's consent = %v", err)
	}
	if err := acme.PutGitHubAppAutomation(ctx, a, now); err != nil {
		t.Fatal(err)
	}
	if err := acme.PutGitHubAppAutomation(ctx, a, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, err := acme.GitHubAppAutomation(ctx, 7, 701)
	if err != nil || !got.Enabled || !got.EnabledAt.Equal(now) {
		t.Fatalf("idempotent consent = %+v,%v", got, err)
	}
	if _, err := other.GitHubAppAutomation(ctx, 7, 701); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("other team read = %v", err)
	}
	if _, err := acme.GitHubAppAutomation(ctx, 8, 701); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("other installation read = %v", err)
	}
	if err := st.AsOperator().SetGitHubAppInstallationSuspended(ctx, 7, true, now); err != nil {
		t.Fatal(err)
	}
	if _, err := acme.GitHubAppAutomation(ctx, 7, 701); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("suspended consent = %v", err)
	}
	if err := st.AsOperator().SetGitHubAppInstallationSuspended(ctx, 7, false, now); err != nil {
		t.Fatal(err)
	}
	if _, err := acme.GitHubAppAutomation(ctx, 7, 701); err != nil {
		t.Fatalf("restored consent = %v", err)
	}
	if err := acme.DeleteGitHubAppAutomation(ctx, 701); err != nil {
		t.Fatal(err)
	}
	if _, err := acme.GitHubAppAutomation(ctx, 7, 701); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("revoked consent = %v", err)
	}
	if err := acme.PutGitHubAppAutomation(ctx, a, now); err != nil {
		t.Fatal(err)
	}
	if err := acme.UnbindGitHubAppInstallation(ctx, 7); err != nil {
		t.Fatal(err)
	}
	list, err := acme.GitHubAppAutomations(ctx)
	if err != nil || len(list) != 0 {
		t.Fatalf("unbound grants = %+v,%v", list, err)
	}
}

func TestV94GitHubAutomationUpgradesSQLite(t *testing.T) {
	assertAutomationUpgrade(t, storetest.NewSQLite(t))
}

func TestGitHubAppAutomationAdmissionChecksConsentGeneration(t *testing.T) {
	st := storetest.Open(t)
	acme := teamHandle(t, st, "acme")
	ctx, now := t.Context(), time.Now()
	if _, err := acme.BindGitHubAppInstallation(ctx, acmeInstallation(), now); err != nil {
		t.Fatal(err)
	}
	a := store.GitHubAppAutomation{RepositoryID: 701, Repository: "acme/widgets", InstallationID: 7, EnabledBy: "acct-1"}
	if err := acme.PutGitHubAppAutomation(ctx, a, now); err != nil {
		t.Fatal(err)
	}
	grant, err := acme.GitHubAppAutomation(ctx, 7, 701)
	if err != nil {
		t.Fatal(err)
	}
	if err := acme.DeleteGitHubAppAutomation(ctx, 701); err != nil {
		t.Fatal(err)
	}
	if err := acme.PutGitHubAppAutomation(ctx, a, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	trig := store.Trigger{ID: "revoked-plan", Pipeline: "deploy", GithubRepoID: 701, CreatedAt: now}
	run := store.Run{ID: trig.ID, Pipeline: trig.Pipeline, Status: "pending", CreatedAt: now, StartedAt: now}
	if err := acme.CreateGitHubAutomationTriggerWithRun(ctx, trig, run, grant); !errors.Is(err, store.ErrGitHubAppAutomationRevoked) {
		t.Fatalf("old generation admitted = %v", err)
	}
	if _, err := acme.GetRun(ctx, trig.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("refused plan left a run = %v", err)
	}
	if count, err := acme.CountPendingTriggers(ctx); err != nil || count != 0 {
		t.Fatalf("refused plan left %d triggers,%v", count, err)
	}
	grant, err = acme.GitHubAppAutomation(ctx, 7, 701)
	if err != nil {
		t.Fatal(err)
	}
	if err := acme.CreateGitHubAutomationTriggerWithRun(ctx, trig, run, grant); err != nil {
		t.Fatalf("current generation refused = %v", err)
	}
}

func TestV94GitHubAutomationUpgradesPostgres(t *testing.T) {
	assertAutomationUpgrade(t, storetest.NewPostgres(t))
}

func assertAutomationUpgrade(t *testing.T, target *storetest.Target) {
	t.Helper()
	st, err := target.TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{`DROP TABLE github_app_automation`, `DELETE FROM sparkwing_schema_version WHERE version >= 94`} {
		if _, err := st.DB().ExecContext(t.Context(), sql); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	up, err := target.TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = up.Close() }()
	acme := teamHandle(t, up, "acme")
	if _, err := acme.GitHubAppAutomations(t.Context()); err != nil {
		t.Fatalf("upgraded automation table: %v", err)
	}
	if version, err := up.CurrentSchemaVersion(t.Context()); err != nil || version != store.ExpectedSchemaVersion() {
		t.Fatalf("version=%d,%v", version, err)
	}
}
