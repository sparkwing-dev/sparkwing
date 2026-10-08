package controller_test

import (
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestOAuthCallbackRefusesAFlowThisBrowserDidNotStart(t *testing.T) {
	f := newIdentityFixture(t)
	p := person("g-a", "a@example.com", "A")

	start := f.browserGet("/auth/google/start?next=%2Fcrons")
	flow := responseCookie(start, "__Host-sw_oauth")
	authorize, err := url.Parse(start.Header.Get("Location"))
	if flow == nil || err != nil {
		t.Fatalf("start = %d %q", start.StatusCode, start.Header.Get("Location"))
	}
	code := f.google.Code(p, flowVerifier(t, flow), dashRedirect)
	state := authorize.Query().Get("state")

	noFlow := f.browserGet("/auth/google/callback?" + url.Values{"code": {code}, "state": {state}}.Encode())
	if noFlow.StatusCode != http.StatusBadRequest || responseCookie(noFlow, "__Host-sw_session") != nil {
		t.Fatalf("callback without the flow cookie = %d, want 400 and no session", noFlow.StatusCode)
	}
	forged := f.browserGet("/auth/google/callback?"+url.Values{"code": {code}, "state": {"forged"}}.Encode(), flow)
	if forged.StatusCode != http.StatusBadRequest || responseCookie(forged, "__Host-sw_session") != nil {
		t.Fatalf("callback with a forged state = %d, want 400 and no session", forged.StatusCode)
	}
	for _, c := range forged.Cookies() {
		if c.Name == "__Host-sw_oauth" {
			t.Fatal("a forged callback discarded the flow this browser has in flight")
		}
	}
	other := f.browserGet("/auth/github/callback?"+url.Values{"code": {code}, "state": {state}}.Encode(), flow)
	if other.StatusCode != http.StatusBadRequest {
		t.Fatalf("a google flow finished at the github callback = %d, want 400", other.StatusCode)
	}
	declined := f.browserGet("/auth/google/callback?error=access_denied", flow)
	if declined.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a declined consent = %d, want 401", declined.StatusCode)
	}

	done := f.browserGet("/auth/google/callback?"+url.Values{"code": {code}, "state": {state}}.Encode(), flow)
	session := responseCookie(done, "__Host-sw_session")
	if done.StatusCode != http.StatusOK || session == nil {
		t.Fatalf("callback = %d, want 200 with a session", done.StatusCode)
	}
	if page := responseBody(t, done); !strings.Contains(page, `content="0;url=/crons"`) {
		t.Fatalf("signed-in page does not continue to next: %s", page)
	}
	if f.whoami(sessionAuth(session.Value)).Principal == "" {
		t.Fatal("the browser's new session does not authenticate")
	}
	replay := f.browserGet("/auth/google/callback?"+url.Values{"code": {code}, "state": {state}}.Encode(), flow)
	if replay.StatusCode == http.StatusOK {
		t.Fatal("a replayed code signed in again")
	}
}

func TestOAuthStartRefusesAnUnknownProvider(t *testing.T) {
	f := newIdentityFixture(t)
	if code := f.browserGet("/auth/okta/start").StatusCode; code != http.StatusNotFound {
		t.Fatalf("unknown provider = %d, want 404", code)
	}
}

func TestOAuthSignInAuditNamesTheAccount(t *testing.T) {
	logs := &syncBuffer{}
	raw, pub := multiTeamLicense(t)
	f := newIdentityFixtureWith(t, fixtureOpts{license: raw, key: pub, logger: slog.New(slog.NewTextHandler(logs, nil))})
	p := person("g-audit", "audit@example.com", "Audit")
	start := f.browserGet("/auth/google/start")
	flow := responseCookie(start, "__Host-sw_oauth")
	authorize, err := url.Parse(start.Header.Get("Location"))
	if flow == nil || err != nil {
		t.Fatalf("start = %d", start.StatusCode)
	}
	done := f.browserGet("/auth/google/callback?"+url.Values{
		"code":  {f.google.Code(p, flowVerifier(t, flow), dashRedirect)},
		"state": {authorize.Query().Get("state")},
	}.Encode(), flow)
	session := responseCookie(done, "__Host-sw_session")
	if done.StatusCode != http.StatusOK || session == nil {
		t.Fatalf("callback = %d", done.StatusCode)
	}
	var me exchangeBody
	if code := f.call("GET", "/api/v1/me", sessionAuth(session.Value), nil, &me); code != http.StatusOK {
		t.Fatalf("me = %d", code)
	}
	line := auditLine(logs.String(), done.Header.Get("X-Request-Id"))
	if !strings.Contains(line, "route=/auth/{provider}/callback") || !strings.Contains(line, "principal_id="+me.User.ID) {
		t.Fatalf("sign-in audit record does not name account %s: %q", me.User.ID, line)
	}
}
