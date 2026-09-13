package controller_test

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const bootstrapAdminRaw = "swu_provisionedbootstrapadmin00001"

func openBootstrapStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func statusWithBearer(t *testing.T, url, token string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func TestEnsureBootstrapAdminToken_NoUnauthenticatedWindow(t *testing.T) {
	st := openBootstrapStore(t)
	now := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)

	created, err := controller.EnsureBootstrapAdminToken(st, bootstrapAdminRaw, now)
	if err != nil {
		t.Fatalf("EnsureBootstrapAdminToken: %v", err)
	}
	if !created {
		t.Fatal("a fresh store reported no bootstrap token created")
	}

	srv := controller.New(st, nil).EnableAuthFromStore()
	if !srv.AuthEnabled() {
		t.Fatal("the bootstrap token left authentication disabled, so --require-auth would refuse to start")
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	if got := statusWithBearer(t, ts.URL+"/api/v1/tokens", ""); got != http.StatusUnauthorized {
		t.Fatalf("first unauthenticated request status=%d, want 401", got)
	}
	if got := statusWithBearer(t, ts.URL+"/api/v1/tokens", bootstrapAdminRaw); got != http.StatusOK {
		t.Fatalf("bootstrap token status=%d, want 200", got)
	}
}

func TestEnsureBootstrapAdminToken_SkipsAPopulatedTable(t *testing.T) {
	st := openBootstrapStore(t)
	now := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)

	existing, _, err := st.CreateToken("operator", store.TokenKindUser,
		[]string{controller.ScopeAdmin}, 0, now)
	if err != nil {
		t.Fatalf("seed token: %v", err)
	}

	created, err := controller.EnsureBootstrapAdminToken(st, bootstrapAdminRaw, now)
	if err != nil {
		t.Fatalf("EnsureBootstrapAdminToken: %v", err)
	}
	if created {
		t.Fatal("a populated tokens table accepted a bootstrap token")
	}

	ts := httptest.NewServer(controller.New(st, nil).EnableAuthFromStore().Handler())
	defer ts.Close()

	if got := statusWithBearer(t, ts.URL+"/api/v1/tokens", bootstrapAdminRaw); got != http.StatusUnauthorized {
		t.Fatalf("unwritten bootstrap token status=%d, want 401", got)
	}
	if got := statusWithBearer(t, ts.URL+"/api/v1/tokens", existing); got != http.StatusOK {
		t.Fatalf("pre-existing token status=%d, want 200", got)
	}
}

func TestEnsureBootstrapAdminToken_EmptyValueCreatesNothing(t *testing.T) {
	st := openBootstrapStore(t)

	created, err := controller.EnsureBootstrapAdminToken(st, "", time.Now().UTC())
	if err != nil {
		t.Fatalf("EnsureBootstrapAdminToken: %v", err)
	}
	if created {
		t.Fatal("an empty bootstrap value created a token")
	}
	toks, err := st.ListTokens("", true)
	if err != nil {
		t.Fatalf("ListTokens: %v", err)
	}
	if len(toks) != 0 {
		t.Fatalf("ListTokens len=%d want 0", len(toks))
	}
}

func TestEnsureBootstrapAdminToken_RefusesAMalformedValue(t *testing.T) {
	st := openBootstrapStore(t)

	if _, err := controller.EnsureBootstrapAdminToken(st, "not-a-token", time.Now().UTC()); err == nil {
		t.Fatal("EnsureBootstrapAdminToken accepted a value no bearer lookup could resolve")
	}
}
