package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"
)

var authTestBundle = fstest.MapFS{
	"index.html": &fstest.MapFile{Data: []byte("<html>dashboard</html>")},
}

func TestSameOriginRequestUsesCookiePolicyForOriginFormRequests(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		secureCookies bool
		origin        string
		want          bool
	}{
		{name: "secure accepts HTTPS", secureCookies: true, origin: "https://dashboard.example:8443", want: true},
		{name: "secure rejects HTTP", secureCookies: true, origin: "http://dashboard.example:8443"},
		{name: "insecure accepts HTTP", origin: "http://dashboard.example:8443", want: true},
		{name: "insecure rejects HTTPS", origin: "https://dashboard.example:8443"},
		{name: "rejects different host", secureCookies: true, origin: "https://attacker.example:8443"},
		{name: "rejects different port", secureCookies: true, origin: "https://dashboard.example:9443"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/runs", nil)
			req.Host = "dashboard.example:8443"
			req.Header.Set("Origin", test.origin)
			if req.URL.IsAbs() {
				t.Fatalf("test request URL = %q, want production origin form", req.URL)
			}
			if got := sameOriginRequestForCookiePolicy(req, test.secureCookies); got != test.want {
				t.Errorf("same-origin decision = %t, want %t", got, test.want)
			}
		})
	}
}

func TestLoginFormsRejectInvalidCSRFFirst(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	mutations := 0
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			mu.Lock()
			mutations++
			mu.Unlock()
		}
		if r.URL.Path == "/api/v1/auth/bootstrap-needed" {
			_ = json.NewEncoder(w).Encode(map[string]bool{"needed": false})
			return
		}
		http.Error(w, "must not reach controller", http.StatusTeapot)
	}))
	t.Cleanup(controller.Close)

	handler := HandlerFromOptionsWithBundle(HandlerOptions{
		ControllerURL: controller.URL,
		RequireLogin:  true,
	}, authTestBundle)
	tests := []struct {
		name   string
		path   string
		origin string
		cookie string
		form   string
	}{
		{name: "login missing provenance", path: "/login", cookie: "same", form: "same"},
		{name: "login cross origin", path: "/login", origin: "https://attacker.example", cookie: "same", form: "same"},
		{name: "login mismatched token", path: "/login", origin: "https://dashboard.example", cookie: "cookie", form: "form"},
		{name: "bootstrap missing token", path: "/login/bootstrap", origin: "https://dashboard.example"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			form := url.Values{
				"username":   {"admin"},
				"password":   {"correct-horse"},
				"csrf_token": {test.form},
			}
			req := httptest.NewRequest(http.MethodPost, "https://dashboard.example"+test.path, strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if test.origin != "" {
				req.Header.Set("Origin", test.origin)
			}
			if test.cookie != "" {
				req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: test.cookie})
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", rec.Code)
			}
		})
	}
	for range loginRateBurst + 1 {
		form := url.Values{"csrf_token": {"same"}}
		req := httptest.NewRequest(http.MethodPost, "https://dashboard.example/login", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", "https://attacker.example")
		req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "same"})
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("forged attempt status = %d, want 403 before rate limiting", rec.Code)
		}
	}
	mu.Lock()
	if mutations != 0 {
		mu.Unlock()
		t.Fatalf("invalid CSRF reached %d controller mutations", mutations)
	}
	mu.Unlock()

	validForm := url.Values{
		"username":   {"admin"},
		"password":   {"correct-horse"},
		"csrf_token": {"valid"},
	}
	valid := httptest.NewRequest(http.MethodPost, "https://dashboard.example/login", strings.NewReader(validForm.Encode()))
	valid.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	valid.Header.Set("Origin", "https://dashboard.example")
	valid.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "valid"})
	validRec := httptest.NewRecorder()
	handler.ServeHTTP(validRec, valid)
	if validRec.Code == http.StatusTooManyRequests {
		t.Fatal("forged CSRF attempts consumed the credential rate limit")
	}
	mu.Lock()
	defer mu.Unlock()
	if mutations != 1 {
		t.Fatalf("valid form reached %d controller mutations, want 1", mutations)
	}
}

