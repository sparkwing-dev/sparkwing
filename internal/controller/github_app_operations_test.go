package controller

import (
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/deployment"
	"github.com/sparkwing-dev/sparkwing/internal/githubapp"
	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestGitHubAppUnavailablePublicResponsesHideConfiguration(t *testing.T) {
	s := New(newStoreForAuth(t), nil).WithGitHubAppUnavailable(errors.New("read /private/credentials/app-4242: permission denied"))
	for _, path := range []string{"/api/v1/health", "/webhooks/github-app"} {
		method := http.MethodGet
		if path == "/webhooks/github-app" {
			method = http.MethodPost
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest(method, path, nil))
		body := w.Body.String()
		if !strings.Contains(body, "GitHub App unavailable") || strings.Contains(body, "private") || strings.Contains(body, "4242") {
			t.Errorf("%s exposes configuration: %s", path, body)
		}
	}
}

func TestWithGitHubAppRefusesEmptyWebhookSecret(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	s := New(newStoreForAuth(t), nil).WithGitHubApp(githubapp.Config{AppID: 1, PrivateKey: key})
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/webhooks/github-app", strings.NewReader(`{}`)))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("empty webhook secret: HTTP %d %s", w.Code, w.Body)
	}
}

func TestGitHubAppPreviousSecretWarnsUntilRemoved(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, previous string
		unavailable    bool
		emptyCurrent   bool
	}{
		{"available rotating", "old-secret", false, false},
		{"available rotated", "", false, false},
		{"unavailable rotating", "old-secret", true, false},
		{"unavailable rotated", "", true, false},
		{"missing current rotating", "old-secret", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := newStoreForAuth(t)
			s := New(st, nil)
			if tc.unavailable {
				cfg, err := deployment.Parse(deployment.Inputs{WebhookSecret: []byte("current"), PreviousWebhookSecret: []byte(tc.previous)})
				if err != nil {
					t.Fatal(err)
				}
				s = NewDeployment(st, cfg, nil).WithGitHubAppUnavailable(errors.New("private key is malformed"))
			} else {
				current := "current"
				if tc.emptyCurrent {
					current = ""
				}
				s.WithGitHubApp(githubapp.Config{AppID: 1, PrivateKey: key, WebhookSecret: current, PreviousWebhookSecret: tc.previous})
			}
			s.WithLocalSessions(t.TempDir())
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
			if strings.Contains(w.Body.String(), "remove the previous webhook secret after rotation") != (tc.previous != "") {
				t.Errorf("health: %s", w.Body)
			}
			w = httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/api/v1/local/doctor", strings.NewReader(`{"dry_run":true}`))
			r.Header.Set("Content-Type", "application/json")
			r = r.WithContext(localhostwire.WithLocalPeer(r.Context(), localhostwire.LocalPeerIdentity{}))
			a := capability.Authorized{Principal: capability.Principal{Class: capability.CredentialPeer}, Target: capability.Target{Team: string(store.DefaultTeam)}}
			if err := s.localDoctor(w, r, a); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(w.Body.String(), "remove the previous webhook secret after rotation") != (tc.previous != "") {
				t.Errorf("doctor: %s", w.Body)
			}
		})
	}
}
