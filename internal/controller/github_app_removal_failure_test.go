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
	if testing.Short() {
		t.Skip("slow: 1.0s measured in the short class; the full suite runs it")
	}
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, removed string
		withdraw      bool
		replay        bool
	}{
		{"named", `[{"id":701}]`, true, false},
		{"empty", `[]`, false, false},
		{"missing", `null`, false, false},
		{"invalid", `[{"id":0}]`, false, false},
		{"partly ambiguous", `[{"id":701},{"full_name":"acme/other"}]`, false, false},
		{"malformed", `{"id":701}`, false, false},
		{"replay after successful listing", `[{"id":701}]`, false, true},
		{"replay ambiguous after successful listing", `[]`, false, true},
		{"replay malformed after successful listing", `{"id":701}`, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := &testClock{}
			s, session := credentialSessionServer(t, clock.option())
			now := teststore.Now(s.store)
			clock.pin(func() time.Time { return now })
			engineSQL(t, s.store, `UPDATE memberships SET role='owner' WHERE team='a' AND account_id='member'`)
			unavailable := !tc.replay
			s.WithGitHubApp(githubapp.Config{AppID: 1, PrivateKey: key, WebhookSecret: "hook", HTTP: &http.Client{Transport: billingPageTransport(func(r *http.Request) (*http.Response, error) {
				status, body := http.StatusBadGateway, "unavailable"
				if r.URL.Path == "/app/installations/7/access_tokens" {
					status, body = http.StatusCreated, `{"token":"token"}`
				} else if r.URL.Path == "/installation/repositories" && !unavailable {
					status, body = http.StatusOK, `{"total_count":2,"repositories":[{"id":701},{"id":702}]}`
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})}})
			teststore.SeedGitHubInstallation(t, s.store, "a", store.GitHubAppInstallation{InstallationID: 7, AccountID: 42, AccountLogin: "acme", AccountType: "Organization"}, time.Now())
			for _, id := range []int{701, 702} {
				engineSQL(t, s.store, `INSERT INTO cron_schedules(id,repo_path,pipeline,cron,tz,overlap,catch_up_ns,armed_at,updated_at,cursor_at,team,repo_key,github_installation_id,github_repository_id) VALUES (?,?, 'build','* * * * *','UTC','skip',0,1,1,1,'a',?,7,?)`, fmt.Sprint(id), fmt.Sprintf("https://github.com/acme/r%d.git", id), fmt.Sprintf("gh:%d", id), id)
				engineSQL(t, s.store, `INSERT INTO github_app_automation(team,repository_id,repository,installation_id,enabled_by,enabled_at,repo_key) VALUES ('a',?,?,7,'member',1,?)`, id, fmt.Sprintf("acme/r%d", id), fmt.Sprintf("gh:%d", id))
			}
			body := fmt.Sprintf(`{"action":"removed","installation":{"id":7},"repositories_removed":%s}`, tc.removed)
			if err := s.operator.EnqueueGitHubDelivery(t.Context(), "removal", "installation_repositories", []byte(body), now); err != nil {
				t.Fatal(err)
			}
			var receipt string
			if tc.replay {
				engineSQL(t, s.store, `CREATE TRIGGER fail_delivery_completion BEFORE UPDATE ON github_app_deliveries WHEN NEW.state='done' BEGIN SELECT RAISE(ABORT,'crash before completion'); END`)
				if outcome, err := s.processNextGitHubDelivery(t.Context()); err == nil || outcome == nil || outcome.failureReason() != "" {
					t.Fatalf("completion failure: %+v, %v", outcome, err)
				}
				if err := teststore.SQL(t, s.store).QueryRow(`SELECT result FROM github_app_delivery_effects WHERE effect LIKE 'withdraw-installation:%'`).Scan(&receipt); err != nil {
					t.Fatal(err)
				}
				engineSQL(t, s.store, `DROP TRIGGER fail_delivery_completion`)
				now = now.Add(3 * time.Minute)
				if outcome, err := s.processNextGitHubDelivery(t.Context()); err != nil || outcome != nil {
					t.Fatalf("expired delivery retried automatically: %+v, %v", outcome, err)
				}
				r := httptest.NewRequest(http.MethodPost, "https://controller.test/api/v1/team/github-app/deliveries/removal/process-again", nil)
				r.Header.Set("Origin", "https://controller.test")
				r.Header.Set(csrfHeaderName, "session-csrf")
				r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
				r.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "session-csrf"})
				w := httptest.NewRecorder()
				s.Handler().ServeHTTP(w, r)
				if w.Code != http.StatusNoContent {
					t.Fatalf("process again: HTTP %d %s", w.Code, w.Body)
				}
				unavailable = true
			}
			outcome, err := s.processNextGitHubDelivery(t.Context())
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
			if (outcome.failureReason() == "") != (tc.withdraw || tc.replay) {
				t.Fatalf("removal failure reason = %q, withdrew = %t", outcome.failureReason(), tc.withdraw)
			}
			if tc.replay {
				var after string
				if err := teststore.SQL(t, s.store).QueryRow(`SELECT result FROM github_app_delivery_effects WHERE effect LIKE 'withdraw-installation:%'`).Scan(&after); err != nil || after != receipt {
					t.Fatalf("withdrawal receipt changed: %q -> %q, %v", receipt, after, err)
				}
			}
			r := httptest.NewRequest(http.MethodGet, "/api/v1/team/github-app/deliveries/failed", nil)
			r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, r)
			if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "removal") == (tc.withdraw || tc.replay) {
				t.Fatalf("admin failed deliveries: HTTP %d %s", w.Code, w.Body)
			}
		})
	}
}
