package controller_test

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

var pageBundle = fstest.MapFS{
	"index.html":          &fstest.MapFile{Data: []byte(`<html><script>boot()</script>dashboard shell</html>`)},
	"favicon.ico":         &fstest.MapFile{Data: []byte("icon")},
	"_next/static/app.js": &fstest.MapFile{Data: []byte("export {};")},
}

func (f *browserFixture) get(path string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	f.t.Helper()
	req := httptest.NewRequest(http.MethodGet, dashOrigin+path, nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	return f.serve(req)
}

func TestBrowserPagesNeedASessionWhenTheControllerAuthenticates(t *testing.T) {
	f := newBrowserFixture(t, &controller.Dashboard{Bundle: pageBundle})
	for _, path := range []string{"/", "/runs?run=r1", "/docs"} {
		rec := f.get(path)
		if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/login?next=") {
			t.Errorf("signed-out %s = %d %q, want a redirect to sign in", path, rec.Code, rec.Header().Get("Location"))
		}
	}
	for _, path := range []string{"/favicon.ico", "/_next/static/app.js"} {
		if rec := f.get(path); rec.Code != http.StatusOK {
			t.Errorf("signed-out %s = %d, want the public asset", path, rec.Code)
		}
	}
	session := &http.Cookie{Name: "__Host-sw_session", Value: f.sessID}
	rec := f.get("/runs", session)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "dashboard shell") {
		t.Fatalf("signed-in page = %d %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `<script nonce="`) {
		t.Fatalf("the page's inline script carries no nonce: %s", rec.Body)
	}
	stale := f.get("/", &http.Cookie{Name: "__Host-sw_session", Value: "swses_gone"})
	if stale.Code != http.StatusSeeOther || !clearsCookie(stale, "__Host-sw_session") {
		t.Fatalf("stale cookie = %d %v", stale.Code, stale.Header().Values("Set-Cookie"))
	}
}

func TestBrowserPagesLeaveTheAPIsOwnRoutesToIt(t *testing.T) {
	f := newBrowserFixture(t, &controller.Dashboard{Bundle: pageBundle})
	rec := f.get("/api/v1/no-such-route", &http.Cookie{Name: "__Host-sw_session", Value: f.sessID})
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"unsupported"`) {
		t.Fatalf("unknown API route = %d %s, want the API's 404", rec.Code, rec.Body)
	}
	post := httptest.NewRequest(http.MethodPost, dashOrigin+"/webhooks/github-app", strings.NewReader("{}"))
	if rec := f.serve(post); rec.Code == http.StatusSeeOther || strings.Contains(rec.Body.String(), "dashboard shell") {
		t.Fatalf("webhook delivery reached the pages: %d", rec.Code)
	}
	if rec := f.serve(httptest.NewRequest(http.MethodPost, dashOrigin+"/runs", nil)); rec.Code != http.StatusSeeOther && rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST to a page = %d", rec.Code)
	}
}

func TestBrowserPagesOnAnOpenControllerNeedNoSignIn(t *testing.T) {
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := controller.New(st, nil).WithDashboard(controller.Dashboard{Bundle: pageBundle})
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, dashOrigin+"/", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "dashboard shell") {
		t.Fatalf("open controller page = %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, dashOrigin+"/operator", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("operator console on an open controller with no session = %d, want 403", rec.Code)
	}
}

func TestBrowserPagesWithoutABundleNameTheBuildStep(t *testing.T) {
	f := newBrowserFixture(t, &controller.Dashboard{})
	rec := f.get("/", &http.Cookie{Name: "__Host-sw_session", Value: f.sessID})
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "build-web.sh") {
		t.Fatalf("missing bundle = %d %s", rec.Code, rec.Body)
	}
	if rec := f.get("/login"); rec.Code != http.StatusOK {
		t.Fatalf("sign-in page without a bundle = %d, want 200", rec.Code)
	}
}

func TestOperatorPageServesOnlyTheOperatorsSession(t *testing.T) {
	f, operator, owner := operatorFixture(t)
	page := func(auth string) int {
		return f.browserSend("GET", auth, "/operator", nil).StatusCode
	}
	if code := page(owner.auth); code != http.StatusForbidden {
		t.Errorf("a team owner's /operator = %d, want 403", code)
	}
	if code := page(operator.auth); code == http.StatusForbidden || code == http.StatusSeeOther {
		t.Errorf("the operator's /operator = %d, want the page", code)
	}
	admin := newBrowserFixture(t, &controller.Dashboard{Bundle: pageBundle})
	if rec := admin.get("/operator", &http.Cookie{Name: "__Host-sw_session", Value: admin.sessID}); rec.Code != http.StatusForbidden {
		t.Errorf("an admin password session's /operator = %d, want 403", rec.Code)
	}
}

