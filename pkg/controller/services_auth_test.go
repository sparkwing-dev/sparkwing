package controller_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/license"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestServices_RequiresABearer(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	now := time.Now().UTC()
	raw, _, err := st.CreateToken("runner", store.TokenKindRunner,
		[]string{controller.ScopeNodesClaim}, 0, now)
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	srv := httptest.NewServer(controller.New(st, nil).
		EnableAuthFromStore().
		WithCachePodURL("http://cache.internal:8080").
		WithLogsURL("http://logs.internal:8081").
		WithDashboardURL("https://dash.internal").
		Handler())
	t.Cleanup(srv.Close)

	anon, err := http.Get(srv.URL + "/api/v1/services")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = anon.Body.Close() }()
	if anon.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d, want 401", anon.StatusCode)
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/api/v1/services", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+raw)
	authed, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = authed.Body.Close() }()
	if authed.StatusCode != http.StatusOK {
		t.Fatalf("runner-token status = %d, want 200", authed.StatusCode)
	}
	var body controller.ServicesResponse
	if err := json.NewDecoder(authed.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.CachePod != "http://cache.internal:8080" {
		t.Fatalf("cache_pod = %q", body.CachePod)
	}
	if body.Dashboard != "https://dash.internal" {
		t.Fatalf("dashboard = %q", body.Dashboard)
	}
}

func TestServices_AnnouncesADashboardOnItsOwn(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	srv := httptest.NewServer(controller.New(st, nil).
		WithDashboardURL("https://dash.internal").
		Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/api/v1/services")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body controller.ServicesResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Dashboard != "https://dash.internal" || body.CachePod != "" || body.Logs != "" {
		t.Fatalf("services = %+v", body)
	}
}

func servicesAs(t *testing.T, url, token string) (int, controller.ServicesResponse) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url+"/api/v1/services", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var body controller.ServicesResponse
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatalf("decode: %v", err)
		}
	}
	return resp.StatusCode, body
}

// A multi-team controller says so even when it announces no URL, so a CLI
// knows the cache's operator routes are not the caller's to use.
func TestServices_AnnouncesAMultiTeamController(t *testing.T) {
	for _, multiTeam := range []bool{true, false} {
		st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
		raw, _, err := st.CreateToken("runner", store.TokenKindRunner,
			[]string{controller.ScopeNodesClaim}, 0, time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		srv := controller.New(st, nil)
		if multiTeam {
			lic, pub := multiTeamLicense(t)
			srv.WithLicense(license.Resolve(lic, pub, time.Now(), nil))
		}
		ts := httptest.NewServer(srv.EnableAuthFromStore().Handler())
		t.Cleanup(ts.Close)

		code, body := servicesAs(t, ts.URL, raw)
		switch {
		case multiTeam && (code != http.StatusOK || !body.MultiTeam):
			t.Fatalf("multi-team controller with nothing announced = %d %+v, want 200 naming it multi-team", code, body)
		case !multiTeam && code != http.StatusNotFound:
			t.Fatalf("single-team controller with nothing announced = %d %+v, want 404", code, body)
		}
	}
}
