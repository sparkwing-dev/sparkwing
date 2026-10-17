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
	now := time.Now().Truncate(time.Millisecond)
	if _, err := acme.TestOnlySeedGitHubInstallation(ctx, acmeInstallation(), now); err != nil {
		t.Fatal(err)
	}
	a := store.GitHubAppAutomation{RepositoryID: 701, Repository: "acme/widgets", InstallationID: 7, EnabledBy: "github-admin"}
	if err := other.PutAuthorizedGitHubAutomation(ctx, githubAppAuthorization(t, st, other), a); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("other team's consent = %v", err)
	}
	if err := acme.PutAuthorizedGitHubAutomation(ctx, githubAppAuthorization(t, st, acme), a); err != nil {
		t.Fatal(err)
	}
	first, err := acme.GitHubAppAutomation(ctx, 7, 701)
	if err != nil {
		t.Fatal(err)
	}
	if err := acme.PutAuthorizedGitHubAutomation(ctx, githubAppAuthorization(t, st, acme), a); err != nil {
		t.Fatal(err)
	}
	got, err := acme.GitHubAppAutomation(ctx, 7, 701)
	if err != nil || !got.Enabled || got.EnabledAt.Before(now) || !got.EnabledAt.Equal(first.EnabledAt) {
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
	if err := acme.DeleteAuthorizedGitHubAutomation(ctx, githubAppAuthorization(t, st, acme), 701); err != nil {
		t.Fatal(err)
	}
	if _, err := acme.GitHubAppAutomation(ctx, 7, 701); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("revoked consent = %v", err)
	}
	if err := acme.PutAuthorizedGitHubAutomation(ctx, githubAppAuthorization(t, st, acme), a); err != nil {
		t.Fatal(err)
	}
	if err := acme.UnbindAuthorizedGitHubInstallation(ctx, githubAppAuthorization(t, st, acme), 7); err != nil {
		t.Fatal(err)
	}
	list, err := acme.GitHubAppAutomations(ctx)
	if err != nil || len(list) != 0 {
		t.Fatalf("unbound grants = %+v,%v", list, err)
	}
}