func TestLogoutRequiresSessionBoundCSRFAndConfirmedRevocation(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	logoutStatus := http.StatusNoContent
	logoutCalls := 0
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/session":
			_ = json.NewEncoder(w).Encode(sessionResp{
				Principal: "admin",
				Scopes:    []string{"admin"},
				CSRFToken: "server-token",
				ExpiresAt: time.Now().Add(time.Hour).Unix(),
			})
		case "/api/v1/auth/logout":
			mu.Lock()
			logoutCalls++
			status := logoutStatus
			mu.Unlock()
			w.WriteHeader(status)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(controller.Close)
	handler := HandlerFromOptionsWithBundle(HandlerOptions{
		ControllerURL: controller.URL,
		RequireLogin:  true,
	}, authTestBundle)
	crossOrigin := httptest.NewRequest(http.MethodPost, "https://dashboard.example/logout", strings.NewReader("csrf_token=chosen"))
	crossOrigin.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	crossOrigin.Header.Set("Origin", "https://attacker.example")
	crossOrigin.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "chosen"})
	crossOriginRec := httptest.NewRecorder()
	handler.ServeHTTP(crossOriginRec, crossOrigin)
	if crossOriginRec.Code != http.StatusForbidden {
		t.Fatalf("cross-origin sessionless logout = %d, want 403", crossOriginRec.Code)
	}
	assertNoClearedCookies(t, crossOriginRec.Result().Cookies())

	request := func(token string) *httptest.ResponseRecorder {
		form := url.Values{"csrf_token": {token}}
		req := httptest.NewRequest(http.MethodPost, "https://dashboard.example/logout", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", "https://dashboard.example")
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "session-1"})
		req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: token})
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	mismatch := request("attacker-controlled")
	if mismatch.Code != http.StatusForbidden {
		t.Fatalf("controller-token mismatch = %d, want 403", mismatch.Code)
	}
	mu.Lock()
	if logoutCalls != 0 {
		mu.Unlock()
		t.Fatal("controller-token mismatch reached logout")
	}
	logoutStatus = http.StatusInternalServerError
	mu.Unlock()

	failed := request("server-token")
	if failed.Code != http.StatusBadGateway {
		t.Fatalf("failed controller logout = %d, want 502", failed.Code)
	}
	assertNoClearedCookies(t, failed.Result().Cookies())

	mu.Lock()
	logoutStatus = http.StatusNoContent
	mu.Unlock()
	success := request("server-token")
	if success.Code != http.StatusSeeOther || success.Header().Get("Location") != "/login" {
		t.Fatalf("successful logout = %d %q, want 303 /login", success.Code, success.Header().Get("Location"))
	}
	assertClearedSessionCookies(t, success.Result().Cookies())
}

func TestRedirectPreservesOneEncodedSameOriginTarget(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	redirectOrUnauth(rec, httptest.NewRequest(http.MethodGet, "https://dashboard.example/runs?run=x&tab=logs", nil))
	const want = "/login?next=%2Fruns%3Frun%3Dx%26tab%3Dlogs"
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != want {
		t.Fatalf("redirect = %d %q, want 303 %q", rec.Code, rec.Header().Get("Location"), want)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("redirect Cache-Control = %q, want no-store", rec.Header().Get("Cache-Control"))
	}
	location, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if got := location.Query().Get("next"); got != "/runs?run=x&tab=logs" {
		t.Fatalf("decoded next = %q", got)
	}
}

func TestAuthenticatedLoginRejectsExternalNext(t *testing.T) {
	t.Parallel()
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/auth/session" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(sessionResp{
			Principal: "admin",
			CSRFToken: "session-token",
			ExpiresAt: time.Now().Add(time.Hour).Unix(),
		})
	}))
	t.Cleanup(controller.Close)
	handler := HandlerFromOptionsWithBundle(HandlerOptions{ControllerURL: controller.URL, RequireLogin: true}, authTestBundle)
	req := httptest.NewRequest(http.MethodGet, "https://dashboard.example/login?next=https%3A%2F%2Fattacker.example", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "session-1"})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("authenticated malicious next = %d %q, want 303 /", rec.Code, rec.Header().Get("Location"))
	}
}

