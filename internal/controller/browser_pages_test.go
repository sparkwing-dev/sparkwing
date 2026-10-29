package controller_test

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
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

	"github.com/sparkwing-dev/sparkwing/internal/runstate"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
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
	principal, err := f.st.AsOperator().LookupCapability(t.Context(), sha256.Sum256([]byte(f.token)))
	if err != nil {
		t.Fatal(err)
	}
	raw, _, err := f.st.AsOperator().CreateCapabilitySession(t.Context(), principal.Capability.Prefix, time.Hour)
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
	f := newBrowserFixture(t, nil)
	ctx := t.Context()
	teststore.SeedTeam(t, f.st, "other")
	own, _, err := teststore.ProvisionCredential(ctx, f.st, capability.ProvisionSpec{Kind: capability.ProvisionMember, AllowTeamAdmin: true, Team: string(store.DefaultTeam), Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "alice"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.RunsRead, capability.LogsRead}})
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := teststore.ProvisionCredential(ctx, f.st, capability.ProvisionSpec{Kind: capability.ProvisionMember, AllowTeamAdmin: true, Team: "other", Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "other"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.RunsRead, capability.LogsRead}})
	if err != nil {
		t.Fatal(err)
	}
	if err := teststore.SeedRun(f.st, ctx, store.DefaultTeam, store.Run{RunSummary: runstate.RunSummary{ID: "run-a", Pipeline: "private-team-pipeline", Status: "success", StartedAt: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	if err := teststore.SeedNode(f.st, ctx, store.Node{RunID: "run-a", NodeID: "n1", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	get := func(path, token string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		return f.serve(req)
	}
	for _, path := range []string{"/api/v1/runs/run-a/logs", "/api/v1/runs/run-a/jobs/n1/logs/completeness", "/api/v1/runs/run-a/events/stream"} {
		if rec := get(path, own); rec.Code != http.StatusOK {
			t.Fatalf("own team %s = %d %s", path, rec.Code, rec.Body)
		}
		if rec := get(path, other); rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "private-team-pipeline") {
			t.Fatalf("foreign team %s = %d %s", path, rec.Code, rec.Body)
		}
	}
	rec := get("/api/v1/runs/grep?q=x", other)
	var grep struct {
		RunsMatching int `json:"runs_matching"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &grep); err != nil || rec.Code != http.StatusOK || grep.RunsMatching != 0 {
		t.Fatalf("foreign grep = %d %s: %v", rec.Code, rec.Body, err)
	}
	if rec := get("/api/v1/capacity/profiles", own); rec.Code != http.StatusNotFound {
		t.Fatalf("retired capacity profiles = %d", rec.Code)
	}
}

func TestDashboardStreamsOutliveTheListenersWriteTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.5s of real work; the fast class runs under -short")
	}
	f := newBrowserFixture(t, &controller.Dashboard{Paths: paths.PathsAt(t.TempDir())})
	ctx := t.Context()
	if err := teststore.SeedRun(f.st, ctx, store.DefaultTeam, store.Run{RunSummary: runstate.RunSummary{ID: "r1", Pipeline: "demo", Status: "running", StartedAt: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewUnstartedServer(f.h)
	// safety: the stream polls every quarter second, so a millisecond write timeout has long expired by the time
	// the event below is sent unless the stream extends its own deadline.
	ts.Config.WriteTimeout = time.Millisecond
	ts.Start()
	t.Cleanup(ts.Close)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/v1/runs/r1/events/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+f.token)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream = %d", resp.StatusCode)
	}
	scanner := bufio.NewScanner(resp.Body)
	appended := false
	for scanner.Scan() {
		if !appended && strings.HasPrefix(scanner.Text(), ": open") {
			if err := teststore.ForTeam(t, f.st, store.DefaultTeam).AppendEvent(ctx, "r1", "", "late_event", []byte(`{}`)); err != nil {
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

// An open event stream ends once its caller's membership does, rather than
// following the run for as long as it lasts.
func TestRunEventStreamEndsWhenMembershipEnds(t *testing.T) {
	f := newBrowserFixture(t, &controller.Dashboard{Paths: paths.PathsAt(t.TempDir())})
	if err := teststore.SeedRun(f.st, t.Context(), store.DefaultTeam, store.Run{RunSummary: runstate.RunSummary{ID: "r1", Pipeline: "demo", Status: "running", StartedAt: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(f.h)
	t.Cleanup(ts.Close)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/v1/runs/r1/events/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+f.token)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream opened with %d", resp.StatusCode)
	}
	scanner := bufio.NewScanner(resp.Body)
	removed := false
	for scanner.Scan() {
		if !removed && strings.HasPrefix(scanner.Text(), ": open") {
			result, err := teststore.SQL(t, f.st).ExecContext(t.Context(), `DELETE FROM memberships WHERE account_id='alice'`)
			if err != nil {
				t.Fatal(err)
			}
			if n, err := result.RowsAffected(); err != nil || n != 1 {
				t.Fatalf("membership removal changed %d rows: %v", n, err)
			}
			if err := teststore.ForTeam(t, f.st, store.DefaultTeam).AppendEvent(t.Context(), "r1", "", "after_removal", []byte(`{}`)); err != nil {
				t.Fatal(err)
			}
			removed = true
		}
	}
	if !removed {
		t.Fatal("the stream ended before it opened")
	}
	if err := scanner.Err(); err != nil || ctx.Err() != nil {
		t.Fatalf("the stream outlived the membership: %v %v", err, ctx.Err())
	}
}

type browserRunDeleter struct {
	*store.Tenant
	seen capability.Authorized
}

func (d *browserRunDeleter) DeleteAuthorizedRun(ctx context.Context, a capability.Authorized, _ func(context.Context) error) error {
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
	writer := &browserRunDeleter{Tenant: tenant}
	f.srv.WithTenantResolver(func(context.Context, store.Team) (controller.TenantHandle, error) { return writer, nil })
	f.h = f.srv.Handler()
	for i, id := range []string{"r1", "r2", `a\b`} {
		if _, err := teststore.SQL(t, f.st).ExecContext(ctx, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,created_principal,created_at,finished_at,log_retention_class,log_retention_days) VALUES ('default',?,?,'demo','success',?,'request',1,'github.com/acme/app','host','execute','alice',1,2,'r30',30)`, id, i+1, []byte(`{}`)); err != nil {
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
