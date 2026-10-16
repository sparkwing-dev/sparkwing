package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestGitHubAppRemovalRoutesUseTenantTransactions(t *testing.T) {
	s, session := credentialSessionServer(t)
	engineSQL(t, s.store, `UPDATE memberships SET role='owner' WHERE team='a' AND account_id='member'`)
	tenant, err := s.store.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.BindGitHubAppInstallation(t.Context(), store.GitHubAppInstallation{InstallationID: 7, AccountID: 1, AccountLogin: "acme", AccountType: "Organization"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(session))
	a := capability.Authorized{Principal: capability.Principal{Authority: capability.AuthorityRef{Kind: capability.AuthoritySession, ID: hex.EncodeToString(digest[:])}}, Target: capability.Target{Team: "a"}, Access: capability.AccessRule{Credential: capability.CredentialMember, Grants: []capability.Grant{capability.TeamAdmin}, Binding: capability.BindNone, Liveness: capability.LiveCredential}}
	if _, err := tenant.PutAuthorizedGitHubTrigger(t.Context(), a, store.GitHubAppTrigger{RepositoryID: 701, Repository: "acme/app", InstallationID: 7, Pipeline: "ci", Push: true}); err != nil {
		t.Fatal(err)
	}
	if err := tenant.PutAuthorizedGitHubAutomation(t.Context(), a, store.GitHubAppAutomation{RepositoryID: 701, Repository: "acme/app", InstallationID: 7}); err != nil {
		t.Fatal(err)
	}
	s.githubApp = &githubAppState{}
	handler := s.Handler()
	for _, path := range []string{"/api/v1/team/github-app/triggers?repository_id=701&pipeline=ci", "/api/v1/team/github-app/automation?repository_id=701", "/api/v1/team/github-app/installations/7"} {
		r := httptest.NewRequest(http.MethodDelete, "https://controller.test"+path, strings.NewReader(""))
		r.Header.Set("Origin", "https://controller.test")
		r.Header.Set(csrfHeaderName, "session-csrf")
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
		r.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "session-csrf"})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusNoContent {
			t.Fatalf("%s: HTTP %d %s", path, w.Code, w.Body)
		}
	}
	if _, err := tenant.GitHubAppInstallation(t.Context(), 7); err == nil {
		t.Fatal("installation still bound")
	}
	if rows, err := tenant.GitHubAppTriggers(t.Context()); err != nil || len(rows) != 0 {
		t.Fatalf("subscriptions=%v: %v", rows, err)
	}
	if rows, err := tenant.GitHubAppAutomations(t.Context()); err != nil || len(rows) != 0 {
		t.Fatalf("consents=%v: %v", rows, err)
	}
}
