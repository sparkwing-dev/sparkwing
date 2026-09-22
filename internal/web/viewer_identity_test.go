package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
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
	}, "session-alice"))
	withViewerIdentity(newViewerTabs(), inner).ServeHTTP(httptest.NewRecorder(), req)
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
	withViewerIdentity(newViewerTabs(), inner).ServeHTTP(httptest.NewRecorder(),
		httptest.NewRequest(http.MethodGet, "/api/v1/runs/r1/logs", nil))
	if header != "" || ctx != "" {
		t.Fatalf("a sessionless request named %q / %q, want neither", header, ctx)
	}
}

// safety: an unvalidated tab id reaches an outgoing header, so a CR or an
// LF in one is a request-splitting bug, and an unbounded one is a way to
// spend the logs meter's memory.
func TestAnInvalidTabIDIsRefused(t *testing.T) {
	handler, seen := identityDashboard(t, "alice")
	for _, tab := range []string{
		"has space",
		"bad\r\nX-Injected: 1",
		"slash/es",
		"colon:s",
		strings.Repeat("a", MaxTabIDLen+1),
		"\n",
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, identityRequest("/api/v1/logs/run-1/node-a", tab))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("tab %q = %d, want 400", tab, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), TabHeaderName) {
			t.Errorf("tab %q: refusal %q does not name the header", tab, rec.Body.String())
		}
	}
	if got := seen(); len(got) != 0 {
		t.Fatalf("a refused tab still reached the logs service: %v", got)
	}

	for _, tab := range []string{"a", "tab-1", "A.b_c-9", strings.Repeat("a", MaxTabIDLen)} {
		if !ValidTabID(tab) {
			t.Errorf("ValidTabID(%q) = false, want true", tab)
		}
	}
	if ValidTabID("") {
		t.Error("ValidTabID(\"\") = true; an empty tab is absent, not valid")
	}
}

// safety: a viewer rotating tab ids must not mint an identity per request,
// because the logs meter tracks a bounded number of principals and a
// viewer who filled it would fold every other principal into one shared
// overflow budget.
func TestOneSessionCannotMintUnboundedIdentities(t *testing.T) {
	handler, seen := identityDashboard(t, "alice")
	for i := range 200 {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, identityRequest("/api/v1/logs/run-1/node-a", "tab-"+strconv.Itoa(i)))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("read %d = %d", i, rec.Code)
		}
	}
	distinct := map[string]bool{}
	for _, id := range seen() {
		distinct[id] = true
	}
	if len(distinct) > MaxTabsPerSession {
		t.Fatalf("200 rotated tabs produced %d identities, want at most %d", len(distinct), MaxTabsPerSession)
	}
	if len(distinct) < 2 {
		t.Fatalf("identities = %d; distinct tabs must still be counted apart", len(distinct))
	}
}

// safety: the slot a tab holds is stable while it keeps reading, so a tab
// does not lose its budget to its own next request.
func TestATabKeepsItsSlotWhileItReads(t *testing.T) {
	tabs := newViewerTabs()
	first := tabs.slot("alice", "tab-a")
	for range 50 {
		if got := tabs.slot("alice", "tab-a"); got != first {
			t.Fatalf("slot moved from %d to %d while the tab kept reading", first, got)
		}
	}
	if tabs.slot("alice", "tab-b") == first {
		t.Fatal("two live tabs share one slot")
	}
	if tabs.slot("bob", "tab-a") != 0 {
		t.Error("a second session did not start at its own first slot")
	}
}

func TestTheLeastRecentlySeenTabLosesItsSlot(t *testing.T) {
	tabs := newViewerTabs()
	stale := tabs.slot("alice", "tab-0")
	for i := 1; i < MaxTabsPerSession; i++ {
		tabs.slot("alice", "tab-"+strconv.Itoa(i))
	}
	// safety: touching every tab but the first leaves the first the oldest.
	// Reading its slot here would refresh it, so the slot it was given on
	// the way in is what the newcomer must take.
	for i := 1; i < MaxTabsPerSession; i++ {
		tabs.slot("alice", "tab-"+strconv.Itoa(i))
	}
	if got := tabs.slot("alice", "newcomer"); got != stale {
		t.Fatalf("the newcomer took slot %d, want the least recently seen %d", got, stale)
	}
	// safety: the evicted tab coming back is a newcomer in its turn.
	if got := tabs.slot("alice", "tab-0"); got == stale {
		t.Fatal("the evicted tab kept the slot the newcomer took")
	}
}

// safety: only the logs routes are metered per identity, so a controller
// route that gained a per-runner budget must never see the dashboard's
// own name on a viewer's request.
func TestTheControllerProxyCarriesNoRunnerIdentity(t *testing.T) {
	handler, seen := identityDashboard(t, "alice")
	handler.ServeHTTP(httptest.NewRecorder(), identityRequest("/api/v1/runs", "tab-1"))
	got := seen()
	if len(got) != 1 {
		t.Fatalf("forwarded %d requests, want 1", len(got))
	}
	if got[0] != "" {
		t.Fatalf("the controller proxy forwarded identity %q, want none", got[0])
	}
}

// safety: a client that names itself must not be believed; the dashboard
// decides whose budget a read spends.
func TestAnInboundRunnerIdentityIsStripped(t *testing.T) {
	handler, seen := identityDashboard(t, "alice")

	forged := identityRequest("/api/v1/logs/run-1/node-a", "")
	forged.Header.Set(store.RunnerIdentityHeader, "pool-runner-7")
	handler.ServeHTTP(httptest.NewRecorder(), forged)

	toController := identityRequest("/api/v1/runs", "")
	toController.Header.Set(store.RunnerIdentityHeader, "pool-runner-7")
	handler.ServeHTTP(httptest.NewRecorder(), toController)

	got := seen()
	if len(got) != 2 {
		t.Fatalf("forwarded %d requests, want 2", len(got))
	}
	if got[0] != ViewerIdentityPrefix+"alice" {
		t.Fatalf("the logs path forwarded %q, want the dashboard's own name", got[0])
	}
	if got[1] != "" {
		t.Fatalf("the controller path forwarded %q, want none", got[1])
	}
}