func TestLoginRequiredConfigurationFailsClosed(t *testing.T) {
	t.Parallel()
	tests := []HandlerOptions{
		{RequireLogin: true},
		{RequireLogin: true, AuthControllerURL: "controller.internal"},
		{RequireLogin: true, AuthControllerURL: "ftp://controller.internal"},
		{RequireLogin: true, AuthControllerURL: "https://user@controller.internal"},
		{RequireLogin: true, AuthControllerURL: "https://controller.internal?mode=session"},
	}
	for _, opts := range tests {
		handler := HandlerFromOptionsWithBundle(opts, authTestBundle)
		for _, path := range []string{"/", "/login", "/api/v1/runs", "/api/health"} {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			if rec.Code != http.StatusServiceUnavailable {
				t.Errorf("opts=%+v path=%s status=%d, want 503", opts, path, rec.Code)
			}
		}
	}

	valid := HandlerFromOptionsWithBundle(HandlerOptions{
		RequireLogin:      true,
		AuthControllerURL: "https://controller.internal",
	}, authTestBundle)
	rec := httptest.NewRecorder()
	valid.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("valid auth-only controller status = %d, want login redirect", rec.Code)
	}

	open := HandlerFromOptionsWithBundle(HandlerOptions{}, authTestBundle)
	rec = httptest.NewRecorder()
	open.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("login-free health status = %d, want 200", rec.Code)
	}
}

func TestUnsafeAPIProxyRequiresSessionBoundCSRF(t *testing.T) {
	t.Parallel()
	type upstreamRequest struct {
		path          string
		body          string
		authorization string
		cookie        string
		csrf          string
	}
	var mu sync.Mutex
	requests := []upstreamRequest{}
	recordMutation := func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		requests = append(requests, upstreamRequest{
			path:          r.URL.Path,
			body:          string(body),
			authorization: r.Header.Get("Authorization"),
			cookie:        r.Header.Get("Cookie"),
			csrf:          r.Header.Get(csrfHeaderName),
		})
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/session" {
			_ = json.NewEncoder(w).Encode(sessionResp{
				Principal: "admin",
				Scopes:    []string{"admin"},
				CSRFToken: "session-token",
				ExpiresAt: time.Now().Add(time.Hour).Unix(),
			})
			return
		}
		recordMutation(w, r)
	}))
	t.Cleanup(controller.Close)
	logs := httptest.NewServer(http.HandlerFunc(recordMutation))
	t.Cleanup(logs.Close)
	handler := HandlerFromOptionsWithBundle(HandlerOptions{
		ControllerURL: controller.URL,
		LogsURL:       logs.URL,
		Token:         "service-token",
		RequireLogin:  true,
	}, authTestBundle)

	request := func(path, origin, cookieToken, headerToken string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "https://dashboard.example.com"+path,
			strings.NewReader(`{"action":"cancel"}`))
		req.Header.Set("Content-Type", "text/plain")
		req.Header.Set("Origin", origin)
		req.Header.Set("Authorization", "Bearer browser-controlled")
		req.Header.Set("Proxy-Authorization", "browser-controlled")
		if headerToken != "" {
			req.Header.Set(csrfHeaderName, headerToken)
		}
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "session-1"})
		if cookieToken != "" {
			req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: cookieToken})
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	for _, test := range []struct {
		name        string
		origin      string
		cookieToken string
		headerToken string
	}{
		{name: "sibling subdomain text plain", origin: "https://attacker.example.com", cookieToken: "session-token", headerToken: "session-token"},
		{name: "scheme mismatch", origin: "http://dashboard.example.com", cookieToken: "session-token", headerToken: "session-token"},
		{name: "missing header", origin: "https://dashboard.example.com", cookieToken: "session-token"},
		{name: "header session mismatch", origin: "https://dashboard.example.com", cookieToken: "attacker-token", headerToken: "attacker-token"},
		{name: "cookie header mismatch", origin: "https://dashboard.example.com", cookieToken: "attacker-token", headerToken: "session-token"},
	} {
		t.Run(test.name, func(t *testing.T) {
			rec := request("/api/v1/runs/r1/cancel", test.origin, test.cookieToken, test.headerToken)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", rec.Code)
			}
		})
	}
	mu.Lock()
	if len(requests) != 0 {
		mu.Unlock()
		t.Fatalf("forged requests reached upstream: %+v", requests)
	}
	mu.Unlock()

	rec := request("/api/v1/runs/r1/cancel", "https://dashboard.example.com", "session-token", "session-token")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("legitimate mutation = %d, want 204", rec.Code)
	}
	if forged := request("/api/v1/logs/r1", "https://dashboard.example.com", "session-token", "session-token"); forged.Code != http.StatusNotFound {
		t.Fatalf("logs write = %d, want 404", forged.Code)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 1 {
		t.Fatalf("upstream requests = %+v, want the controller mutation only", requests)
	}
	for _, got := range requests {
		if got.body != `{"action":"cancel"}` || got.authorization != "Bearer service-token" ||
			got.cookie != "" || got.csrf != "" {
			t.Errorf("upstream credential boundary = %+v", got)
		}
	}
}

