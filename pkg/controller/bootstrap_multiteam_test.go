package controller_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/license"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func freshController(t *testing.T, lic *license.License) (string, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := controller.New(st, nil).WithLicense(lic)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	return ts.URL, st
}

func TestBootstrap_MultiTeamControllerRefusesAWebSignup(t *testing.T) {
	raw, pub := multiTeamLicense(t)
	base, st := freshController(t, license.Resolve(raw, pub, time.Now(), nil))

	if getBootstrapNeeded(t, base) {
		t.Fatal("a fresh multi-team controller offered the first-admin signup")
	}
	status, body := postJSONWithStatus(t, base+"/api/v1/users", map[string]string{
		"name": "attacker", "password": "correctbatteryhorse",
	})
	if status != http.StatusForbidden {
		t.Fatalf("web signup on a multi-team controller = %d %s, want 403", status, body)
	}
	if n, err := st.CountUsers(); err != nil || n != 0 {
		t.Fatalf("users after the refused signup = %d (%v), want 0", n, err)
	}
}

func TestBootstrap_SingleTeamControllerKeepsTheWebSignup(t *testing.T) {
	base, _ := freshController(t, nil)
	if !getBootstrapNeeded(t, base) {
		t.Fatal("a fresh single-team controller stopped offering the first-admin signup")
	}
	status, body := postJSONWithStatus(t, base+"/api/v1/users", map[string]string{
		"name": "admin", "password": "correctbatteryhorse",
	})
	if status != http.StatusCreated {
		t.Fatalf("single-team bootstrap = %d %s, want 201", status, body)
	}
}
