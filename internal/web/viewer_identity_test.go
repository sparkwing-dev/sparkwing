package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/logs"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// safety: the fake serves one session and records the runner identity the
// dashboard puts on whatever it forwards to the logs service.
func identityDashboard(t *testing.T, principal string) (http.Handler, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/session" {
			_ = json.NewEncoder(w).Encode(sessionResp{
				Principal: principal,
				Scopes:    []string{controller.ScopeLogsRead, controller.ScopeRunsRead},
				CSRFToken: proxyTestCSRF,
				ExpiresAt: time.Now().Add(time.Hour).Unix(),
			})
			return
		}
		mu.Lock()
		seen = append(seen, r.Header.Get(store.RunnerIdentityHeader))
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(upstream.Close)
	handler := HandlerFromOptionsWithBundle(HandlerOptions{
		ControllerURL: upstream.URL,
		LogsURL:       upstream.URL,
		Token:         "service-token",
		RequireLogin:  true,
	}, authTestBundle)
	return handler, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string{}, seen...)
	}
}

func identityRequest(path, tab string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "https://dashboard.example.com"+path, strings.NewReader(""))
	req.Header.Set("Origin", "https://dashboard.example.com")
	req.Header.Set(csrfHeaderName, proxyTestCSRF)
	if tab != "" {
		req.Header.Set(TabHeaderName, tab)
	}
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "session-1"})
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: proxyTestCSRF})
	return req
}

// safety: the dashboard serves every browser tab through one token, so
// without a per-viewer identity the logs service counts every viewer's
// reads against the dashboard and one viewer spends the deployment's
// whole stream cap.
func TestDashboardNamesTheViewerOnForwardedLogReads(t *testing.T) {
	handler, seen := identityDashboard(t, "alice")

	handler.ServeHTTP(httptest.NewRecorder(), identityRequest("/api/v1/logs/run-1/node-a", ""))
	got := seen()
	if len(got) != 1 {
		t.Fatalf("forwarded %d requests, want 1", len(got))
	}
	if want := ViewerIdentityPrefix + "alice"; got[0] != want {
		t.Fatalf("forwarded identity = %q, want %q", got[0], want)
	}
}

// safety: one viewer's several tabs each get their own slots, so opening
// a second log view does not refuse the first.
func TestDashboardCountsEachTabApart(t *testing.T) {
	handler, seen := identityDashboard(t, "alice")

	handler.ServeHTTP(httptest.NewRecorder(), identityRequest("/api/v1/logs/run-1/node-a/stream", "tab-1"))
	handler.ServeHTTP(httptest.NewRecorder(), identityRequest("/api/v1/logs/run-1/node-a/stream", "tab-2"))
	got := seen()
	if len(got) != 2 {
		t.Fatalf("forwarded %d requests, want 2", len(got))
	}
	if got[0] == got[1] {
		t.Fatalf("both tabs forwarded the identity %q; each tab holds its own slots", got[0])
	}
	for _, id := range got {
		if !strings.HasPrefix(id, ViewerIdentityPrefix+"alice/") {
			t.Errorf("identity %q is not this viewer's", id)
		}
	}
}

// safety: two viewers must not share one budget, which is the whole point
// of deriving the identity from the session.
func TestTwoViewersAreCountedApart(t *testing.T) {
	alice, aliceSeen := identityDashboard(t, "alice")
	bob, bobSeen := identityDashboard(t, "bob")

	alice.ServeHTTP(httptest.NewRecorder(), identityRequest("/api/v1/logs/run-1/node-a", ""))
	bob.ServeHTTP(httptest.NewRecorder(), identityRequest("/api/v1/logs/run-1/node-a", ""))

	a, b := aliceSeen(), bobSeen()
	if len(a) != 1 || len(b) != 1 {
		t.Fatalf("forwarded %d and %d requests, want one each", len(a), len(b))
	}
	if a[0] == b[0] {
		t.Fatalf("both viewers forwarded %q; a shared identity shares one cap", a[0])
	}
}

// safety: the backend's own log routes reach the logs service through
// logs.Client rather than the proxy, so the identity has to ride the
// context as well as the header.
func TestTheViewerIdentityAlsoRidesTheContext(t *testing.T) {
	var got string
	inner := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = logs.ReaderIdentityFromContext(r.Context())
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/r1/logs", nil)
	req = req.WithContext(contextWithWebPrincipal(req.Context(), &sessionResp{
		Principal: "alice",
		ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}))
	withViewerIdentity(inner).ServeHTTP(httptest.NewRecorder(), req)
	if want := ViewerIdentityPrefix + "alice"; got != want {
		t.Fatalf("context identity = %q, want %q", got, want)
	}
}

// safety: a request with no session carries no identity rather than a
// made-up one, so an unauthenticated path cannot claim someone's budget.
func TestNoSessionNamesNoViewer(t *testing.T) {
	var header, ctx string
	inner := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		header = r.Header.Get(store.RunnerIdentityHeader)
		ctx = logs.ReaderIdentityFromContext(r.Context())
	})
	withViewerIdentity(inner).ServeHTTP(httptest.NewRecorder(),
		httptest.NewRequest(http.MethodGet, "/api/v1/runs/r1/logs", nil))
	if header != "" || ctx != "" {
		t.Fatalf("a sessionless request named %q / %q, want neither", header, ctx)
	}
}