func TestBrowserLocalModeServesPagesWithoutSignIn(t *testing.T) {
	f := newBrowserFixture(t, &controller.Dashboard{Local: true, Bundle: pageBundle})
	if rec := f.get("/"); rec.Code != http.StatusOK {
		t.Fatalf("local page = %d", rec.Code)
	}
	if rec := f.get("/sparkwing-runtime.js"); !strings.Contains(rec.Body.String(), `__SPARKWING_REQUIRE_LOGIN__="false"`) {
		t.Fatalf("local runtime config = %s", rec.Body)
	}
	login := httptest.NewRequest(http.MethodPost, dashOrigin+"/login", strings.NewReader("username=alice&password=correct-horse-battery"))
	login.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if rec := f.serve(login); setCookie(rec, "__Host-sw_session") != nil || setCookie(rec, "sw_session") != nil {
		t.Fatal("local mode signed a browser in")
	}
}

func TestDashboardReadsStayInsideTheTeamBoundary(t *testing.T) {
	f := newIdentityFixture(t)
	a := f.user("g-a", "a@example.com")
	b := f.user("g-b", "b@example.com")
	ctx := context.Background()
	team, err := f.store.ForTeam(ctx, store.Team(a.team))
	if err != nil {
		t.Fatal(err)
	}
	if err := team.CreateRun(ctx, store.Run{ID: "run-a", Pipeline: "demo", Status: "success", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/runs/run-a/logs", "/api/v1/runs/run-a/logs/n1/completeness", "/api/v1/runs/run-a/events/stream"} {
		if code := f.call("GET", path, b.auth, nil, nil); code != http.StatusNotFound {
			t.Errorf("another team's %s = %d, want 404", path, code)
		}
	}
	if code := f.call("GET", "/api/v1/runs/run-a/logs", a.auth, nil, nil); code != http.StatusOK {
		t.Errorf("the owning team's logs = %d, want 200", code)
	}
	var grep struct {
		RunsMatching int `json:"runs_matching"`
	}
	if code := f.call("GET", "/api/v1/runs/grep?q=x", b.auth, nil, &grep); code != http.StatusOK || grep.RunsMatching != 0 {
		t.Errorf("another team's grep = %d over %d runs, want none of team a's", code, grep.RunsMatching)
	}
	if code := f.call("GET", "/api/v1/capacity/profiles", a.auth, nil, nil); code != http.StatusNotImplemented {
		t.Errorf("hosted capacity profiles = %d, want 501", code)
	}
}

func TestDashboardLogReadsCarryTheCallersCredential(t *testing.T) {
	var mu sync.Mutex
	var got []string
	logsSvc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, r.Header.Get("Authorization")+"|"+r.Header.Get("X-Sparkwing-Runner"))
		mu.Unlock()
		_, _ = w.Write([]byte(`{"msg":"from the logs service"}` + "\n"))
	}))
	t.Cleanup(logsSvc.Close)
	f := newBrowserFixture(t, &controller.Dashboard{})
	f.srv.WithLogsURL(logsSvc.URL)
	f.h = f.srv.Handler()
	if err := f.st.CreateRun(context.Background(), store.Run{ID: "r1", Pipeline: "demo", Status: "success", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	rec := f.get("/api/v1/runs/r1/logs/n1", &http.Cookie{Name: "__Host-sw_session", Value: f.sessID})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "from the logs service") {
		t.Fatalf("cookie log read = %d %s", rec.Code, rec.Body)
	}
	bearer := httptest.NewRequest(http.MethodGet, dashOrigin+"/api/v1/runs/r1/logs/n1", nil)
	bearer.Header.Set("Authorization", "Bearer "+f.token)
	if rec := f.serve(bearer); rec.Code != http.StatusOK {
		t.Fatalf("bearer log read = %d", rec.Code)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{"Session " + f.sessID + "|dashboard:alice", "Bearer " + f.token + "|"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("logs service saw %q, want %q", got, want)
	}
}

func TestDashboardStreamsOutliveTheListenersWriteTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.5s of real work; the fast class runs under -short")
	}
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{ID: "r1", Pipeline: "demo", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	srv := controller.New(st, nil).WithDashboard(controller.Dashboard{Paths: paths.PathsAt(t.TempDir())})
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	ts := httptest.NewUnstartedServer(srv.Handler())
	// safety: the stream polls every quarter second, so a millisecond write timeout has long expired by the time
	// the event below is sent unless the stream extends its own deadline.
	ts.Config.WriteTimeout = time.Millisecond
	ts.Start()
	t.Cleanup(ts.Close)

	resp, err := ts.Client().Get(ts.URL + "/api/v1/runs/r1/events/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	scanner := bufio.NewScanner(resp.Body)
	appended := false
	for scanner.Scan() {
		if !appended && strings.HasPrefix(scanner.Text(), ": open") {
			if _, err := st.AppendEvent(ctx, "r1", "", "late_event", []byte(`{}`)); err != nil {
				t.Fatal(err)
			}
			appended = true
		}
		if strings.Contains(scanner.Text(), "late_event") {
			return
		}
	}
	t.Fatalf("stream ended before the late event: %v", scanner.Err())
}
