package controller

import (
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/githubapp"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestGitHubAppRemovalListingFailureIsTargeted(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, removed string
		withdraw      bool
	}{
		{"named", `[{"id":701}]`, true},
		{"empty", `[]`, false},
		{"missing", `null`, false},
		{"invalid", `[{"id":0}]`, false},
		{"partly ambiguous", `[{"id":701},{"full_name":"acme/other"}]`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, session := credentialSessionServer(t)
			engineSQL(t, s.store, `UPDATE memberships SET role='owner' WHERE team='a' AND account_id='member'`)
			s.WithGitHubApp(githubapp.Config{AppID: 1, PrivateKey: key, HTTP: &http.Client{Transport: billingPageTransport(func(r *http.Request) (*http.Response, error) {
				status, body := http.StatusBadGateway, "unavailable"
				if r.URL.Path == "/app/installations/7/access_tokens" {
					status, body = http.StatusCreated, `{"token":"token"}`
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})}})
			teststore.SeedGitHubInstallation(t, s.store, "a", store.GitHubAppInstallation{InstallationID: 7, AccountID: 42, AccountLogin: "acme", AccountType: "Organization"}, time.Now())
			for _, id := range []int{701, 702} {
				engineSQL(t, s.store, `INSERT INTO cron_schedules(id,repo_path,pipeline,cron,tz,overlap,catch_up_ns,armed_at,updated_at,cursor_at,team,repo_key,github_installation_id,github_repository_id) VALUES (?,?, 'build','* * * * *','UTC','skip',0,1,1,1,'a',?,7,?)`, fmt.Sprint(id), fmt.Sprintf("https://github.com/acme/r%d.git", id), fmt.Sprintf("gh:%d", id), id)
				engineSQL(t, s.store, `INSERT INTO github_app_automation(team,repository_id,repository,installation_id,enabled_by,enabled_at,repo_key) VALUES ('a',?,?,7,'member',1,?)`, id, fmt.Sprintf("acme/r%d", id), fmt.Sprintf("gh:%d", id))
			}
			now := time.Now()
			body := fmt.Sprintf(`{"action":"removed","installation":{"id":7},"repositories_removed":%s}`, tc.removed)
			if err := s.operator.EnqueueGitHubDelivery(t.Context(), "removal", "installation_repositories", []byte(body), now); err != nil {
				t.Fatal(err)
			}
			outcome, err := s.processNextGitHubDelivery(t.Context(), now)
			if err != nil || outcome == nil {
				t.Fatalf("removal: %+v, %v", outcome, err)
			}
			for _, id := range []int{701, 702} {
				var declared, consent int
				if err := teststore.SQL(t, s.store).QueryRow(`SELECT declared FROM cron_schedules WHERE id=?`, fmt.Sprint(id)).Scan(&declared); err != nil {
					t.Fatal(err)
				}
				if err := teststore.SQL(t, s.store).QueryRow(`SELECT count(*) FROM github_app_automation WHERE repository_id=?`, id).Scan(&consent); err != nil {
					t.Fatal(err)
				}
				want := 1
				if id == 701 && tc.withdraw {
					want = 0
				}
				if declared != want || consent != want {
					t.Fatalf("repository %d: declared=%d consent=%d, want %d", id, declared, consent, want)
				}
			}
			if (outcome.failureReason() == "") != tc.withdraw {
				t.Fatalf("removal failure reason = %q, withdrew = %t", outcome.failureReason(), tc.withdraw)
			}
			r := httptest.NewRequest(http.MethodGet, "/api/v1/team/github-app/deliveries/failed", nil)
			r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, r)
			if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "removal") == tc.withdraw {
				t.Fatalf("admin failed deliveries: HTTP %d %s", w.Code, w.Body)
			}
		})
	}
}
