package controller_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func onboardingCookieRequest(t *testing.T, f *appFixture, who signedIn, method, path, body string) *http.Request {
	t.Helper()
	raw := strings.TrimPrefix(who.auth, "Session ")
	session, err := f.store.LookupSession(context.Background(), raw, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, "https://dash.example.com"+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "__Host-sw_session", Value: raw})
	req.AddCookie(&http.Cookie{Name: "__Host-sw_csrf", Value: session.CSRFToken})
	if method != http.MethodGet {
		req.Header.Set("Origin", "https://dash.example.com")
		req.Header.Set("X-CSRF-Token", session.CSRFToken)
	}
	return req
}

func TestGitHubOnboardingCookieSessionUsesTeamRole(t *testing.T) {
	f := newAppFixture(t)
	owner := f.ghUser(501, "olga")
	f.connect(owner, 501, 7, acmeAdmin)
	editor := f.user("browser-editor", "editor@example.com")
	reader := f.user("browser-reader", "reader@example.com")
	f.join(owner, editor, "editor@example.com", "editor")
	f.join(owner, reader, "reader@example.com", "reader")
	f.app.SetCommit("acme/widgets", "main", headSHA)
	f.app.SetFile("acme/widgets", headSHA, ".sparkwing/sparkwing.yaml", []byte(automationConfig))
	handler := f.srv.Handler()
	for _, who := range []signedIn{reader, editor, owner} {
		for _, path := range []string{"/api/v1/team/github-app/automation", "/api/v1/team/github-app/automation?repository=acme/widgets", "/api/v1/team/github-runners"} {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, onboardingCookieRequest(t, f, who, http.MethodGet, path, ""))
			if rec.Code != http.StatusOK {
				t.Fatalf("%s reads %s = %d %s", who.id, path, rec.Code, rec.Body)
			}
		}
	}
	for _, mutation := range []struct {
		method string
		path   string
		body   string
		want   int
	}{
		{http.MethodPut, "/api/v1/team/github-app/automation", `{"repository":"acme/widgets"}`, http.StatusOK},
		{http.MethodPost, "/api/v1/team/github-runners", `{"repository":"acme/widgets"}`, http.StatusCreated},
		{http.MethodDelete, "/api/v1/team/github-app/automation?repository_id=701", "", http.StatusNoContent},
		{http.MethodDelete, "/api/v1/team/github-runners/701", "", http.StatusNoContent},
	} {
		for _, who := range []signedIn{reader, editor, owner} {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, onboardingCookieRequest(t, f, who, mutation.method, mutation.path, mutation.body))
			want := http.StatusForbidden
			if who.id == owner.id {
				want = mutation.want
			}
			if rec.Code != want {
				t.Fatalf("%s %s %s = %d %s, want %d", who.id, mutation.method, mutation.path, rec.Code, rec.Body, want)
			}
		}
	}
}

func TestGitHubOnboardingCookieWritesRejectInvalidCSRF(t *testing.T) {
	f := newAppFixture(t)
	owner := f.ghUser(501, "olga")
	f.connect(owner, 501, 7, acmeAdmin)
	f.app.SetCommit("acme/widgets", "main", headSHA)
	f.app.SetFile("acme/widgets", headSHA, ".sparkwing/sparkwing.yaml", []byte(automationConfig))
	handler := f.srv.Handler()
	for _, mutation := range []struct{ method, path string }{
		{http.MethodPut, "/api/v1/team/github-app/automation"},
		{http.MethodPost, "/api/v1/team/github-runners"},
		{http.MethodDelete, "/api/v1/team/github-app/automation?repository_id=701"},
		{http.MethodDelete, "/api/v1/team/github-runners/701"},
	} {
		for name, spoil := range map[string]func(*http.Request){
			"missing header": func(r *http.Request) { r.Header.Del("X-CSRF-Token") },
			"forged header":  func(r *http.Request) { r.Header.Set("X-CSRF-Token", "forged") },
			"foreign origin": func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") },
			"planted cookie": func(r *http.Request) { plantCSRF(r, "planted"); r.Header.Set("X-CSRF-Token", "planted") },
		} {
			req := onboardingCookieRequest(t, f, owner, mutation.method, mutation.path, `{"repository":"acme/widgets"}`)
			spoil(req)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Errorf("%s %s with %s = %d %s, want browser write refusal", mutation.method, mutation.path, name, rec.Code, rec.Body)
			}
		}
	}
	if out := automationPreview(t, f, owner); out["enabled"] != false {
		t.Fatalf("refused writes enabled automation: %+v", out)
	}
	var bindings struct {
		Bindings []any `json:"bindings"`
	}
	if code := f.call(http.MethodGet, "/api/v1/team/github-runners", owner.auth, nil, &bindings); code != http.StatusOK || len(bindings.Bindings) != 0 {
		t.Fatalf("refused writes granted Actions permission = %d %+v", code, bindings)
	}
}