func TestSessionlessProxyPreservesDirectAuthorization(t *testing.T) {
	t.Parallel()
	var authorization, cookie, csrf string
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		cookie = r.Header.Get("Cookie")
		csrf = r.Header.Get(csrfHeaderName)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(controller.Close)
	handler := HandlerFromOptionsWithBundle(HandlerOptions{ControllerURL: controller.URL}, authTestBundle)
	req := httptest.NewRequest(http.MethodPost, "https://dashboard.example.com/api/v1/runs/r1/cancel", strings.NewReader("payload"))
	req.Header.Set("Origin", "https://attacker.example.com")
	req.Header.Set("Authorization", "Bearer direct-user-token")
	req.Header.Set(csrfHeaderName, "browser-token")
	req.AddCookie(&http.Cookie{Name: "unrelated", Value: "private"})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("sessionless mutation = %d, want 204", rec.Code)
	}
	if authorization != "Bearer direct-user-token" || cookie != "" || csrf != "" {
		t.Fatalf("sessionless upstream headers = Authorization %q Cookie %q CSRF %q", authorization, cookie, csrf)
	}
}

func TestGitcacheMachineProxyBypassesBrowserSessionAndPreservesBearer(t *testing.T) {
	t.Parallel()
	var authorization, proxyAuthorization, cookie, csrf, path string
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		proxyAuthorization = r.Header.Get("Proxy-Authorization")
		cookie = r.Header.Get("Cookie")
		csrf = r.Header.Get(csrfHeaderName)
		path = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(controller.Close)
	handler := HandlerFromOptionsWithBundle(HandlerOptions{
		ControllerURL: controller.URL,
		RequireLogin:  true,
	}, authTestBundle)
	req := httptest.NewRequest(http.MethodPost, "https://dashboard.example.com/api/v1/gitcache/seed", strings.NewReader("bundle"))
	req.Header.Set("Authorization", "Bearer swr_0123456789abcdef")
	req.Header.Set("Proxy-Authorization", "Bearer proxy-secret")
	req.Header.Set(csrfHeaderName, "browser-token")
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "browser-session"})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("machine proxy status = %d, want 204", rec.Code)
	}
	if path != "/api/v1/gitcache/seed" || authorization != "Bearer swr_0123456789abcdef" || proxyAuthorization != "" || cookie != "" || csrf != "" {
		t.Fatalf("machine proxy boundary = path %q Authorization %q Proxy-Authorization %q Cookie %q CSRF %q", path, authorization, proxyAuthorization, cookie, csrf)
	}
}

func TestGitcacheMachineProxyAllowsSlowPackStreamBeyondDefaultDeadline(t *testing.T) {
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "first")
		if flush, ok := w.(http.Flusher); ok {
			flush.Flush()
		}
		time.Sleep(60 * time.Millisecond)
		_, _ = io.WriteString(w, "second")
	}))
	defer controller.Close()
	dashboard := httptest.NewUnstartedServer(HandlerFromOptionsWithBundle(HandlerOptions{
		ControllerURL: controller.URL,
		RequireLogin:  true,
	}, authTestBundle))
	dashboard.Config.WriteTimeout = 20 * time.Millisecond
	dashboard.Start()
	defer dashboard.Close()
	req, err := http.NewRequest(http.MethodGet, dashboard.URL+"/api/v1/gitcache/git/widgets/info/refs?service=git-upload-pack", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer swr_0123456789abcdef")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "firstsecond" {
		t.Fatalf("body = %q", body)
	}
}

