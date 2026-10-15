package acceptance_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/googleauth"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type identityLinkWriter struct {
	claim               capability.IdentityLink
	consumed, committed int
	failure             error
	profile             store.SignInProfile
	onConsume           func()
}

func (w *identityLinkWriter) ConsumeIdentityLink(_ context.Context, claim capability.IdentityLink) error {
	if w.failure != nil {
		return w.failure
	}
	if w.consumed != 0 {
		return &apierr.Error{Code: apierr.Forbidden, Message: "link already used"}
	}
	w.claim, w.consumed = claim, 1
	if w.onConsume != nil {
		w.onConsume()
	}
	return nil
}

func (w *identityLinkWriter) LinkAccountIdentity(_ context.Context, claim capability.IdentityLink, profile store.SignInProfile) (store.Identity, error) {
	if w.failure != nil {
		return store.Identity{}, w.failure
	}
	if w.consumed != 1 || w.claim != claim || w.committed != 0 || profile.Provider != claim.Provider || !profile.EmailVerified {
		return store.Identity{}, &apierr.Error{Code: apierr.Forbidden, Message: "invalid link"}
	}
	w.committed, w.profile = 1, profile
	return store.Identity{Provider: profile.Provider, Subject: profile.Subject}, nil
}

type identityLinkHTTP struct {
	oauthHTTP
	writer *identityLinkWriter
}

func newIdentityLinkHTTP(t *testing.T) identityLinkHTTP {
	t.Helper()
	f := newOAuthHTTP(t)
	if _, err := f.store.DB().ExecContext(t.Context(), `DELETE FROM identities WHERE provider='github'`); err != nil {
		t.Fatal(err)
	}
	writer := &identityLinkWriter{}
	f.server.WithIdentityLinkWriter(writer)
	f.handler = f.server.Handler()
	return identityLinkHTTP{oauthHTTP: f, writer: writer}
}

func (f *identityLinkHTTP) startRequest() *http.Request {
	r := f.request("POST", "/auth/github/link", url.Values{"csrf_token": {f.csrf}}.Encode())
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return r
}

func (f *identityLinkHTTP) start(t *testing.T) *http.Cookie {
	t.Helper()
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, f.startRequest())
	cookie := oauthCookie(w, "__Host-sw_oauth")
	if w.Code != 200 || cookie == nil {
		t.Fatalf("start HTTP %d %s", w.Code, w.Body.String())
	}
	raw, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	var flow map[string]string
	if err != nil || json.Unmarshal(raw, &flow) != nil || flow["mode"] != "link" {
		t.Fatalf("invalid link flow: %v", err)
	}
	f.provider.challenge = googleauth.CodeChallenge(flow["verifier"])
	f.oauthState = flow["state"]
	return cookie
}

func (f *identityLinkHTTP) ready(t *testing.T) *http.Cookie {
	t.Helper()
	cookie := f.start(t)
	w := f.callback(url.Values{"state": {f.oauthState}, "code": {f.provider.code}}, cookie)
	cookie = oauthCookie(w, "__Host-sw_oauth")
	if w.Code != 200 || cookie == nil || f.provider.calls != 0 {
		t.Fatalf("callback HTTP %d %s calls=%d", w.Code, w.Body.String(), f.provider.calls)
	}
	return cookie
}

func (f *identityLinkHTTP) complete(cookie *http.Cookie) *httptest.ResponseRecorder {
	r := f.request("GET", "/auth/github/link/complete", "")
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	return w
}

