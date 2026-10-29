package controller

import (
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/githubapp"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

// TestScheduleRepositoryNameIsGitHubsCanonicalName stores a schedule URL in another
// case than GitHub's and expects the fire-time lookup to report GitHub's owner/name,
// which is what case-sensitive cloud trust policies on the OIDC repository claim match.
func TestScheduleRepositoryNameIsGitHubsCanonicalName(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := credentialSessionServer(t)
	teststore.SeedGitHubInstallation(t, s.store, "a", store.GitHubAppInstallation{InstallationID: 7, AccountID: 70, AccountLogin: "acme", AccountType: "Organization", ConnectedBy: "member", GitHubUserID: 71}, time.Now())
	repo := `{"id":42,"full_name":"Acme/App","default_branch":"trunk","owner":{"id":70,"login":"Acme"}}`
	s.WithGitHubApp(githubapp.Config{AppID: 1, Slug: "test", ClientID: "id", ClientSecret: "secret", WebhookSecret: "webhook", PrivateKey: key, HTTP: &http.Client{Transport: intakeGitHubTransport(func(r *http.Request) (*http.Response, error) {
		var payload string
		switch strings.ToLower(r.URL.Path) {
		case "/repos/acme/app/installation":
			payload = `{"id":7,"app_id":1,"account":{"id":70,"login":"acme","type":"Organization"}}`
		case "/app/installations/7/access_tokens":
			payload = `{"token":"private","expires_at":"2100-01-01T00:00:00Z","permissions":{"metadata":"read"}}`
		case "/installation/repositories":
			payload = `{"total_count":1,"repositories":[` + repo + `]}`
		case "/repositories/42":
			payload = repo
		default:
			return nil, fmt.Errorf("unexpected GitHub request %s", r.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload))}, nil
	})}})
	got, err := s.githubScheduleRepository(t.Context(), store.CronSchedule{Team: "a", RepoPath: "https://github.com/ACME/app.git"})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != 42 || got.FullName != "Acme/App" || got.DefaultBranch != "trunk" {
		t.Fatalf("schedule repository = %+v, want GitHub's id 42, Acme/App and trunk", got)
	}
}
