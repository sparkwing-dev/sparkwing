package controller_test

import (
	"bufio"
	"context"
	"crypto/sha256"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
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
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"not_found"`) {
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

func TestBrowserPagesRequireSignInBeforeBootstrap(t *testing.T) {
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := controller.New(st, nil).WithDashboard(controller.Dashboard{Bundle: pageBundle})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, dashOrigin+"/", nil))
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/login?next=") {
		t.Fatalf("unseeded controller page = %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, dashOrigin+"/operator", nil))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("operator page without a session = %d, want 303", rec.Code)
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
	f := newBrowserFixture(t, &controller.Dashboard{Bundle: pageBundle})
	session := &http.Cookie{Name: "__Host-sw_session", Value: f.sessID}
	if rec := f.get("/operator", session); rec.Code != http.StatusForbidden {
		t.Fatalf("unlisted account = %d", rec.Code)
	}
	f.srv.WithOperatorAccounts([]string{"alice"})
	if rec := f.get("/operator", session); rec.Code != http.StatusOK {
		t.Fatalf("listed account = %d %s", rec.Code, rec.Body)
	}
	principal, err := f.st.LookupCapability(t.Context(), sha256.Sum256([]byte(f.token)))
	if err != nil {
		t.Fatal(err)
	}
	raw, _, err := f.st.CreateCapabilitySession(t.Context(), principal.Capability.Prefix, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if rec := f.get("/operator", &http.Cookie{Name: "__Host-sw_session", Value: raw}); rec.Code != http.StatusForbidden {
		t.Fatalf("capability session = %d", rec.Code)
	}
	f.srv.WithOperatorAccounts(nil)
	if rec := f.get("/operator", session); rec.Code != http.StatusForbidden {
		t.Fatalf("removed operator = %d", rec.Code)
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
	for _, cookie := range f.serve(login).Result().Cookies() {
		if cookie.Name == "__Host-sw_session" || cookie.Name == "sw_session" {
			t.Fatal("local mode signed a browser in")
		}
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
	if err := teststore.SeedRun(f.store, ctx, team.Team(), store.Run{ID: "run-a", Pipeline: "demo", Status: "success", StartedAt: time.Now()}); err != nil {
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
	if err := teststore.SeedRun(st, ctx, store.DefaultTeam, store.Run{ID: "r1", Pipeline: "demo", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	srv := controller.New(st, nil).WithDashboard(controller.Dashboard{Paths: paths.PathsAt(t.TempDir())})
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
			if err := teststore.ForTeam(t, st, store.DefaultTeam).AppendEvent(ctx, "r1", "", "late_event", []byte(`{}`)); err != nil {
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

type browserRunDeleter struct {
	controller.TeamWriter
	tenant *store.Tenant
	seen   capability.Authorized
}

func (d *browserRunDeleter) Team() store.Team { return d.tenant.Team() }
func (d *browserRunDeleter) DeleteAuthorizedRun(ctx context.Context, a capability.Authorized) error {
	d.seen = a
	return nil
}

func TestDeleteRunOnALocalDashboardRemovesTheRunDirectory(t *testing.T) {
	p := paths.PathsAt(t.TempDir())
	f := newBrowserFixture(t, &controller.Dashboard{Local: true, Paths: p})
	ctx := context.Background()
	tenant, err := f.st.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	writer := &browserRunDeleter{tenant: tenant}
	f.srv.WithTeamWriter(func(context.Context, store.Team) (controller.TeamWriter, error) { return writer, nil })
	f.h = f.srv.Handler()
	for i, id := range []string{"r1", "r2", `a\b`} {
		if _, err := f.st.DB().ExecContext(ctx, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,allowed_pools,created_principal,created_at,finished_at,log_retention_class,log_retention_days) VALUES ('default',?,?,'demo','success',?,'request',1,'github.com/acme/app','host','execute',?,'alice',1,2,'r30',30)`, id, i+1, []byte(`{}`), []byte(`[]`)); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(p.RunDir(id), "logs"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	del := func(escaped string) int {
		req := httptest.NewRequest(http.MethodDelete, dashOrigin+"/api/v1/runs/"+escaped, nil)
		req.Header.Set("Authorization", "Bearer "+f.token)
		return f.serve(req).Code
	}
	if code := del("r1"); code != http.StatusNoContent {
		t.Fatalf("delete r1 = %d, want 204", code)
	}
	if writer.seen.Target.RunID != "r1" || writer.seen.Target.Team != string(store.DefaultTeam) || writer.seen.Principal.Capability.Subject.ID != "alice" {
		t.Fatalf("writer authority = %+v", writer.seen)
	}
	if _, err := os.Stat(p.RunDir("r1")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("r1's directory after the delete: %v, want it gone", err)
	}
	if _, err := os.Stat(p.RunDir("r2")); err != nil {
		t.Fatalf("r2's directory after r1's delete: %v", err)
	}
	if code := del(`a%5Cb`); code != http.StatusBadRequest {
		t.Fatalf("delete a run whose id is not a path segment = %d, want 400", code)
	}
	if _, err := os.Stat(p.RunDir(`a\b`)); err != nil {
		t.Fatalf("the refused id's directory: %v, want it left alone", err)
	}
	if _, err := tenant.ReadRun(ctx, `a\b`); err != nil {
		t.Fatalf("the refused id's run row: %v, want it kept", err)
	}
}
