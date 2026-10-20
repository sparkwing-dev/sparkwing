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
	"slices"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func credentialSessionServer(t *testing.T, opts ...store.Option) (*Server, string) {
	t.Helper()
	e, _ := engineRuntimeStore(t, opts...)
	s := e.Store
	engineSQL(t, s, `INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('member','member@example.test',1,1)`)
	engineSQL(t, s, `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('a','member','editor',1)`)
	raw := "private-account-session"
	digest := sha256.Sum256([]byte(raw))
	engineSQL(t, s, `INSERT INTO sessions(hash,principal,scopes,created_at,expires_at,team,account_id) VALUES (?,'member','runs.read,runs.write',?,?,'a','member')`, hex.EncodeToString(digest[:]), time.Now().UnixNano(), time.Now().Add(time.Hour).UnixNano())
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
	w := credentialRequestHTTP(t, h, session, `{"grants":["runs.read"]}`, false)
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &root) != nil {
		t.Fatalf("root HTTP %d %s", w.Code, w.Body.String())
	}
	p, err := s.store.LookupCapability(t.Context(), sha256.Sum256([]byte(root.Capability)))
	if err != nil || p.Capability.Parent == nil || p.Capability.Parent.Kind != capability.AuthoritySession || p.Capability.Subject != (capability.Subject{Kind: capability.SubjectAccount, ID: "member"}) || p.Capability.Team != "a" || p.Capability.ExpiresAt == nil || p.Capability.ExpiresAt.After(time.Now().Add(90*24*time.Hour)) {
		t.Fatalf("root=%+v err=%v", p, err)
	}
	w = credentialRequestHTTP(t, h, root.Capability, `{"grants":["runs.read"]}`, true)
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &child) != nil {
		t.Fatalf("child HTTP %d %s", w.Code, w.Body.String())
	}
	delegated, err := s.store.LookupCapability(t.Context(), sha256.Sum256([]byte(child.Capability)))
	if err != nil || delegated.Capability.Parent == nil || *delegated.Capability.Parent != p.Authority || delegated.Capability.ExpiresAt.After(*p.Capability.ExpiresAt) {
		t.Fatalf("delegate=%+v err=%v", delegated, err)
	}
	w = credentialRequestHTTP(t, h, root.Capability, `{"grants":["runs.write"]}`, true)
	var failure apierr.Envelope
	if w.Code != 403 || json.Unmarshal(w.Body.Bytes(), &failure) != nil || failure.Error.Code != apierr.Forbidden {
		t.Fatalf("widened HTTP %d %s", w.Code, w.Body.String())
	}
	digest := sha256.Sum256([]byte(session))
	engineSQL(t, s.store, `UPDATE sessions SET revoked_at=1 WHERE hash=?`, hex.EncodeToString(digest[:]))
	if _, err := s.store.LookupCapability(t.Context(), sha256.Sum256([]byte(root.Capability))); err == nil {
		t.Fatal("session revocation left its member credential live")
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
			if err := teststore.SQL(t, s.store).QueryRowContext(t.Context(), `SELECT COUNT(*) FROM capabilities`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("issued=%d err=%v", count, err)
			}
		})
	}
}

func TestCredentialIssuanceValidatesEveryGrant(t *testing.T) {
	s, raw := credentialSessionServer(t)
	h := s.Handler()
	for _, grant := range capability.Grants() {
		t.Run(string(grant), func(t *testing.T) {
			body, err := json.Marshal(credentialRequest{Grants: []capability.Grant{grant}})
			if err != nil {
				t.Fatal(err)
			}
			w := credentialRequestHTTP(t, h, raw, string(body), false)
			want := http.StatusForbidden
			if slices.Contains([]capability.Grant{capability.RunsRead, capability.RunsWrite, capability.RunsControl, capability.LogsRead, capability.ApprovalsWrite}, grant) {
				want = http.StatusCreated
			}
			if w.Code != want {
				t.Fatalf("HTTP %d %s, want %d", w.Code, w.Body, want)
			}
		})
	}
	for _, body := range []string{`{}`, `{"grants":null}`, `{"grants":[null]}`, `{"grants":["runs.read","runs.read"]}`, `{"grants":[" runs.read "]}`, `{"grants":[""]}`, `{"grants":["runs.read","jobs:write","runs.admin"]}`, `{"scopes":["runs.read"]}`} {
		w := credentialRequestHTTP(t, h, raw, body, false)
		var failure apierr.Envelope
		if w.Code != 400 || json.Unmarshal(w.Body.Bytes(), &failure) != nil || failure.Error == nil || failure.Error.Code != apierr.BadRequest {
			t.Fatalf("HTTP %d %s", w.Code, w.Body)
		}
	}
	w := credentialRequestHTTP(t, h, raw, `{"grants":[]}`, false)
	var result credentialResponse
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Grants == nil || len(result.Grants) != 0 {
		t.Fatalf("inert credential: %d %s", w.Code, w.Body)
	}
}

func TestCredentialIssuanceRefusedGrantProperty(t *testing.T) {
	s, raw := credentialSessionServer(t)
	h := s.Handler()
	property := func(input []byte, mixed bool) bool {
		grant := "private-" + hex.EncodeToString(input)
		grants := []string{grant}
		if mixed {
			grants = append(grants, "runs.read")
		}
		body, err := json.Marshal(map[string]any{"grants": grants})
		if err != nil {
			t.Fatal(err)
		}
		w := credentialRequestHTTP(t, h, raw, string(body), false)
		var count int
		if err := teststore.SQL(t, s.store).QueryRowContext(t.Context(), `SELECT COUNT(*) FROM capabilities`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		var failure apierr.Envelope
		return w.Code == 400 && json.Unmarshal(w.Body.Bytes(), &failure) == nil && failure.Error != nil && failure.Error.Code == apierr.BadRequest && count == 0 && !strings.Contains(w.Body.String(), grant)
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 128}); err != nil {
		t.Fatal(err)
	}
}
