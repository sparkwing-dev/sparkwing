package logs

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// A logs service in front of a multi-team controller: team A's run has a log,
// and team B holds tokens that would read it if the service only checked
// scopes.
func TestLogReadsStayInsideTheCallersTeam(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, _, err := st.CreateToken("root", store.TokenKindUser, []string{controller.ScopeAdmin}, 0, now); err != nil {
		t.Fatal(err)
	}
	ctrl := controller.New(st, nil).EnableAuthFromStore()
	cts := httptest.NewServer(ctrl.Handler())
	t.Cleanup(cts.Close)

	signIn := func(name string) (store.Account, *store.Tenant) {
		res, err := st.ResolveSignIn(ctx, store.SignInProfile{
			Provider: "google", Subject: "sub-" + name, Email: name + "@example.test",
			EmailVerified: true, Name: name,
		}, now)
		if err != nil {
			t.Fatal(err)
		}
		tn, err := st.ForTeam(ctx, res.PersonalTeam)
		if err != nil {
			t.Fatal(err)
		}
		return res.Account, tn
	}
	forDefault, err := st.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	alice, teamA := signIn("alice")
	_, teamB := signIn("bob")
	mint := func(tn *store.Tenant, name string, scopes ...string) string {
		raw, _, err := tn.CreateToken(ctx, name, store.TokenKindUser, scopes, 0, now)
		if err != nil {
			t.Fatal(err)
		}
		return "Bearer " + raw
	}
	// safety: the operator's admin token appends without a runner's claim, and
	// only the operator's own team may hold that scope.
	writerA := mint(forDefault, "operator", controller.ScopeAdmin, controller.ScopeLogsWrite)
	readerA := mint(teamA, "a-reader", controller.ScopeLogsRead)
	readerB := mint(teamB, "b-reader", controller.ScopeLogsRead)
	everyB := mint(teamB, "b-every", controller.ScopeLogsRead, controller.ScopeLogsWrite,
		controller.ScopeRunsRead, controller.ScopeTeamAdmin)
	writerB := mint(teamB, "b-writer", controller.ScopeLogsWrite)
	rawSession, _, _, err := st.CreateAccountSession(ctx, alice, teamA.Team(), time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	sessionA := "Session " + rawSession

	const runA = "run-team-a"
	if err := teamA.CreateTriggerWithRun(ctx,
		store.Trigger{ID: runA, Pipeline: "build", TriggerSource: "api", CreatedAt: now},
		store.Run{ID: runA, Pipeline: "build", Status: "running", CreatedAt: now, StartedAt: now},
	); err != nil {
		t.Fatal(err)
	}

	logs, err := New(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	logs.WithControllerAuth(cts.URL, time.Minute)
	lts := httptest.NewServer(logs.Handler())
	t.Cleanup(lts.Close)

	call := func(method, path, auth, body string) (int, string) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, method, lts.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", auth)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(raw)
	}

	const secret = "team-a-deploy-output"
	if code, body := call("POST", "/api/v1/logs/"+runA+"/n1", writerA, secret+"\n"); code/100 != 2 {
		t.Fatalf("append as team A = %d: %s", code, body)
	}
	reads := []string{
		"/api/v1/logs/" + runA + "/n1",
		"/api/v1/logs/" + runA,
		"/api/v1/logs/search?q=deploy&run_id=" + runA,
	}
	for _, path := range reads {
		for _, own := range []struct{ name, auth string }{{"team A token", readerA}, {"team A session", sessionA}} {
			if code, body := call("GET", path, own.auth, ""); code != http.StatusOK || !strings.Contains(body, secret) {
				t.Errorf("GET %s as %s = %d without its own log: %s", path, own.name, code, body)
			}
		}
		for _, other := range []struct{ name, auth string }{{"team B reader", readerB}, {"team B token with every team scope", everyB}} {
			if code, body := call("GET", path, other.auth, ""); code != http.StatusNotFound || strings.Contains(body, secret) {
				t.Errorf("GET %s as %s = %d want 404: %s", path, other.name, code, body)
			}
		}
	}
	if code, body := call("DELETE", "/api/v1/logs/"+runA, writerB, ""); code != http.StatusNotFound {
		t.Errorf("DELETE as team B = %d want 404: %s", code, body)
	}
	if code, body := call("GET", "/api/v1/logs/"+runA+"/n1", readerA, ""); !strings.Contains(body, secret) {
		t.Errorf("team B's delete removed team A's log: %d %s", code, body)
	}
}
