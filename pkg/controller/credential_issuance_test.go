package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func credentialSessionServer(t *testing.T) (*Server, string) {
	t.Helper()
	e, _ := engineRuntimeStore(t)
	s := e.Store
	engineSQL(t, s, `INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('member','member@example.test',1,1)`)
	engineSQL(t, s, `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('a','member','editor',1)`)
	raw := "private-account-session"
	digest := sha256.Sum256([]byte(raw))
	engineSQL(t, s, `INSERT INTO sessions(hash,principal,scopes,created_at,expires_at,team,account_id) VALUES (?,'member','runs.read,runs.write',1,?,'a','member')`, hex.EncodeToString(digest[:]), time.Now().Add(time.Hour).UnixNano())
	server := New(s, slog.New(slog.NewTextHandler(io.Discard, nil)))
	a := server.authorization
	a.Authenticator = capability.NewAuthenticator(s)
	a.CheckCSRF = func(r *http.Request, _ capability.Principal) error {
		if !validAPIRequestCSRF(r, "session-csrf", true) {
			return &apierr.Error{Code: apierr.Forbidden, Message: "invalid CSRF token"}
		}
		return nil
	}
	server.WithAuthorization(a)
	return server, raw
}

func credentialRequestHTTP(t *testing.T, handler http.Handler, raw, body string, bearer bool) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", "https://controller.test/api/v1/tokens", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if bearer {
		r.Header.Set("Authorization", "Bearer "+raw)
	} else {
		r.Header.Set("Origin", "https://controller.test")
		r.Header.Set(csrfHeaderName, "session-csrf")
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: raw})
		r.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "session-csrf"})
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestCredentialIssuancePersistsMemberRootAndDelegation(t *testing.T) {
	s, session := credentialSessionServer(t)
	h := s.Handler()
	var root, child credentialResponse
	w := credentialRequestHTTP(t, h, session, `{"grants":["runs.read"],"expires_at":"2100-01-01T00:00:00Z"}`, false)
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &root) != nil {
		t.Fatalf("root HTTP %d %s", w.Code, w.Body.String())
	}
	p, err := s.store.LookupCapability(t.Context(), sha256.Sum256([]byte(root.Capability)))
	if err != nil || p.Capability.Parent != nil || p.Capability.Subject != (capability.Subject{Kind: capability.SubjectAccount, ID: "member"}) || p.Capability.Team != "a" || p.Capability.ExpiresAt.Year() != 2100 {
		t.Fatalf("root=%+v err=%v", p, err)
	}
	w = credentialRequestHTTP(t, h, root.Capability, `{"grants":["runs.read"]}`, true)
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &child) != nil {
		t.Fatalf("child HTTP %d %s", w.Code, w.Body.String())
	}
	delegated, err := s.store.LookupCapability(t.Context(), sha256.Sum256([]byte(child.Capability)))
	if err != nil || delegated.Capability.Parent == nil || *delegated.Capability.Parent != p.Authority || !delegated.Capability.ExpiresAt.Equal(*p.Capability.ExpiresAt) {
		t.Fatalf("delegate=%+v err=%v", delegated, err)
	}
	w = credentialRequestHTTP(t, h, root.Capability, `{"grants":["runs.write"]}`, true)
	var failure apierr.Envelope
	if w.Code != 403 || json.Unmarshal(w.Body.Bytes(), &failure) != nil || failure.Error.Code != apierr.Forbidden {
		t.Fatalf("widened HTTP %d %s", w.Code, w.Body.String())
	}
	digest := sha256.Sum256([]byte(session))
	engineSQL(t, s.store, `UPDATE sessions SET revoked_at=1 WHERE hash=?`, hex.EncodeToString(digest[:]))
	if _, err := s.store.LookupCapability(t.Context(), sha256.Sum256([]byte(root.Capability))); err != nil {
		t.Fatalf("session revocation ended member root: %v", err)
	}
	tenant, err := s.store.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	if err := tenant.RevokeAuthority(t.Context(), store.Actor{Authority: p.Authority, CapabilityPrefix: root.Prefix}, p.Authority); err != nil {
		t.Fatal(err)
	}
	w = credentialRequestHTTP(t, h, child.Capability, `{"grants":["runs.read"]}`, true)
	if w.Code != 401 {
		t.Fatalf("revoked ancestor HTTP %d %s", w.Code, w.Body.String())
	}
}

type credentialRaceIssuer struct {
	*store.Store
	before func()
}

func (f credentialRaceIssuer) ProvisionCapability(ctx context.Context, spec capability.ProvisionSpec) (capability.Issued, error) {
	f.before()
	return f.Store.ProvisionCapability(ctx, spec)
}

func TestCredentialIssuanceRechecksMembershipInTransaction(t *testing.T) {
	for _, change := range []string{"removed", "demoted", "session revoked"} {
		t.Run(change, func(t *testing.T) {
			s, raw := credentialSessionServer(t)
			s.WithCredentialIssuer(credentialRaceIssuer{Store: s.store, before: func() {
				switch change {
				case "removed":
					engineSQL(t, s.store, `DELETE FROM memberships WHERE team='a' AND account_id='member'`)
				case "demoted":
					engineSQL(t, s.store, `UPDATE memberships SET role='reader' WHERE team='a' AND account_id='member'`)
				case "session revoked":
					engineSQL(t, s.store, `UPDATE sessions SET revoked_at=1 WHERE account_id='member'`)
				}
			}})
			w := credentialRequestHTTP(t, s.Handler(), raw, `{"grants":["runs.write"]}`, false)
			var result apierr.Envelope
			if json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Error == nil || change == "demoted" && (w.Code != 403 || result.Error.Code != apierr.Forbidden) || change != "demoted" && (w.Code != 401 || result.Error.Code != apierr.Unauthenticated) {
				t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
			}
			var count int
			if err := s.store.DB().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM capabilities`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("issued=%d err=%v", count, err)
			}
		})
	}
}