func otherLinkAccount(t *testing.T, f *identityLinkHTTP) (string, string) {
	t.Helper()
	for _, statement := range []string{
		`INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('stranger','stranger@example.test',1,1)`,
		`INSERT INTO memberships(team,account_id,role,created_at) VALUES ('team','stranger','reader',1)`,
	} {
		if _, err := f.store.DB().ExecContext(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	raw, csrf, _, err := f.store.CreateAccountSession(t.Context(), store.Account{ID: "stranger"}, "team", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return raw, csrf
}

func TestIdentityLinkHTTPCommitsVerifiedIdentityOnce(t *testing.T) {
	f := newIdentityLinkHTTP(t)
	before, expires := f.state(t)
	cookie := f.ready(t)
	w := f.complete(cookie)
	digest := sha256.Sum256([]byte(f.raw))
	claim := f.writer.claim
	if w.Code != 303 || w.Header().Get("Location") != "/account/sign-ins?linked=github" || f.writer.committed != 1 || f.writer.profile.Subject != "123" || claim.Authority != (capability.AuthorityRef{Kind: capability.AuthoritySession, ID: hex.EncodeToString(digest[:])}) || claim.AccountID != "member" || claim.Provider != "github" || claim.Nonce == "" || !claim.ExpiresAt.After(time.Now()) {
		t.Fatalf("HTTP %d %s claim=%+v", w.Code, w.Body.String(), claim)
	}
	after, renewed := f.state(t)
	if before != after || expires != renewed || oauthCookie(w, "__Host-sw_oauth").MaxAge != -1 {
		t.Fatal("link changed session lifetime or retained flow")
	}
	calls := f.provider.calls
	w = f.complete(cookie)
	assertEngineError(t, w, 403, apierr.Forbidden)
	if f.provider.calls != calls || f.writer.committed != 1 {
		t.Fatal("replay reached provider or committed again")
	}
	for _, secret := range []string{f.raw, f.csrf, f.provider.code, cookie.Value, "private-provider-access-token"} {
		if strings.Contains(f.logs.String(), secret) || strings.Contains(w.Body.String(), secret) {
			t.Fatal("link proof leaked")
		}
	}
}

func TestIdentityLinkHTTPProofProperty(t *testing.T) {
	f := newIdentityLinkHTTP(t)
	cookie := f.ready(t)
	other, otherCSRF, _, err := f.store.CreateAccountSession(t.Context(), store.Account{ID: "member"}, "team", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	stranger, strangerCSRF := otherLinkAccount(t, &f)
	property := func(input []byte, mutation uint8) bool {
		r := f.request("GET", "/auth/github/link/complete", "")
		flow := *cookie
		r.AddCookie(&flow)
		forged := "forged-" + hex.EncodeToString(input)
		switch mutation % 13 {
		case 0:
			r.Header.Set("Authorization", "Bearer "+forged)
		case 1:
			r.Header.Set("Cookie", "__Host-sw_session="+other+"; __Host-sw_csrf="+otherCSRF)
			r.AddCookie(&flow)
		case 2:
			r.AddCookie(&flow)
		case 3:
			r.Header.Del("Cookie")
		case 4:
			flow.Value = forged
			r.Header.Set("Cookie", "__Host-sw_session="+f.raw)
			r.AddCookie(&flow)
		case 5:
			r.AddCookie(&http.Cookie{Name: "__Host-sw_session", Value: forged})
		case 6:
			r.Header.Set("Authorization", "Session "+other)
		case 7:
			r.URL.Path = "/auth/google/link/complete"
		case 8, 9, 10, 11:
			raw, _ := base64.RawURLEncoding.DecodeString(flow.Value)
			var fields map[string]string
			if json.Unmarshal(raw, &fields) != nil {
				return false
			}
			switch mutation % 13 {
			case 8:
				fields["verifier"] = forged
			case 9:
				fields["state"] = forged
			case 10:
				fields["code"] = ""
			case 11:
				fields["mode"] = ""
			}
			raw, _ = json.Marshal(fields)
			flow.Value = base64.RawURLEncoding.EncodeToString(raw)
			r.Header.Set("Cookie", "__Host-sw_session="+f.raw)
			r.AddCookie(&flow)
		case 12:
			r.Header.Set("Cookie", "__Host-sw_session="+stranger+"; __Host-sw_csrf="+strangerCSRF)
			r.AddCookie(&flow)
		}
		w := httptest.NewRecorder()
		f.handler.ServeHTTP(w, r)
		return w.Code >= 400 && w.Code < 500 && f.writer.consumed == 0 && f.writer.committed == 0 && f.provider.calls == 0 && oauthCookie(w, "__Host-sw_oauth") == nil
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 130}); err != nil {
		t.Fatal(err)
	}
}

func TestIdentityLinkHTTPStartProofProperty(t *testing.T) {
	f := newIdentityLinkHTTP(t)
	before, expires := f.state(t)
	property := func(input []byte, mutation uint8) bool {
		r := f.startRequest()
		forged := "forged-" + hex.EncodeToString(input)
		switch mutation % 8 {
		case 0:
			r.Header.Set("Authorization", "Session "+f.raw)
		case 1:
			r.Header.Del("Origin")
		case 2:
			r.Header.Set("Origin", "https://foreign.test")
		case 3:
			r.AddCookie(&http.Cookie{Name: "__Host-sw_csrf", Value: forged})
		case 4:
			r.AddCookie(&http.Cookie{Name: "__Host-sw_session", Value: f.raw})
		case 5:
			r.Body = io.NopCloser(strings.NewReader(url.Values{"csrf_token": {f.csrf, forged}}.Encode()))
		case 6:
			r.Body = io.NopCloser(strings.NewReader(url.Values{"csrf_token": {f.csrf}, "account": {forged}}.Encode()))
		case 7:
			r.Header.Set("Cookie", "__Host-sw_session="+f.raw+"; __Host-sw_csrf="+forged)
		}
		w := httptest.NewRecorder()
		f.handler.ServeHTTP(w, r)
		after, renewed := f.state(t)
		return w.Code >= 400 && w.Code < 500 && oauthCookie(w, "__Host-sw_oauth") == nil && f.provider.calls == 0 && before == after && expires == renewed
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 80}); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"GET", "HEAD", "PUT", "DELETE", "OPTIONS"} {
		r := f.startRequest()
		r.Method = method
		w := httptest.NewRecorder()
		f.handler.ServeHTTP(w, r)
		assertEngineError(t, w, 405, apierr.MethodNotAllowed)
	}
}

func TestIdentityLinkHTTPStartLimitsAndExistingProvider(t *testing.T) {
	f := newIdentityLinkHTTP(t)
	for i := range 11 {
		w := httptest.NewRecorder()
		f.handler.ServeHTTP(w, f.startRequest())
		if i < 10 && w.Code != 200 {
			t.Fatalf("start %d HTTP %d %s", i, w.Code, w.Body.String())
		}
		if i == 10 {
			assertEngineError(t, w, 429, apierr.RateLimited)
			if w.Header().Get("Retry-After") != "60" {
				t.Fatal("missing retry delay")
			}
		}
	}
	other, csrf := otherLinkAccount(t, &f)
	r := f.startRequest()
	r.Header.Set("Cookie", "__Host-sw_session="+other+"; __Host-sw_csrf="+csrf)
	r.Body = io.NopCloser(strings.NewReader(url.Values{"csrf_token": {csrf}}.Encode()))
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("other account HTTP %d %s", w.Code, w.Body.String())
	}

	g := newIdentityLinkHTTP(t)
	if _, err := g.store.DB().ExecContext(t.Context(), `INSERT INTO identities(provider,subject,account_id,email,email_verified,created_at) VALUES ('github','123','member','member@example.test',1,1)`); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	g.handler.ServeHTTP(w, g.startRequest())
	assertEngineError(t, w, 409, apierr.Conflict)
	if oauthCookie(w, "__Host-sw_oauth") != nil || g.writer.consumed != 0 {
		t.Fatal("already-linked provider started a flow")
	}
}

