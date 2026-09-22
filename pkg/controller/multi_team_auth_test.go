package controller_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/license"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// emptyTokenTableServer is a controller whose tokens table holds no row, the
// state of a fresh install before anyone mints a token.
func emptyTokenTableServer(t *testing.T, multiTeam bool) (*controller.Server, *store.Store, string) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := controller.New(st, nil).EnableAuthFromStore()
	if multiTeam {
		raw, pub := multiTeamLicense(t)
		srv.WithLicense(license.Resolve(raw, pub, time.Now(), nil))
		if !srv.MultiTeam() {
			t.Fatal("the test license did not make the controller multi-team")
		}
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	return srv, st, ts.URL
}

func anonymousStatus(t *testing.T, method, url string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// On a multi-team controller a request with no credential is an anonymous
// caller acting for the default team with every scope, so it is refused even
// while the tokens table is empty.
func TestMultiTeamAuth_AnEmptyTokenTableStillRefusesAnonymousCallers(t *testing.T) {
	srv, st, url := emptyTokenTableServer(t, true)
	for _, route := range []string{
		"GET /api/v1/runs",
		"GET /api/v1/users",
		"GET /api/v1/tokens",
	} {
		method, path, _ := strings.Cut(route, " ")
		if code := anonymousStatus(t, method, url+path); code != http.StatusUnauthorized {
			t.Errorf("%s with no credential on a multi-team controller = %d, want 401", route, code)
		}
	}
	if !srv.AuthEnabled() {
		t.Error("a multi-team controller reports authentication disabled")
	}
	if code := anonymousStatus(t, "GET", url+"/api/v1/health"); code != http.StatusOK {
		t.Errorf("GET /api/v1/health with no credential = %d, want 200", code)
	}

	raw, _, err := st.CreateToken("late", store.TokenKindUser, []string{controller.ScopeAdmin}, 0, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(context.Background(), "GET", url+"/api/v1/runs", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+raw)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /api/v1/runs with a token minted after startup = %d, want 200", resp.StatusCode)
	}
}

// A single-team install with no token keeps serving unauthenticated, which is
// the laptop-local mode.
func TestMultiTeamAuth_ASingleTeamInstallStaysOpenWithNoTokens(t *testing.T) {
	srv, _, url := emptyTokenTableServer(t, false)
	if code := anonymousStatus(t, "GET", url+"/api/v1/runs"); code != http.StatusOK {
		t.Errorf("GET /api/v1/runs with no credential on a single-team install = %d, want 200", code)
	}
	if srv.AuthEnabled() {
		t.Error("a single-team install with no tokens reports authentication enabled")
	}
}

// The controller binary installs the license before resolving auth, so
// --require-auth and the startup log already see auth on.
func TestMultiTeamAuth_ALicenseInstalledFirstTurnsAuthOnAtEnable(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	raw, pub := multiTeamLicense(t)
	srv := controller.New(st, nil).WithLicense(license.Resolve(raw, pub, time.Now(), nil)).EnableAuthFromStore()
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	if !srv.AuthEnabled() {
		t.Error("EnableAuthFromStore left a multi-team controller with an empty tokens table unauthenticated")
	}
}