func TestSessionBackendFailuresRetainBrowserCookies(t *testing.T) {
	tests := []struct {
		name    string
		handler http.Handler
	}{
		{
			name: "controller 500",
			handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "temporary failure", http.StatusInternalServerError)
			}),
		},
		{
			name: "malformed 200",
			handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, "{")
			}),
		},
		{
			name:    "network failure",
			handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			logoutCalls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/auth/logout" {
					logoutCalls++
					w.WriteHeader(http.StatusNoContent)
					return
				}
				test.handler.ServeHTTP(w, r)
			}))
			if test.name == "network failure" {
				server.Close()
			} else {
				t.Cleanup(server.Close)
			}
			dashboard := HandlerFromOptionsWithBundle(HandlerOptions{
				ControllerURL: server.URL,
				RequireLogin:  true,
			}, authTestBundle)

			requests := []*http.Request{
				httptest.NewRequest(http.MethodGet, "https://dashboard.example.com/api/v1/runs", nil),
				httptest.NewRequest(http.MethodGet, "https://dashboard.example.com/login", nil),
			}
			logout := httptest.NewRequest(http.MethodPost, "https://dashboard.example.com/logout",
				strings.NewReader("csrf_token=session-token"))
			logout.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			logout.Header.Set("Origin", "https://dashboard.example.com")
			requests = append(requests, logout)
			for _, req := range requests {
				req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "session-1"})
				req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "session-token"})
				rec := httptest.NewRecorder()
				dashboard.ServeHTTP(rec, req)
				if rec.Code != http.StatusBadGateway {
					t.Errorf("%s %s status = %d, want 502", req.Method, req.URL.Path, rec.Code)
				}
				if rec.Header().Get("Cache-Control") != "no-store" {
					t.Errorf("%s %s Cache-Control = %q", req.Method, req.URL.Path, rec.Header().Get("Cache-Control"))
				}
				assertNoClearedCookies(t, rec.Result().Cookies())
			}
			if logoutCalls != 0 {
				t.Fatalf("session backend failure reached controller logout %d times", logoutCalls)
			}
		})
	}
}

func TestController401AuthoritativelyRevokesSession(t *testing.T) {
	t.Parallel()
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "invalid session", http.StatusUnauthorized)
	}))
	t.Cleanup(controller.Close)
	handler := HandlerFromOptionsWithBundle(HandlerOptions{
		ControllerURL: controller.URL,
		RequireLogin:  true,
	}, authTestBundle)
	req := httptest.NewRequest(http.MethodGet, "https://dashboard.example.com/api/v1/runs", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "revoked"})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("revoked session = %d, want 401", rec.Code)
	}
	assertClearedSessionCookies(t, rec.Result().Cookies())
}

func TestImmutableNextAssetsBypassSessionResolution(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	resolveCalls := 0
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/auth/session" {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		resolveCalls++
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(sessionResp{
			Principal: "admin",
			CSRFToken: "session-token",
			ExpiresAt: time.Now().Add(time.Hour).Unix(),
		})
	}))
	t.Cleanup(controller.Close)
	bundle := fstest.MapFS{
		"index.html":                 &fstest.MapFile{Data: []byte("<html>dashboard</html>")},
		"_next/static/chunks/app.js": &fstest.MapFile{Data: []byte("immutable")},
	}
	handler := HandlerFromOptionsWithBundle(HandlerOptions{
		ControllerURL: controller.URL,
		RequireLogin:  true,
	}, bundle)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(method, "/_next/static/chunks/app.js", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s immutable asset = %d, want 200", method, rec.Code)
		}
	}
	mu.Lock()
	if resolveCalls != 0 {
		mu.Unlock()
		t.Fatalf("immutable assets resolved session %d times", resolveCalls)
	}
	mu.Unlock()
	missing := httptest.NewRecorder()
	handler.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/_next/static/missing.js", nil))
	if missing.Code != http.StatusSeeOther || !strings.HasPrefix(missing.Header().Get("Location"), "/login?") {
		t.Fatalf("missing static asset = %d %q, want login redirect", missing.Code, missing.Header().Get("Location"))
	}

	page := httptest.NewRequest(http.MethodGet, "https://dashboard.example.com/", nil)
	page.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "session-1"})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, page)
	if rec.Code != http.StatusOK {
		t.Fatalf("authenticated HTML = %d, want 200", rec.Code)
	}
	mu.Lock()
	if resolveCalls != 1 {
		mu.Unlock()
		t.Fatalf("HTML session resolutions = %d, want 1", resolveCalls)
	}
	mu.Unlock()

	for _, test := range []struct {
		method string
		path   string
		want   bool
	}{
		{method: http.MethodGet, path: "/_next/static/chunks/app.js", want: true},
		{method: http.MethodHead, path: "/_next/static/chunks/app.js", want: true},
		{method: http.MethodGet, path: "/_next/static/missing.js"},
		{method: http.MethodPost, path: "/_next/static/chunks/app.js"},
		{method: http.MethodGet, path: "/_next/static/../runs.html"},
		{method: http.MethodGet, path: `/_next/static/..\runs.html`},
		{method: http.MethodGet, path: "/runs.html"},
	} {
		req := httptest.NewRequest(test.method, test.path, nil)
		if got := immutableStaticAssetRequest(req, bundle); got != test.want {
			t.Errorf("immutableStaticAssetRequest(%s, %s) = %t, want %t", test.method, test.path, got, test.want)
		}
	}
}