func TestIdentityLinkHTTPRefusals(t *testing.T) {
	for _, failure := range []string{"missing", "backend", "revoked", "old", "parent", "consumed-race", "commit-race", "provider-rejected", "provider-unverified", "provider-unavailable"} {
		t.Run(failure, func(t *testing.T) {
			f := newIdentityLinkHTTP(t)
			cookie := f.ready(t)
			code, status := apierr.Forbidden, 403
			switch failure {
			case "missing":
				f.server.WithIdentityLinkWriter(nil)
				code, status = apierr.PolicyMissing, 500
			case "backend":
				f.writer.failure = errors.New("private-writer-diagnostic")
				code, status = apierr.Unavailable, 503
			case "revoked":
				if _, err := f.store.DB().ExecContext(t.Context(), `DELETE FROM sessions`); err != nil {
					t.Fatal(err)
				}
				code, status = apierr.Unauthenticated, 401
			case "old":
				if _, err := f.store.DB().ExecContext(t.Context(), `UPDATE sessions SET created_at=?`, time.Now().Add(-time.Hour).UnixNano()); err != nil {
					t.Fatal(err)
				}
			case "parent":
				if _, err := f.store.DB().ExecContext(t.Context(), `UPDATE sessions SET capability_prefix='missing'`); err != nil {
					t.Fatal(err)
				}
				code, status = apierr.Unauthenticated, 401
			case "consumed-race":
				f.writer.failure = &apierr.Error{Code: apierr.Forbidden, Message: "link authority ended"}
			case "commit-race":
				f.writer.onConsume = func() {
					f.writer.failure = &apierr.Error{Code: apierr.Unauthenticated, Message: "session ended during provider exchange"}
				}
				code, status = apierr.Unauthenticated, 401
			case "provider-rejected":
				f.provider.failure = "rejected"
				code, status = apierr.Unauthenticated, 401
			case "provider-unverified":
				f.provider.failure = "unverified"
			case "provider-unavailable":
				f.provider.failure = "unavailable"
				code, status = apierr.Unavailable, 503
			}
			f.handler = f.server.Handler()
			w := f.complete(cookie)
			assertEngineError(t, w, status, code)
			if f.writer.committed != 0 || strings.Contains(w.Body.String(), "private-") || strings.Contains(f.logs.String(), "private-provider-diagnostic") {
				t.Fatal("refused link committed or leaked diagnostics")
			}
		})
	}
}

func TestIdentityLinkHTTPProviderDiagnosticProperty(t *testing.T) {
	property := func(input []byte, shape uint8) bool {
		f := newIdentityLinkHTTP(t)
		cookie := f.ready(t)
		secret := "provider-private-" + hex.EncodeToString(input)
		failure := &apierr.Error{Code: apierr.Conflict, Message: secret, Hint: secret, Details: map[string]any{secret: secret}}
		switch shape % 3 {
		case 0:
			f.provider.transportError = failure
		case 1:
			f.provider.transportError = fmt.Errorf("provider: %w", failure)
		case 2:
			f.provider.transportError = errors.Join(errors.New(secret), failure)
		}
		w := f.complete(cookie)
		var envelope apierr.Envelope
		return w.Code == 503 && json.Unmarshal(w.Body.Bytes(), &envelope) == nil && envelope.Error != nil && envelope.Error.Code == apierr.Unavailable &&
			!strings.Contains(w.Body.String(), secret) && !strings.Contains(f.logs.String(), secret) && f.writer.committed == 0
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 30}); err != nil {
		t.Fatal(err)
	}
}

var _ controller.IdentityLinkWriter = (*identityLinkWriter)(nil)
