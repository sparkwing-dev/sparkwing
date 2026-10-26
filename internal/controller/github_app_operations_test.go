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
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/webhooks/github-app", nil))
	if body := w.Body.String(); !strings.Contains(body, "GitHub App unavailable") || strings.Contains(body, "private") || strings.Contains(body, "4242") {
		t.Errorf("webhook exposes configuration: %s", body)
	}
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
	if body := w.Body.String(); strings.Contains(body, "GitHub App") || strings.Contains(body, "private") {
		t.Errorf("public health describes the GitHub App: %s", body)
	}
	if problems := strings.Join(OperatorHealth(t, s).Problems, ";"); !strings.Contains(problems, "GitHub App unavailable") || strings.Contains(problems, "private") || strings.Contains(problems, "4242") {
		t.Errorf("operator health exposes configuration: %s", problems)
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
			if problems := strings.Join(OperatorHealth(t, s).Problems, ";"); strings.Contains(problems, "remove the previous webhook secret after rotation") != (tc.previous != "") {
				t.Errorf("operator health: %s", problems)
			}
			w := httptest.NewRecorder()
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