func assertNoClearedCookies(t *testing.T, cookies []*http.Cookie) {
	t.Helper()
	for _, cookie := range cookies {
		if (cookie.Name == sessionCookieName || cookie.Name == csrfCookieName) && cookie.MaxAge < 0 {
			t.Fatalf("failed logout cleared %s", cookie.Name)
		}
	}
}

func TestGitcacheMachineProxyRejectsARequestWithNoBearer(t *testing.T) {
	t.Parallel()
	reached := false
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(controller.Close)
	handler := HandlerFromOptionsWithBundle(HandlerOptions{
		ControllerURL: controller.URL,
		RequireLogin:  true,
	}, authTestBundle)

	for _, authorization := range []string{
		"", "Basic dXNlcjpwYXNz", "Bearer", "Bearer   ",
		"Bearer x", "Bearer swr_short", "Bearer nope_0123456789abcdef",
	} {
		req := httptest.NewRequest(http.MethodPost, "https://dashboard.example.com/api/v1/gitcache/seed", strings.NewReader("bundle"))
		if authorization != "" {
			req.Header.Set("Authorization", authorization)
		}
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "browser-session"})
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("Authorization %q = %d, want 401", authorization, rec.Code)
		}
	}
	if reached {
		t.Error("an unauthenticated request reached the controller")
	}
}

func TestGitcacheMachineProxyCapsConcurrentStreams(t *testing.T) {
	release := make(chan struct{})
	admitted := make(chan struct{}, gitcacheStreamLimit+1)
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		admitted <- struct{}{}
		<-release
		w.WriteHeader(http.StatusNoContent)
	}))
	defer controller.Close()
	dashboard := httptest.NewServer(HandlerFromOptionsWithBundle(HandlerOptions{
		ControllerURL: controller.URL,
		RequireLogin:  true,
	}, authTestBundle))
	defer dashboard.Close()
	releaseOnce := sync.OnceFunc(func() { close(release) })
	defer releaseOnce()

	stream := gitcacheStream(dashboard.URL)

	var wg sync.WaitGroup
	for range gitcacheStreamLimit {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := stream(); err != nil {
				t.Error(err)
			}
		}()
	}
	for range gitcacheStreamLimit {
		<-admitted
	}

	code, retryAfter, err := stream()
	if err != nil {
		t.Fatal(err)
	}
	if code != http.StatusServiceUnavailable {
		t.Errorf("stream past the cap = %d, want 503", code)
	}
	if retryAfter == "" {
		t.Error("503 past the cap carried no Retry-After")
	}
	releaseOnce()
	wg.Wait()
}

func TestGitcacheMachineProxyQueuesForAFreeStreamSlot(t *testing.T) {
	release := make(chan struct{})
	admitted := make(chan struct{}, gitcacheStreamLimit+1)
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		admitted <- struct{}{}
		<-release
		w.WriteHeader(http.StatusNoContent)
	}))
	defer controller.Close()
	dashboard := httptest.NewServer(HandlerFromOptionsWithBundle(HandlerOptions{
		ControllerURL: controller.URL,
		RequireLogin:  true,
	}, authTestBundle))
	defer dashboard.Close()
	releaseOnce := sync.OnceFunc(func() { close(release) })
	defer releaseOnce()

	stream := gitcacheStream(dashboard.URL)
	var wg sync.WaitGroup
	for range gitcacheStreamLimit {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := stream(); err != nil {
				t.Error(err)
			}
		}()
	}
	for range gitcacheStreamLimit {
		<-admitted
	}

	queued := make(chan int, 1)
	go func() {
		code, _, err := stream()
		if err != nil {
			t.Error(err)
		}
		queued <- code
	}()
	time.Sleep(50 * time.Millisecond)
	releaseOnce()
	if code := <-queued; code != http.StatusNoContent {
		t.Errorf("queued stream = %d, want 204 once a slot freed inside the wait", code)
	}
	wg.Wait()
}

func gitcacheStream(dashboardURL string) func() (int, string, error) {
	return func() (int, string, error) {
		req, err := http.NewRequest(http.MethodGet,
			dashboardURL+"/api/v1/gitcache/git/widgets/info/refs?service=git-upload-pack", nil)
		if err != nil {
			return 0, "", err
		}
		req.Header.Set("Authorization", "Bearer swr_0123456789abcdef")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0, "", err
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode, resp.Header.Get("Retry-After"), nil
	}
}
